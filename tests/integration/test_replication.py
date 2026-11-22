"""
Replication engine tests: snapshot once per cycle, match parents by UUID,
recover from interrupted receives, keep the anchor of an offline slave.

Cycles are run with the `btrepl run` CLI on the master. The master daemon is
stopped for this module so that no daemon cycle runs in between.
"""

import re

import pytest

from conftest import MASTER, SLAVES, SSH_KEY, SUBVOLS, rem
from helpers import CONFIG, run_cycle, sends, snaps


@pytest.fixture(scope="module", autouse=True)
def master_daemon_stopped():
    rem(MASTER, "systemctl stop btrepl.service")
    yield
    rem(MASTER, "systemctl start btrepl.service")


class TestSnapshotPerCycle:
    def test_one_snapshot_per_subvolume(self, master_cli):
        """Three slaves, one cycle: one new snapshot per subvolume, not three."""
        before = {sv: {s.name for s in snaps(MASTER, sv)} for sv in SUBVOLS}
        run_cycle(master_cli)
        for sv in SUBVOLS:
            new = {s.name for s in snaps(MASTER, sv)} - before[sv]
            assert len(new) == 1, f"{sv}: new snapshots {sorted(new)}"

    def test_all_slaves_receive_the_same_snapshot(self, master_cli):
        run_cycle(master_cli)
        for sv in SUBVOLS:
            newest = snaps(MASTER, sv)[-1]
            for slave in SLAVES:
                remote = {s.name: s for s in snaps(slave, sv)}
                assert newest.name in remote, f"{slave} lacks {newest.name}"
                assert remote[newest.name].received
                assert remote[newest.name].received_uuid == newest.uuid


class TestInterruptedReceive:
    def test_partial_snapshot_is_replaced_and_send_stays_incremental(self, master_cli, slave_ips):
        """
        An interrupted receive leaves a writable subvolume without received_uuid
        under the name of the master's snapshot. It must not be used as parent.
        """
        slave, ip, sv = SLAVES[0], slave_ips[0], SUBVOLS[0]
        run_cycle(master_cli)
        complete = snaps(slave, sv)
        assert len(complete) >= 2, "need an older snapshot to fall back to"
        last = complete[-1]
        rem(slave, f"btrfs subvolume delete {last.path} && btrfs subvolume create {last.path}")

        log = run_cycle(master_cli)

        remote = {s.name: s for s in snaps(slave, sv)}
        assert last.name not in remote, "partial snapshot was not deleted"
        newest = snaps(MASTER, sv)[-1]
        assert remote[newest.name].received
        assert sends(log, ip)[newest.name] == complete[-2].name


class TestOfflineSlaveAnchor:
    def test_last_common_snapshot_survives_pruning(self, master_cli, slave_ips):
        """
        While a slave is unreachable for more cycles than keep_sender covers,
        the master keeps the last snapshot it has in common with that slave,
        so the next send is still incremental.
        """
        slave, ip = SLAVES[-1], slave_ips[-1]
        pubkey = rem(MASTER, f"cat {SSH_KEY}.pub").stdout.strip()
        keep = re.search(r"^keep_sender: (\d+)$", rem(MASTER, f"cat {CONFIG}").stdout, re.M)[1]
        rem(MASTER, f"sed -i 's/^keep_sender: .*/keep_sender: 2/' {CONFIG}")
        try:
            run_cycle(master_cli)
            anchors = {sv: snaps(MASTER, sv)[-1].name for sv in SUBVOLS}

            rem(slave, f"grep -vF '{pubkey}' /root/.ssh/authorized_keys > /root/.ssh/ak.tmp; "
                       "cat /root/.ssh/ak.tmp > /root/.ssh/authorized_keys; rm /root/.ssh/ak.tmp")
            for _ in range(3):
                log = run_cycle(master_cli, expect_ok=False)
                assert "replication failed" in log and ip in log

            for sv in SUBVOLS:
                names = [s.name for s in snaps(MASTER, sv)]
                assert anchors[sv] in names, f"{sv}: anchor for {slave} was pruned"
                assert len(names) == 3, f"{sv}: expected keep_sender 2 + anchor, got {names}"

            rem(slave, f"echo '{pubkey}' >> /root/.ssh/authorized_keys")
            log = run_cycle(master_cli)
            for sv in SUBVOLS:
                newest = snaps(MASTER, sv)[-1].name
                assert sends(log, ip)[newest] == anchors[sv]
        finally:
            rem(slave, f"grep -qF '{pubkey}' /root/.ssh/authorized_keys || "
                       f"echo '{pubkey}' >> /root/.ssh/authorized_keys")
            rem(MASTER, f"sed -i 's/^keep_sender: .*/keep_sender: {keep}/' {CONFIG}")
