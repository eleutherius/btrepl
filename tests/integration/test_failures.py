"""
Failure tests: the connection drops in the middle of a send, a slave loses
power while it receives, a slave's disk fills up, a slave is switched off.
In every case the other slaves are still replicated, the master keeps its
last common snapshot with the failed slave, and once the slave is back the
next cycle cleans up and sends incrementally from that snapshot.

Receives on the slave under test are slowed down with pv, through a btrfs
wrapper in /usr/local/sbin, so that a send is still running when the failure
is injected.

Cycles are run with the `btrepl run` CLI on the master. The master daemon is
stopped for this module so that no daemon cycle runs in between.
"""

import time
from concurrent.futures import Future, ThreadPoolExecutor

import pytest

from conftest import BTRFS_MNT, MASTER, SLAVES, get_ip, incus, ref, rem, restart, wait_ready
from helpers import CONFIG, Snap, manifest, run_cycle, sends, snaps

SUBVOL = "@eventsPictures"
WRAPPER = "/usr/local/sbin/btrfs"
NFT_TABLE = "btrepl_test"


@pytest.fixture(scope="module", autouse=True)
def master_daemon_stopped():
    rem(MASTER, "systemctl stop btrepl.service")
    yield
    rem(MASTER, "systemctl start btrepl.service")


@pytest.fixture(scope="module", autouse=True)
def short_timeouts():
    rem(MASTER, f"cp {CONFIG} {CONFIG}.failures.bak && "
                f"sed -i '/^ssh_command_timeout:/d; /^send_idle_timeout:/d' {CONFIG} && "
                f"printf 'ssh_command_timeout: 10s\\nsend_idle_timeout: 5s\\n' >> {CONFIG}")
    yield
    rem(MASTER, f"mv {CONFIG}.failures.bak {CONFIG}")


@pytest.fixture
def slow_receive():
    """Make btrfs receive on a slave read at most rate bytes per second."""
    slaves = []

    def install(slave: str, rate: str = "2m") -> None:
        rem(slave, f"""cat > {WRAPPER} <<'EOF2'
#!/bin/sh
case "$1" in
  receive) pv -q -L {rate} | /usr/bin/btrfs "$@"; exit ;;
esac
exec /usr/bin/btrfs "$@"
EOF2
chmod 755 {WRAPPER}""")
        slaves.append(slave)

    yield install
    for slave in slaves:
        rem(slave, f"rm -f {WRAPPER}", check=False)


@pytest.fixture
def blocked():
    """Drop every packet from the master on a slave."""
    slaves = []

    def block(slave: str) -> None:
        rem(slave, f"""
            nft add table inet {NFT_TABLE}
            nft add chain inet {NFT_TABLE} input '{{ type filter hook input priority 0; }}'
            nft add rule inet {NFT_TABLE} input ip saddr {get_ip(MASTER)} drop
        """)
        slaves.append(slave)

    yield block
    for slave in slaves:
        rem(slave, f"nft delete table inet {NFT_TABLE}", check=False)


def write_data(mib: int) -> None:
    rem(MASTER, f"dd if=/dev/urandom of={BTRFS_MNT}/{SUBVOL}/failure_$(date +%s%N).bin "
                f"bs=1M count={mib} status=none && sync")


def start_cycle(master_cli) -> Future:
    pool = ThreadPoolExecutor(1)
    future = pool.submit(run_cycle, master_cli, expect_ok=False)
    pool.shutdown(wait=False)
    return future


