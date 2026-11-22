"""
Cluster lifecycle and shared fixtures for btrepl integration tests.

Topology: btrepl1 (master) + btrepl2/3/4 (slaves), Incus instances.
Everything goes through the incus client: `incus exec` runs commands,
`incus file push` copies files, and gRPC reaches the daemon's root-only Unix
socket through a local port bridged with `incus exec ... socat`. The host
needs no SSH or network access to the instances, so a remote Incus server
works the same as a local one.

Pre-requisites:
  - an Incus server the `incus` client can reach (local, `incus remote`, or
    Colima with `--runtime incus`)
  - a .deb for the instances' architecture: make deb

Settings (environment variables):
  BTREPL_INCUS_REMOTE  Incus remote to use (default: the current one)
  BTREPL_INCUS_TYPE    vm (default) or container. Containers are privileged
                       and get the host's loop devices; use them where there
                       is no KVM, e.g. Colima on Apple M1/M2.
  BTREPL_INCUS_IMAGE   image for the cluster (default: images:debian/13)
  BTREPL_TEST_DEB      .deb to install (default: newest build/*_<arch>.deb)

Quick start:
    uv run pytest tests/integration/ -v --spin-up               # spin up + test
    uv run pytest tests/integration/ -v --spin-up --teardown    # spin up + test + destroy
    uv run pytest tests/integration/ -v                         # test only (instances already up)
    uv run pytest tests/integration/ -v --destructive           # also run destructive tests
"""

import os
import select
import socket
import subprocess
import threading
import time
from collections.abc import Generator, Iterator
from concurrent.futures import ThreadPoolExecutor
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

INCUS_REMOTE = os.environ.get("BTREPL_INCUS_REMOTE", "")
INCUS_TYPE = os.environ.get("BTREPL_INCUS_TYPE", "vm")
INCUS_IMAGE = os.environ.get("BTREPL_INCUS_IMAGE", "images:debian/13")

BTRFS_IMG   = "/btrfs.img"
BTRFS_MNT   = "/btrfs"
SSH_KEY     = "/root/.ssh/id_ed25519"
SUBVOLS     = ["@eventsPictures", "@reports"]
SNAP_DIR    = ".snapshots"
SNAP_PREFIX = "btrepl_"
GRPC_SOCKET = "/run/btrepl/btrepl.sock"
# Packages every node needs besides btrepl. pv slows down receives in
# test_failures.py; socat bridges the gRPC socket to the host.
NODE_PACKAGES = "btrfs-progs openssh-server openssh-client socat nftables pv openssl iproute2 procps"

REPO_ROOT = Path(__file__).parent.parent.parent

if INCUS_TYPE not in ("vm", "container"):
    raise ValueError(f"BTREPL_INCUS_TYPE must be vm or container, not {INCUS_TYPE!r}")

# ── pytest options / markers ──────────────────────────────────────────────────


def pytest_addoption(parser: pytest.Parser) -> None:
    parser.addoption(
        "--spin-up", action="store_true", default=False,
        help="Create/start the 4-node Incus cluster before tests.",
    )
    parser.addoption(
        "--teardown", action="store_true", default=False,
        help="Delete all cluster instances after tests.",
    )
    parser.addoption(
        "--destructive", action="store_true", default=False,
        help="Also run destructive tests (clear, standalone). Off by default.",
    )
    parser.addoption(
        "--distros", action="store_true", default=False,
        help="Run the package install tests on fresh instances of several distros.",
    )
    parser.addoption(
        "--channels", action="store_true", default=False,
        help="Run the tests of the published install.sh, APT and RPM repositories.",
    )


def pytest_configure(config: pytest.Config) -> None:
    config.addinivalue_line(
        "markers",
        "destructive: modifies cluster state irreversibly (skipped unless --destructive)",
    )
    config.addinivalue_line("markers", "distros: package install tests (skipped unless --distros)")
    config.addinivalue_line("markers", "channels: published install channels (skipped unless --channels)")


def pytest_collection_modifyitems(
    config: pytest.Config, items: list[pytest.Item]
) -> None:
    for marker, option in (("destructive", "--destructive"), ("distros", "--distros"),
                           ("channels", "--channels")):
        if not config.getoption(option):
            skip = pytest.mark.skip(reason=f"pass {option} to run")
            for item in items:
                if item.get_closest_marker(marker):
                    item.add_marker(skip)
    # Destructive tests detach slaves and delete snapshots: run them after
    # everything else, in their original order.
    items.sort(key=lambda item: item.get_closest_marker("destructive") is not None)


# ── remote execution via incus ────────────────────────────────────────────────


