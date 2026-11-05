"""
Slave access tests: add-slave restricts the master's key, btrfs receive runs
in a chroot, a stuck slave times out, snapshot directories are root-only.

Cycles are run with the `btrepl run` CLI on the master. The master daemon is
stopped for this module so that its cycles do not hold the run lock.
"""

import time

import pytest

from conftest import BTRFS_MNT, MASTER, SLAVES, SNAP_DIR, SSH_KEY, get_ip, rem

CONFIG = "/etc/btrepl/config.yaml"
AUTHORIZED_KEYS = "/root/.ssh/authorized_keys"
FAKE_BTRFS = "/usr/local/sbin/btrfs"


def run_cycle(master_cli, *, expect_ok: bool = True) -> str:
    r = master_cli("run", check=False)
    out = r.stdout + r.stderr
    if expect_ok:
        assert r.returncode == 0, f"btrepl run failed:\n{out}"
    return out


@pytest.fixture(scope="module", autouse=True)
def master_daemon_stopped():
    rem(MASTER, "systemctl stop btrepl.service")
    yield
    rem(MASTER, "systemctl start btrepl.service")


@pytest.fixture(scope="module")
def pubkey() -> str:
    return rem(MASTER, f"cat {SSH_KEY}.pub").stdout.strip()


def key_lines(slave: str, pubkey: str) -> list[str]:
    blob = pubkey.split()[1]
    return [l for l in rem(slave, f"cat {AUTHORIZED_KEYS}").stdout.splitlines() if blob in l]


def unrestrict(slave: str, pubkey: str) -> None:
    blob = pubkey.split()[1]
    rem(slave, f"grep -vF '{blob}' {AUTHORIZED_KEYS} > /root/ak.tmp; "
               f"cat /root/ak.tmp > {AUTHORIZED_KEYS}; rm /root/ak.tmp; "
               f"echo '{pubkey}' >> {AUTHORIZED_KEYS}")


def ssh_from_master(ip: str, cmd: str):
    return rem(MASTER, f"ssh -o BatchMode=yes -i {SSH_KEY} root@{ip} {cmd}", check=False)


class TestRestrictedKey:
    def test_add_slave_restricts_the_key(self, master_cli, slave_ips, pubkey):
        slave, ip = SLAVES[0], slave_ips[0]
        unrestrict(slave, pubkey)

        r = master_cli("add-slave", "-s", ip)
        assert "restricted the master's key" in r.stdout + r.stderr

        master_ip = get_ip(MASTER)
        lines = key_lines(slave, pubkey)
        assert lines == [f'restrict,from="{master_ip}",command="/usr/local/bin/btrepl ssh-filter" {pubkey}']
        r = ssh_from_master(ip, "id")
        assert r.returncode != 0 and "command not allowed" in r.stderr
        run_cycle(master_cli)

    def test_add_slave_again_keeps_restriction(self, master_cli, slave_ips, pubkey):
        slave, ip = SLAVES[0], slave_ips[0]
        r = master_cli("add-slave", "-s", ip)
        assert "already restricted" in r.stdout + r.stderr
        assert key_lines(slave, pubkey)[0].startswith("restrict,")

    def test_no_restrict(self, master_cli, slave_ips, pubkey):
        slave, ip = SLAVES[0], slave_ips[0]
        unrestrict(slave, pubkey)
        master_cli("add-slave", "-s", ip, "--no-restrict")
        assert key_lines(slave, pubkey) == [pubkey]

    def test_failed_check_restores_authorized_keys(self, master_cli, slave_ips, pubkey):
        """ssh-filter on the slave reads another btrfs_root, so the check fails."""
        slave, ip = SLAVES[0], slave_ips[0]
        unrestrict(slave, pubkey)
        before = rem(slave, f"cat {AUTHORIZED_KEYS}").stdout
        rem(slave, f"cp {CONFIG} {CONFIG}.bak && sed -i 's|^btrfs_root: .*|btrfs_root: /elsewhere|' {CONFIG}")
        try:
            r = master_cli("add-slave", "-s", ip)
            assert "could not restrict" in r.stdout + r.stderr
            assert rem(slave, f"cat {AUTHORIZED_KEYS}").stdout == before
        finally:
            rem(slave, f"mv {CONFIG}.bak {CONFIG}")
        master_cli("add-slave", "-s", ip)  # restrict it for the other tests

    def test_slave_without_btrepl(self, master_cli, slave_ips, pubkey):
        slave, ip = SLAVES[0], slave_ips[0]
        unrestrict(slave, pubkey)
        rem(slave, "mv /usr/local/bin/btrepl /usr/local/bin/btrepl.off")
        try:
            r = master_cli("add-slave", "-s", ip)
            assert "btrepl is not installed on the slave" in r.stdout + r.stderr
            assert key_lines(slave, pubkey) == [pubkey]
        finally:
            rem(slave, "mv /usr/local/bin/btrepl.off /usr/local/bin/btrepl")
        master_cli("add-slave", "-s", ip)


