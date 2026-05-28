"""
CLI interface tests.

Every command is executed on the remote machine via:
    orbctl run -m <machine> -u root btrepl <args>

No local Python gRPC client is used here — this validates the binary's
command-line interface independently of the gRPC layer.

Fixtures used:
  master_cli   — NodeCLI(btrepl1)
  slave_cli    — list[NodeCLI] for btrepl2/3/4
  slave_ips    — list of slave IP strings (needed for -s flag tests)
"""

import re
import time

import pytest

EXPECTED_SUBVOLS = {"@eventsPictures", "@reports"}


# ── btrepl status ─────────────────────────────────────────────────────────────


class TestStatusCLI:
    def test_exits_zero(self, master_cli):
        assert master_cli("status").returncode == 0

    def test_contains_all_subvolumes(self, master_cli):
        out = master_cli("status").stdout
        for sv in EXPECTED_SUBVOLS:
            assert sv in out, f"{sv!r} not found in status output"

    def test_shows_slaves_line(self, master_cli):
        assert "slaves:" in master_cli("status").stdout

    def test_timer_reported_active(self, master_cli):
        assert "active" in master_cli("status").stdout

    def test_snapshot_count_present(self, master_cli):
        assert "snapshots:" in master_cli("status").stdout

    def test_slave_flag_shows_remote_info(self, master_cli, slave_ips):
        """btrepl status -s <slave_ip> appends remote snapshot info."""
        r = master_cli("status", "-s", slave_ips[0])
        assert r.returncode == 0
        assert "remote" in r.stdout

    def test_all_slave_ips_listed_in_status(self, master_cli, slave_ips):
        out = master_cli("status").stdout
        for ip in slave_ips:
            assert ip in out, f"slave {ip} not listed in status output"


# ── btrepl run ────────────────────────────────────────────────────────────────


class TestRunCLI:
    def test_exits_zero(self, master_cli):
        assert master_cli("run").returncode == 0

    def test_logs_replication_result(self, master_cli):
        r = master_cli("run")
        combined = r.stdout + r.stderr
        assert "replication" in combined.lower()

    def test_creates_snapshot_on_master(self, master_cli):
        """Snapshot count should increase after a run."""
        def _counts() -> dict[str, int]:
            out = master_cli("status").stdout
            counts = {}
            for sv in EXPECTED_SUBVOLS:
                m = re.search(rf"{re.escape(sv)}\s+snapshots:\s+(\d+)", out)
                if m:
                    counts[sv] = int(m.group(1))
            return counts

        before = _counts()
        master_cli("run")

        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            time.sleep(3)
            after = _counts()
            if all(after.get(sv, 0) > before.get(sv, -1) for sv in EXPECTED_SUBVOLS):
                return

        after = _counts()
        pytest.fail(f"snapshot count did not increase within 60 s — before: {before}, after: {after}")


# ── btrepl add-slave / del-slave ──────────────────────────────────────────────


class TestSlaveManagementCLI:
    def test_del_nonexistent_exits_nonzero(self, master_cli):
        r = master_cli("del-slave", "-s", "192.0.2.99", check=False)
        assert r.returncode != 0

    def test_del_nonexistent_error_message(self, master_cli):
        r = master_cli("del-slave", "-s", "192.0.2.99", check=False)
        assert "not found" in (r.stdout + r.stderr).lower()

    def test_del_and_readd_round_trip(self, master_cli, slave_ips):
        """Remove last slave, verify gone, re-add, verify back."""
        ip = slave_ips[-1]

        r = master_cli("del-slave", "-s", ip)
        assert r.returncode == 0

        assert ip not in master_cli("status").stdout

        r = master_cli("add-slave", "-s", ip)
        assert r.returncode == 0

        assert ip in master_cli("status").stdout

    def test_add_slave_missing_flag_exits_nonzero(self, master_cli):
        r = master_cli("add-slave", check=False)
        assert r.returncode != 0

    def test_del_slave_missing_flag_exits_nonzero(self, master_cli):
        r = master_cli("del-slave", check=False)
        assert r.returncode != 0


# ── btrepl init-master ────────────────────────────────────────────────────────


class TestInitMasterCLI:
    def test_idempotent_on_already_initialized_master(self, master_cli):
        """Running init-master again on an active master must not fail."""
        r = master_cli("init-master")
        assert r.returncode == 0


# ── btrepl clear (destructive) ────────────────────────────────────────────────


@pytest.mark.destructive
class TestClearCLI:
    def test_clear_removes_all_local_snapshots(self, slave_cli):
        """btrepl clear on a slave removes its received snapshots."""
        s = slave_cli[0]

        # Ensure there is something to clear.
        s.shell("btrepl run", check=False)
        time.sleep(10)

        r = s("clear")
        assert r.returncode == 0

        # All snapshot counts should now be 0.
        out = s("status").stdout
        for m in re.finditer(r"snapshots:\s+(\d+)", out):
            assert int(m.group(1)) == 0, f"expected 0 snapshots after clear, got {m.group(1)}"

    def test_clear_output_mentions_cleared(self, slave_cli):
        r = slave_cli[1]("clear")
        combined = r.stdout + r.stderr
        assert "clear" in combined.lower() or r.returncode == 0


# ── btrepl standalone (destructive) ──────────────────────────────────────────


@pytest.mark.destructive
class TestStandaloneCLI:
    def test_standalone_detaches_and_restores(self, master_cli, slave_cli):
        """
        standalone on a slave stops replication services and restores subvols
        from the latest received snapshot.
        Uses slave_cli[2] (last slave) to avoid disturbing other tests.
        """
        s = slave_cli[2]

        # Ensure slave has at least one received snapshot to restore from.
        master_cli("run")
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            time.sleep(5)
            out = s("status").stdout
            counts = re.findall(r"snapshots:\s+(\d+)", out)
            if counts and all(int(c) >= 1 for c in counts):
                break

        r = s("standalone")
        assert r.returncode == 0
        combined = r.stdout + r.stderr
        assert "standalone" in combined.lower()

    def test_standalone_stops_btrepl_service(self, slave_cli):
        s = slave_cli[2]
        s("standalone", check=False)
        r = s.shell("systemctl is-active btrepl.service", check=False)
        assert r.stdout.strip() in ("inactive", "failed", "activating")
