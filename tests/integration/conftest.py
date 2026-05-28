"""
Cluster lifecycle and shared fixtures for btrepl integration tests.

Topology: btrepl1 (master) + btrepl2/3/4 (slaves) on OrbStack.
All remote access is via SSH — either orbctl-exec for CLI or SSH port-forwarding
for gRPC (since btrepl:50051 is not exposed on the host network).

Pre-requisites:
  - OrbStack running with a base image named SOURCE_MACHINE
  - arm64 .deb already built: make deb
  - SSH keys: OrbStack cloned VMs inherit the host's authorized_keys by default

Quick start:
    uv run pytest tests/integration/ -v --spin-up               # spin up + test
    uv run pytest tests/integration/ -v --spin-up --teardown    # spin up + test + destroy
    uv run pytest tests/integration/ -v                         # test only (VMs already up)
    uv run pytest tests/integration/ -v --destructive           # also run destructive tests
"""

import os
import socket
import subprocess
import time
from collections.abc import Generator, Iterator
from contextlib import ExitStack, contextmanager
from pathlib import Path

import pytest

# Suppress gRPC INFO-level fork-handler warnings that appear when subprocess.Popen
# is called while gRPC threads are active (expected in a test environment).
os.environ.setdefault("GRPC_VERBOSITY", "ERROR")

from btrepl import BtreplClient  # noqa: E402

# ── topology ──────────────────────────────────────────────────────────────────

MASTER = "btrepl1"
SLAVES = ["btrepl2", "btrepl3", "btrepl4"]
ALL_NODES = [MASTER] + SLAVES
SOURCE_MACHINE = "debian-arm-13"

BTRFS_IMG   = "/btrfs.img"
BTRFS_MNT   = "/btrfs"
SSH_KEY     = "/root/.ssh/id_ed25519"
SUBVOLS     = ["@eventsPictures", "@reports"]
SNAP_DIR    = ".snapshots"
SNAP_PREFIX = "btrepl_"
GRPC_PORT   = 50051

REPO_ROOT = Path(__file__).parent.parent.parent

# ── pytest options / markers ──────────────────────────────────────────────────


def pytest_addoption(parser: pytest.Parser) -> None:
    parser.addoption(
        "--spin-up", action="store_true", default=False,
        help="Create/start the 4-node OrbStack cluster before tests.",
    )
    parser.addoption(
        "--teardown", action="store_true", default=False,
        help="Delete all cluster VMs after tests.",
    )
    parser.addoption(
        "--destructive", action="store_true", default=False,
        help="Also run destructive tests (clear, standalone). Off by default.",
    )


def pytest_configure(config: pytest.Config) -> None:
    config.addinivalue_line(
        "markers",
        "destructive: modifies cluster state irreversibly (skipped unless --destructive)",
    )


def pytest_collection_modifyitems(
    config: pytest.Config, items: list[pytest.Item]
) -> None:
    if not config.getoption("--destructive"):
        skip = pytest.mark.skip(reason="pass --destructive to run")
        for item in items:
            if item.get_closest_marker("destructive"):
                item.add_marker(skip)


# ── remote execution via orbctl ───────────────────────────────────────────────


def rem(machine: str, cmd: str, *, check: bool = True) -> subprocess.CompletedProcess:
    """Run a bash command on an OrbStack machine as root."""
    return subprocess.run(
        ["orbctl", "run", "-m", machine, "-u", "root", "bash", "-c", cmd],
        capture_output=True, text=True, check=check,
    )


def push(machine: str, local: Path | str, remote: str) -> None:
    subprocess.run(["orbctl", "push", "-m", machine, str(local), remote], check=True)


def machine_exists(name: str) -> bool:
    r = subprocess.run(["orbctl", "list"], capture_output=True, text=True)
    return any(
        line.split() and line.split()[0] == name
        for line in r.stdout.splitlines()
    )


def wait_ssh(machine: str, retries: int = 45, delay: float = 3.0) -> None:
    print(f"  [{machine}] waiting for ssh ...", flush=True)
    for _ in range(retries):
        if rem(machine, "echo ok", check=False).returncode == 0:
            return
        time.sleep(delay)
    raise TimeoutError(f"{machine} not reachable after {retries * delay:.0f} s")


def get_ip(machine: str) -> str:
    return rem(machine, "hostname -I | awk '{print $1}'").stdout.strip()


# ── SSH port-forwarding tunnel ────────────────────────────────────────────────