class TestReceiveChroot:
    def test_receive_runs_in_chroot(self, master_cli):
        log = run_cycle(master_cli)
        assert f"Chroot to {BTRFS_MNT}/{SNAP_DIR}" in log


class TestSnapshotDirMode:
    def test_snapshot_dir_is_root_only(self, master_cli):
        for node in [MASTER] + SLAVES:
            rem(node, f"chmod 0755 {BTRFS_MNT}/{SNAP_DIR}")
        run_cycle(master_cli)
        for node in [MASTER] + SLAVES:
            assert rem(node, f"stat -c %a {BTRFS_MNT}/{SNAP_DIR}").stdout.strip() == "700", node


@pytest.fixture
def short_timeouts():
    rem(MASTER, f"cp {CONFIG} {CONFIG}.bak && "
                f"sed -i '/^ssh_command_timeout:/d; /^send_idle_timeout:/d' {CONFIG} && "
                f"printf 'ssh_command_timeout: 3s\\nsend_idle_timeout: 5s\\n' >> {CONFIG}")
    yield
    rem(MASTER, f"mv {CONFIG}.bak {CONFIG}")


@pytest.fixture
def fake_btrfs():
    """Install a btrfs wrapper on a slave that hangs for the given command."""
    slaves = []

    def install(slave: str, hang_on: str) -> None:
        rem(slave, f"""cat > {FAKE_BTRFS} <<'EOF'
#!/bin/sh
case "$*" in
  "{hang_on}"*) exec sleep 120 ;;
esac
exec /usr/bin/btrfs "$@"
EOF
chmod 755 {FAKE_BTRFS}""")
        slaves.append(slave)

    yield install
    for slave in slaves:
        rem(slave, f"rm -f {FAKE_BTRFS}; pkill -f 'sleep 120' || true", check=False)


class TestTimeouts:
    def test_hanging_command_times_out(self, master_cli, slave_ips, short_timeouts, fake_btrfs):
        slave, ip = SLAVES[1], slave_ips[1]
        fake_btrfs(slave, "subvolume list")
        start = time.monotonic()
        log = run_cycle(master_cli, expect_ok=False)
        assert time.monotonic() - start < 60
        assert "no result after 3s" in log
        # The other slaves were still replicated.
        for other in (slave_ips[0], slave_ips[2]):
            assert f'msg="replication ok" slave={other}' in log

    def test_stalled_send_is_aborted(self, master_cli, slave_ips, short_timeouts, fake_btrfs):
        slave, ip = SLAVES[1], slave_ips[1]
        fake_btrfs(slave, "receive")
        start = time.monotonic()
        log = run_cycle(master_cli, expect_ok=False)
        assert time.monotonic() - start < 90
        assert "made no progress for 5s" in log
        for other in (slave_ips[0], slave_ips[2]):
            assert f'msg="replication ok" slave={other}' in log
