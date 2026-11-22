"""
Data integrity tests: what arrives on the slaves is byte for byte what the
master snapshotted, after a full send, after incremental sends with every
kind of change, and after a slave is promoted with standalone.

The trees are compared with helpers.manifest: type, mode, owner, link count,
symlink target, size, mtime and sha256 of every entry.

Cycles are run with the `btrepl run` CLI on the master. The master daemon is
stopped for this module so that no daemon cycle runs in between.
"""

import pytest

from conftest import BTRFS_MNT, MASTER, SLAVES, rem
from helpers import manifest, run_cycle, sends, snaps

SUBVOL = "@reports"
LIVE = f"{BTRFS_MNT}/{SUBVOL}"
DATA = f"{LIVE}/integrity"


@pytest.fixture(scope="module", autouse=True)
def master_daemon_stopped():
    rem(MASTER, "systemctl stop btrepl.service")
    yield
    rem(MASTER, "systemctl start btrepl.service")


def assert_slaves_match_master() -> str:
    """Compare the master's newest snapshot with the same snapshot on every slave."""
    newest = snaps(MASTER, SUBVOL)[-1]
    expected = manifest(MASTER, newest.path)
    assert "integrity" in expected, "the test data is not in the snapshot"
    for slave in SLAVES:
        remote = {s.name: s for s in snaps(slave, SUBVOL)}
        assert newest.name in remote, f"{slave} lacks {newest.name}"
        assert remote[newest.name].received, f"{slave}: {newest.name} is incomplete"
        assert manifest(slave, remote[newest.name].path) == expected, f"{slave}: {newest.name} differs"
    return newest.name


def change(cmd: str) -> None:
    rem(MASTER, f"set -e\ncd {DATA}\n{cmd}\nsync")


class TestIntegrity:
    def test_full_tree(self, master_cli):
        """A new tree with every kind of entry, sent to slaves that hold none of it."""
        rem(MASTER, f"""
            set -e
            rm -rf {DATA}
            mkdir -p {DATA}/docs/nested/deeper "{DATA}/з пробілами"
            cd {DATA}
            dd if=/dev/urandom of=big.bin bs=1M count=48 status=none
            for i in $(seq 1 60); do head -c $((RANDOM * 3)) /dev/urandom > docs/small_$i; done
            truncate -s 64M sparse.img
            dd if=/dev/urandom of=sparse.img bs=1M count=2 seek=31 conv=notrunc status=none
            : > empty
            echo "звіт" > "з пробілами/звіт 2026.txt"
            ln -s docs/small_1 link_rel
            ln -s /etc/hostname link_abs
            ln big.bin big.hardlink
            mkfifo pipe
            chmod 600 docs/small_2
            chown nobody:nogroup docs/small_3
            chmod 4755 docs/small_4
            touch -d '2001-02-03 04:05:06' docs/nested/deeper/old
            sync
        """)
        run_cycle(master_cli)
        assert_slaves_match_master()

    def test_incremental_changes(self, master_cli, slave_ips):
        """Appends, overwrites, deletes, renames, type changes and metadata changes."""
        before = snaps(MASTER, SUBVOL)[-1].name
        change("""
            head -c 3M /dev/urandom >> big.bin
            dd if=/dev/urandom of=sparse.img bs=4k count=8 seek=100 conv=notrunc status=none
            truncate -s 100 docs/small_5
            rm docs/small_6 docs/small_7 link_abs
            mv docs/nested renamed_dir
            rm docs/small_8 && ln -s ../empty docs/small_8
            rm link_rel && mkdir link_rel && echo x > link_rel/inside
            chmod 640 docs/small_9
            chown nobody docs/small_10
            touch -d '2020-01-01' docs/small_11
            rm big.hardlink
            head -c 1M /dev/urandom > docs/new_file
        """)
        log = run_cycle(master_cli)
        newest = assert_slaves_match_master()
        for ip in slave_ips:
            assert sends(log, ip)[newest] == before, f"{ip}: the send was not incremental"

    def test_many_cycles(self, master_cli):
        """Several small changes in a row; older snapshots stay correct too."""
        names = []
        for i in range(4):
            change(f"head -c 200K /dev/urandom > cycle_{i}; rm -f cycle_{i - 2}")
            run_cycle(master_cli)
            names.append(assert_slaves_match_master())
        previous = snaps(MASTER, SUBVOL)
        older = next(s for s in previous if s.name == names[1])
        for slave in SLAVES:
            remote = {s.name: s for s in snaps(slave, SUBVOL)}
            assert manifest(slave, remote[older.name].path) == manifest(MASTER, older.path)

    def test_emptied_tree(self, master_cli):
        change("rm -rf ./* ./.[!.]* 2>/dev/null || true")
        run_cycle(master_cli)
        newest = snaps(MASTER, SUBVOL)[-1]
        assert "./integrity/" not in manifest(MASTER, newest.path)
        assert_slaves_match_master()


@pytest.mark.destructive
class TestStandaloneIntegrity:
    def test_promoted_subvolume_matches_the_master(self, master_cli):
        """standalone turns the last received snapshot into the live subvolume, unchanged."""
        slave = SLAVES[0]
        rem(MASTER, f"""
            set -e
            rm -rf {DATA} && mkdir -p {DATA}/promote
            dd if=/dev/urandom of={DATA}/promote/data.bin bs=1M count=8 status=none
            ln -s data.bin {DATA}/promote/link
            sync
        """)
        # Other destructive tests may have detached slaves already, so only
        # this slave has to succeed.
        run_cycle(master_cli, expect_ok=False)
        newest = snaps(MASTER, SUBVOL)[-1]
        remote = {s.name: s for s in snaps(slave, SUBVOL)}
        assert newest.name in remote and remote[newest.name].received

        r = rem(slave, "btrepl standalone")
        assert "node is now standalone" in r.stdout + r.stderr

        assert manifest(slave, LIVE) == manifest(MASTER, newest.path)
        rem(slave, f"touch {DATA}/promote/writable && rm {DATA}/promote/writable")