_SSH_OPTS = [
    "-o", "StrictHostKeyChecking=no",
    "-o", "ExitOnForwardFailure=yes",
    "-o", "ConnectTimeout=10",
    "-o", "BatchMode=yes",
]


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_local_port(port: int, timeout: float = 15.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                return
        except OSError:
            time.sleep(0.3)
    raise TimeoutError(f"local port {port} not open after {timeout} s")


@contextmanager
def ssh_tunnel(machine: str, remote_port: int = GRPC_PORT) -> Iterator[int]:
    """
    Forward a free local port to machine:remote_port via SSH.
    OrbStack VMs are reachable at <machine>.orb.local.
    Yields the local port number.
    """
    port = _free_port()
    proc = subprocess.Popen(
        [
            "ssh", *_SSH_OPTS,
            "-N",
            "-L", f"127.0.0.1:{port}:127.0.0.1:{remote_port}",
            f"root@{machine}.orb.local",
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        _wait_local_port(port)
        yield port
    finally:
        proc.terminate()
        proc.wait(timeout=5)


# ── cluster setup / teardown ──────────────────────────────────────────────────


def _install_node(machine: str, deb: Path) -> None:
    """Install btrfs tools + btrepl package on a single node."""
    rem(machine, f"""
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq
        apt-get install -y -qq btrfs-progs
        if [ ! -f {BTRFS_IMG} ]; then
            fallocate -l 2G {BTRFS_IMG}
            mkfs.btrfs {BTRFS_IMG}
        fi
        mkdir -p {BTRFS_MNT}
        mountpoint -q {BTRFS_MNT} || mount -o loop {BTRFS_IMG} {BTRFS_MNT}
        grep -q '{BTRFS_IMG}' /etc/fstab || \
            echo '{BTRFS_IMG} {BTRFS_MNT} btrfs loop,nofail 0 0' >> /etc/fstab
    """)
    for sv in SUBVOLS:
        rem(machine, (
            f"btrfs subvolume show {BTRFS_MNT}/{sv} &>/dev/null || "
            f"btrfs subvolume create {BTRFS_MNT}/{sv}"
        ))
    push(machine, deb, "/tmp/btrepl.deb")
    rem(machine, "dpkg -i /tmp/btrepl.deb || apt-get install -f -y -qq")
    rem(machine, "systemctl stop btrepl.service 2>/dev/null || true")


def _write_config(machine: str) -> None:
    sv_yaml = "\n".join(f'  - "{sv}"' for sv in SUBVOLS)
    cfg = (
        f"btrfs_root: {BTRFS_MNT}\n"
        f"ssh_identity: {SSH_KEY}\n"
        f"ssh_user: root\n"
        f"snapshot_dir: {SNAP_DIR}\n"
        f"snapshot_prefix: {SNAP_PREFIX}\n"
        f"interval: 1h\n"
        f"keep_sender: 10\n"
        f"keep_receiver: 5\n"
        f"subvolumes:\n{sv_yaml}\n"
        f"slaves: []\n"
    )
    tmp = Path(f"/tmp/btrepl_cfg_{machine}.yaml")
    tmp.write_text(cfg)
    push(machine, tmp, "/tmp/btrepl_config.yaml")
    tmp.unlink()
    rem(machine, "mkdir -p /etc/btrepl && mv /tmp/btrepl_config.yaml /etc/btrepl/config.yaml")


def _setup_cluster() -> None:
    debs = sorted(REPO_ROOT.glob("build/*arm64.deb"))
    if not debs:
        raise FileNotFoundError("No arm64 .deb in build/ — run: make deb")
    deb = debs[-1]
    print(f"\n[btrepl] package: {deb.name}", flush=True)

    # 1. clone missing VMs
    for m in ALL_NODES:
        if not machine_exists(m):
            print(f"[btrepl] cloning {SOURCE_MACHINE} → {m} ...", flush=True)
            subprocess.run(["orbctl", "clone", SOURCE_MACHINE, m], check=True)

    # 2. start all VMs and wait for SSH
    for m in ALL_NODES:
        subprocess.run(["orbctl", "start", m], capture_output=True)
    for m in ALL_NODES:
        wait_ssh(m)

    # 3. install btrfs + btrepl, write config on every node
    for m in ALL_NODES:
        print(f"[btrepl] provisioning {m} ...", flush=True)
        _install_node(m, deb)
        _write_config(m)

    # 4. generate SSH key on master, authorize it on every slave
    rem(MASTER, f"""
        mkdir -p /root/.ssh && chmod 700 /root/.ssh
        [ -f {SSH_KEY} ] || ssh-keygen -t ed25519 -N '' -f {SSH_KEY} -q
    """)
    pubkey = rem(MASTER, f"cat {SSH_KEY}.pub").stdout.strip()

    for slave in SLAVES:
        slave_ip = get_ip(slave)
        rem(slave, f"""
            mkdir -p /root/.ssh && chmod 700 /root/.ssh
            touch /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys
            grep -qF '{pubkey}' /root/.ssh/authorized_keys || \
                echo '{pubkey}' >> /root/.ssh/authorized_keys
        """)
        rem(MASTER, f"ssh-keyscan -H {slave_ip} >> /root/.ssh/known_hosts 2>/dev/null || true")

    # 5. init master, add all three slaves
    rem(MASTER, "btrepl init-master")
    for slave in SLAVES:
        slave_ip = get_ip(slave)
        rem(MASTER, f"btrepl add-slave -s {slave_ip}")

    # 6. start btrepl daemon on every node
    for m in ALL_NODES:
        rem(m, "systemctl daemon-reload && systemctl enable btrepl.service && systemctl start btrepl.service")
        rem(m, "systemctl is-active btrepl.service")

    print("[btrepl] cluster ready", flush=True)


def _teardown_cluster() -> None:
    print("\n[btrepl] tearing down cluster ...", flush=True)
    for m in ALL_NODES:
        if machine_exists(m):
            subprocess.run(["orbctl", "delete", "-f", m], check=False)


# ── session-scoped fixtures ───────────────────────────────────────────────────


@pytest.fixture(scope="session", autouse=True)
def cluster(request: pytest.FixtureRequest) -> Generator[None, None, None]:
    """Create/verify the 4-node cluster. Tear it down if --teardown is given."""
    if request.config.getoption("--spin-up"):
        _setup_cluster()
    yield
    if request.config.getoption("--teardown"):
        _teardown_cluster()


@pytest.fixture(scope="session")
def slave_ips() -> list[str]:
    """Actual IP addresses of the three slave nodes (fetched once per session)."""
    return [get_ip(s) for s in SLAVES]


@pytest.fixture(scope="session")
def master_client() -> Generator[BtreplClient, None, None]:
    """Long-lived BtreplClient for btrepl1, connected via SSH tunnel."""
    with ssh_tunnel(MASTER) as port:
        with BtreplClient(f"127.0.0.1:{port}", timeout=15.0) as c:
            yield c


@pytest.fixture(scope="session")
def slave_clients() -> Generator[list[BtreplClient], None, None]:
    """One BtreplClient per slave, each via its own SSH tunnel."""
    with ExitStack() as stack:
        clients: list[BtreplClient] = []
        for slave in SLAVES:
            port = stack.enter_context(ssh_tunnel(slave))
            client = BtreplClient(f"127.0.0.1:{port}", timeout=15.0)
            stack.enter_context(client)
            clients.append(client)
        yield clients


@pytest.fixture(scope="session")
def grpc_client_factory() -> Iterator[object]:
    """
    Factory for creating short-lived gRPC clients via fresh SSH tunnels.
    Use for WatchLogs tests or any test that needs an independent channel.

    Usage:
        with grpc_client_factory(MASTER) as client:
            for entry in client.watch_logs(): ...
    """
    @contextmanager
    def _make(machine: str = MASTER, timeout: float = 30.0) -> Iterator[BtreplClient]:
        with ssh_tunnel(machine) as port:
            with BtreplClient(f"127.0.0.1:{port}", timeout=timeout) as c:
                yield c

    yield _make


# ── CLI helper ────────────────────────────────────────────────────────────────


class NodeCLI:
    """Runs btrepl CLI commands on a remote machine via orbctl."""

    def __init__(self, machine: str) -> None:
        self.machine = machine

    def __call__(self, *args: str, check: bool = True) -> subprocess.CompletedProcess:
        """Run: btrepl <args>"""
        return subprocess.run(
            ["orbctl", "run", "-m", self.machine, "-u", "root", "btrepl"] + list(args),
            capture_output=True, text=True, check=check,
        )

    def shell(self, cmd: str, *, check: bool = True) -> subprocess.CompletedProcess:
        """Run an arbitrary shell command."""
        return rem(self.machine, cmd, check=check)


@pytest.fixture(scope="session")
def master_cli() -> NodeCLI:
    return NodeCLI(MASTER)


@pytest.fixture(scope="session")
def slave_cli() -> list[NodeCLI]:
    return [NodeCLI(s) for s in SLAVES]