def wait_partial(slave: str, timeout: float = 60.0) -> Snap:
    """Wait until the slave is receiving: a snapshot that is not complete yet."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        partial = [s for s in snaps(slave, SUBVOL) if not s.received]
        if partial:
            return partial[-1]
        time.sleep(0.5)
    raise TimeoutError(f"{slave} did not start receiving within {timeout:.0f} s")


def in_sync(master_cli) -> str:
    """Run a cycle so that every slave has the newest snapshot; return its name."""
    run_cycle(master_cli)
    return snaps(MASTER, SUBVOL)[-1].name


def assert_recovered(log: str, slave: str, ip: str, anchor: str) -> None:
    """After a failure: no leftovers, an incremental send from the anchor, same data."""
    newest = snaps(MASTER, SUBVOL)[-1]
    remote = snaps(slave, SUBVOL)
    assert all(s.received for s in remote), f"{slave} kept an incomplete snapshot: {remote}"
    by_name = {s.name: s for s in remote}
    assert newest.name in by_name, f"{slave} lacks {newest.name}"
    assert sends(log, ip)[newest.name] == anchor, f"{slave}: the send was not incremental from {anchor}"
    assert manifest(slave, by_name[newest.name].path) == manifest(MASTER, newest.path)


def assert_others_replicated(log: str, slave_ips: list[str], failed_ip: str) -> None:
    for ip in slave_ips:
        if ip != failed_ip:
            assert f'msg="replication ok" slave={ip}' in log, f"{ip} was not replicated"


class TestConnectionLost:
    @pytest.mark.xfail(strict=True, reason=(
        "known bug: after send_idle_timeout aborts a stalled send, the replicator opens the "
        "next subvolume's session on the same dead SSH connection, which hangs until the "
        "keepalives give up (~2 min) and fails with 'unexpected packet in response to channel open'"))
    def test_packets_dropped_mid_send(self, master_cli, slave_ips, slow_receive, blocked):
        slave, ip = SLAVES[0], slave_ips[0]
        anchor = in_sync(master_cli)
        write_data(32)
        slow_receive(slave)

        job = start_cycle(master_cli)
        wait_partial(slave)
        blocked(slave)
        start = time.monotonic()
        log = job.result(timeout=180)

        assert f'msg="replication failed" slave={ip}' in log
        elapsed = time.monotonic() - start
        assert elapsed < 90, f"the stalled send was aborted after {elapsed:.0f} s:\n{log[-3000:]}"
        assert_others_replicated(log, slave_ips, ip)
        assert anchor in {s.name for s in snaps(MASTER, SUBVOL)}

        rem(slave, f"nft delete table inet {NFT_TABLE}; rm -f {WRAPPER}")
        log = run_cycle(master_cli)
        assert_recovered(log, slave, ip, anchor)


class TestSlavePowerLoss:
    def test_slave_restarted_mid_receive(self, master_cli, slave_ips, slow_receive):
        slave, ip = SLAVES[0], slave_ips[0]
        anchor = in_sync(master_cli)
        write_data(32)
        slow_receive(slave)

        job = start_cycle(master_cli)
        wait_partial(slave)
        restart(slave)
        log = job.result(timeout=180)

        assert f'msg="replication failed" slave={ip}' in log
        assert_others_replicated(log, slave_ips, ip)
        assert get_ip(slave) == ip, "the slave came back with another address"
        assert rem(slave, f"mountpoint -q {BTRFS_MNT}", check=False).returncode == 0, \
            "btrfs is not mounted after the restart"

        rem(slave, f"rm -f {WRAPPER}")
        log = run_cycle(master_cli)
        assert_recovered(log, slave, ip, anchor)


class TestSlaveDiskFull:
    def test_receive_fails_then_recovers(self, master_cli, slave_ips):
        slave, ip = SLAVES[0], slave_ips[0]
        anchor = in_sync(master_cli)
        # Leave about 16 MiB free on the slave, then send more than that.
        rem(slave, f"""
            free=$(btrfs filesystem usage -b {BTRFS_MNT} | awk '/Free \\(estimated\\)/ {{print $3}}')
            size=$((free - 16 * 1024 * 1024))
            while [ "$size" -gt 0 ] && ! fallocate -l "$size" {BTRFS_MNT}/filler 2>/dev/null; do
                size=$((size - 32 * 1024 * 1024))
            done
            sync
        """)
        try:
            write_data(48)
            log = run_cycle(master_cli, expect_ok=False)
            assert f'msg="replication failed" slave={ip}' in log
            assert_others_replicated(log, slave_ips, ip)
            assert anchor in {s.name for s in snaps(MASTER, SUBVOL)}
        finally:
            rem(slave, f"rm -f {BTRFS_MNT}/filler && btrfs filesystem sync {BTRFS_MNT}")

        log = run_cycle(master_cli)
        assert_recovered(log, slave, ip, anchor)


class TestSlaveOff:
    def test_switched_off_slave_does_not_block_the_others(self, master_cli, slave_ips):
        slave, ip = SLAVES[1], slave_ips[1]
        anchor = in_sync(master_cli)
        incus("stop", "--force", ref(slave))
        try:
            write_data(4)
            start = time.monotonic()
            log = run_cycle(master_cli, expect_ok=False)
            assert time.monotonic() - start < 120
            assert f'msg="replication failed" slave={ip}' in log
            assert_others_replicated(log, slave_ips, ip)
        finally:
            incus("start", ref(slave), check=False)
            wait_ready(slave)

        log = run_cycle(master_cli)
        assert_recovered(log, slave, ip, anchor)