def ref(machine: str) -> str:
    """Instance name as the incus client expects it, with the remote if set."""
    return f"{INCUS_REMOTE}:{machine}" if INCUS_REMOTE else machine


def incus(*args: str, check: bool = True, timeout: float | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(["incus", *args], capture_output=True, text=True, check=check, timeout=timeout)


def rem(machine: str, cmd: str, *, check: bool = True) -> subprocess.CompletedProcess:
    """Run a bash command on an instance as root."""
    r = subprocess.run(
        ["incus", "exec", ref(machine), "--", "bash", "-c", cmd],
        capture_output=True, text=True,
    )
    if check and r.returncode != 0:
        raise subprocess.CalledProcessError(r.returncode, f"[{machine}] {cmd}", r.stdout, r.stderr)
    return r


def push(machine: str, local: Path | str, remote: str) -> None:
    incus("file", "push", str(local), f"{ref(machine)}{remote}")


def machine_exists(name: str) -> bool:
    return incus("info", ref(name), check=False).returncode == 0


def wait_ready(machine: str, timeout: float = 240.0) -> None:
    """Wait until systemd has booted the instance and it has an IPv4 address."""
    print(f"  [{machine}] waiting for boot ...", flush=True)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = rem(machine, "systemctl is-system-running --wait; hostname -I", check=False)
        lines = r.stdout.split()
        if r.returncode == 0 and len(lines) >= 2 and lines[0] in ("running", "degraded"):
            return
        time.sleep(2)
    raise TimeoutError(f"{machine} not ready after {timeout:.0f} s")



def get_ip(machine: str) -> str:
    return rem(machine, "hostname -I | awk '{print $1}'").stdout.strip()


def restart(machine: str) -> None:
    """Hard-restart an instance, as a power cut would, and wait for it."""
    incus("restart", "--force", ref(machine))
    wait_ready(machine)


def launch(machine: str, image: str = INCUS_IMAGE) -> None:
    """Create and start an instance; containers get the host's loop devices."""
    args = ["launch", image, ref(machine)]
    if INCUS_TYPE == "vm":
        args += ["--vm", "-c", "limits.cpu=2", "-c", "limits.memory=1536MiB",
                 "-c", "security.secureboot=false"]
    else:
        # A btrfs image file is mounted with a loop device, which needs a
        # privileged container and the host's /dev/loop*.
        args += ["-c", "security.privileged=true", "-c", "security.nesting=true"]
    incus(*args)
    if INCUS_TYPE == "container":
        incus("config", "device", "add", ref(machine), "loop-control",
              "unix-char", "path=/dev/loop-control", "required=false")
        for i in range(32):
            incus("config", "device", "add", ref(machine), f"loop{i}",
                  "unix-block", f"path=/dev/loop{i}", "required=false")


# ── gRPC bridge ───────────────────────────────────────────────────────────────


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _pump(conn: socket.socket, proc: subprocess.Popen) -> None:
    """Copy bytes both ways between a local connection and socat's stdio."""
    assert proc.stdin is not None and proc.stdout is not None
    out_fd = proc.stdout.fileno()
    try:
        while True:
            ready, _, _ = select.select([conn, out_fd], [], [], 1.0)
            if proc.poll() is not None and not ready:
                return
            if conn in ready:
                data = conn.recv(65536)
                if not data:
                    proc.stdin.close()
                    return
                proc.stdin.write(data)
                proc.stdin.flush()
            if out_fd in ready:
                data = os.read(out_fd, 65536)
                if not data:
                    return
                conn.sendall(data)
    except (OSError, ValueError):
        return
    finally:
        conn.close()
        proc.kill()
        proc.wait()


@contextmanager
def tunnel(machine: str, remote: str | int = GRPC_SOCKET) -> Iterator[int]:
    """
    Listen on a free local port and connect each connection to the daemon's
    Unix socket on machine, or to a TCP port on the machine's loopback when
    remote is a port number, through `incus exec ... socat`.
    Yields the local port number.
    """
    target = f"TCP:127.0.0.1:{remote}" if isinstance(remote, int) else f"UNIX-CONNECT:{remote}"
    port = _free_port()
    server = socket.create_server(("127.0.0.1", port))
    server.settimeout(0.5)
    stop = threading.Event()

    def serve() -> None:
        while not stop.is_set():
            try:
                conn, _ = server.accept()
            except TimeoutError:
                continue
            except OSError:
                return
            proc = subprocess.Popen(
                ["incus", "exec", "--force-noninteractive", ref(machine), "--",
                 "socat", "-", target],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                bufsize=0,
            )
            threading.Thread(target=_pump, args=(conn, proc), daemon=True).start()

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    try:
        yield port
    finally:
        stop.set()
        thread.join(timeout=5)
        server.close()



# ── cluster setup / teardown ──────────────────────────────────────────────────


def _install_node(machine: str, deb: Path) -> None:
    """Install btrfs tools + btrepl package on a single node."""
    rem(machine, f"""
        export DEBIAN_FRONTEND=noninteractive
        if ! dpkg -s {NODE_PACKAGES} >/dev/null 2>&1; then
            apt-get update -qq
            apt-get install -y -qq {NODE_PACKAGES}
        fi
        systemctl enable --now ssh
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


def find_deb(arch: str) -> Path:
    if os.environ.get("BTREPL_TEST_DEB"):
        return Path(os.environ["BTREPL_TEST_DEB"])
    debs = sorted(REPO_ROOT.glob(f"build/*_{arch}.deb"), key=lambda p: p.stat().st_mtime)
    if not debs:
        raise FileNotFoundError(f"No {arch} .deb in build/ — run: make deb")
    return debs[-1]


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
    # 1. create missing instances, start all and wait for boot
    print(f"\n[btrepl] incus {INCUS_TYPE}s from {INCUS_IMAGE}", flush=True)
    for m in ALL_NODES:
        if not machine_exists(m):
            print(f"[btrepl] launching {m} ...", flush=True)
            launch(m)
        else:
            incus("start", ref(m), check=False)
    with ThreadPoolExecutor(len(ALL_NODES)) as pool:
        list(pool.map(wait_ready, ALL_NODES))

    arch = rem(MASTER, "dpkg --print-architecture").stdout.strip()
    deb = find_deb(arch)
    print(f"[btrepl] package: {deb.name}", flush=True)

    # 2. install btrfs + btrepl, write config on every node
    def provision(m: str) -> None:
        print(f"[btrepl] provisioning {m} ...", flush=True)
        _install_node(m, deb)
        _write_config(m)

    with ThreadPoolExecutor(len(ALL_NODES)) as pool:
        list(pool.map(provision, ALL_NODES))

    # 3. generate SSH key on master, authorize it on every slave
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

    # 4. init master, add all three slaves
    rem(MASTER, "btrepl init-master")
    for slave in SLAVES:
        slave_ip = get_ip(slave)
        rem(MASTER, f"btrepl add-slave -s {slave_ip}")

    # 5. start btrepl daemon on every node
    for m in ALL_NODES:
        rem(m, "systemctl daemon-reload && systemctl enable btrepl.service && systemctl start btrepl.service")
        rem(m, "systemctl is-active btrepl.service")

    print("[btrepl] cluster ready", flush=True)


def _teardown_cluster() -> None:
    print("\n[btrepl] tearing down cluster ...", flush=True)
    for m in ALL_NODES:
        if machine_exists(m):
            incus("delete", "--force", ref(m), check=False)


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
    """Long-lived BtreplClient for btrepl1, connected via a bridge."""
    with tunnel(MASTER) as port:
        with BtreplClient(f"127.0.0.1:{port}", timeout=15.0) as c:
            yield c


@pytest.fixture(scope="session")
def slave_clients() -> Generator[list[BtreplClient], None, None]:
    """One BtreplClient per slave, each via its own SSH tunnel."""
    with ExitStack() as stack:
        clients: list[BtreplClient] = []
        for slave in SLAVES:
            port = stack.enter_context(tunnel(slave))
            client = BtreplClient(f"127.0.0.1:{port}", timeout=15.0)
            stack.enter_context(client)
            clients.append(client)
        yield clients


@pytest.fixture(scope="session")
def grpc_client_factory() -> Iterator[object]:
    """
    Factory for creating short-lived gRPC clients via fresh bridges.
    Use for WatchLogs tests or any test that needs an independent channel.

    Usage:
        with grpc_client_factory(MASTER) as client:
            for entry in client.watch_logs(): ...
    """
    @contextmanager
    def _make(machine: str = MASTER, timeout: float = 30.0) -> Iterator[BtreplClient]:
        with tunnel(machine) as port:
            with BtreplClient(f"127.0.0.1:{port}", timeout=timeout) as c:
                yield c

    yield _make


# ── CLI helper ────────────────────────────────────────────────────────────────


class NodeCLI:
    """Runs btrepl CLI commands on an instance via incus exec."""

    def __init__(self, machine: str) -> None:
        self.machine = machine

    def __call__(self, *args: str, check: bool = True) -> subprocess.CompletedProcess:
        """Run: btrepl <args>"""
        return subprocess.run(
            ["incus", "exec", ref(self.machine), "--", "btrepl"] + list(args),
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
