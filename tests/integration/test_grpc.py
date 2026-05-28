"""
gRPC API tests.

Every call goes through an SSH port-forwarding tunnel to the btrepl daemon
(btrepl:50051). No direct network access to the VMs is assumed.

Fixtures used (all defined in conftest.py):
  master_client        — session-scoped BtreplClient for btrepl1
  slave_clients        — list[BtreplClient] for btrepl2/3/4, one tunnel each
  grpc_client_factory  — factory that opens a fresh SSH tunnel + client on demand
  slave_ips            — list of IP strings for the three slaves
"""

import threading
import time

import pytest

from btrepl import BtreplClient
from conftest import SLAVES

EXPECTED_SUBVOLS = frozenset({"@eventsPictures", "@reports"})
SLAVE_COUNT = len(SLAVES)  # 3


def _safe_run(client: BtreplClient, max_wait: float = 60.0) -> None:
    """
    Enqueue a run cycle, retrying while the previous one is still queued.

    The server's runCh has capacity 1. If a cycle is already queued (but not
    yet consumed by the RunLoop goroutine), run() returns {ok:false,
    error:"replication already running"}.  This wrapper waits for the goroutine
    to drain the channel and retries.
    """
    deadline = time.monotonic() + max_wait
    while time.monotonic() < deadline:
        try:
            client.run()
            return
        except RuntimeError as exc:
            if "already running" not in str(exc):
                raise
            time.sleep(2.0)
    raise TimeoutError(f"could not enqueue run cycle within {max_wait} s")


# ── Status ────────────────────────────────────────────────────────────────────


class TestStatus:
    def test_master_subvolumes(self, master_client):
        st = master_client.status()
        assert {sv.name for sv in st.subvolumes} == EXPECTED_SUBVOLS

    def test_master_interval(self, master_client):
        assert master_client.status().interval == "1h"

    def test_master_has_three_slaves(self, master_client):
        assert len(master_client.status().slaves) == SLAVE_COUNT

    def test_slave_ips_match_master_registry(self, master_client, slave_ips):
        assert set(master_client.status().slaves) == set(slave_ips)

    def test_all_slave_daemons_reachable(self, slave_clients):
        for client in slave_clients:
            st = client.status()
            assert {sv.name for sv in st.subvolumes} == EXPECTED_SUBVOLS

    def test_all_slaves_have_matching_interval(self, slave_clients):
        for client in slave_clients:
            assert client.status().interval == "1h"


# ── Replication cycle ─────────────────────────────────────────────────────────


class TestReplication:
    def test_run_ok(self, master_client):
        _safe_run(master_client)

    def test_run_increments_master_snapshot_count(self, master_client):
        before = {sv.name: sv.snapshot_count for sv in master_client.status().subvolumes}
        _safe_run(master_client)

        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            time.sleep(3)
            after = {sv.name: sv.snapshot_count for sv in master_client.status().subvolumes}
            if all(after[n] > before[n] for n in before):
                return

        delta = {
            n: master_client.status().subvolumes[i].snapshot_count - before[n]
            for i, n in enumerate(before)
        }
        pytest.fail(f"master snapshot count did not increase within 60 s — delta: {delta}")

    def test_run_replicates_to_all_slaves(self, master_client, slave_clients):
        """Every slave must have ≥ 1 snapshot per subvol after a run cycle."""
        _safe_run(master_client)

        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            time.sleep(5)
            ready = sum(
                all(sv.snapshot_count >= 1 for sv in c.status().subvolumes)
                for c in slave_clients
            )
            if ready == len(slave_clients):
                return

        states = [
            {sv.name: sv.snapshot_count for sv in c.status().subvolumes}
            for c in slave_clients
        ]
        pytest.fail(f"not all slaves received snapshots within 90 s — counts: {states}")

    def test_latest_snapshot_non_empty_after_run(self, master_client):
        _safe_run(master_client)
        time.sleep(5)
        for sv in master_client.status().subvolumes:
            assert sv.latest_snapshot, f"{sv.name}: latest_snapshot is empty after run"


# ── Slave management ──────────────────────────────────────────────────────────


class TestSlaveManagement:
    def test_del_nonexistent_raises(self, master_client):
        with pytest.raises(RuntimeError, match="slave not found"):
            master_client.del_slave("192.0.2.99")

    def test_slave_count_unchanged_after_bad_del(self, master_client):
        n = len(master_client.status().slaves)
        try:
            master_client.del_slave("192.0.2.99")
        except RuntimeError:
            pass
        assert len(master_client.status().slaves) == n

    def test_del_and_readd_round_trip(self, master_client, slave_ips):
        """Remove last slave, verify it's gone, re-add, verify it's back."""
        ip = slave_ips[-1]

        master_client.del_slave(ip)
        assert ip not in master_client.status().slaves

        master_client.add_slave(ip)
        assert ip in master_client.status().slaves


# ── Log streaming ─────────────────────────────────────────────────────────────


class TestWatchLogs:
    @pytest.mark.timeout(45)
    def test_receives_entries_during_run(self, master_client, grpc_client_factory):
        """WatchLogs stream delivers entries while a run cycle is in progress."""
        received = []
        errors: list[Exception] = []

        def _collect() -> None:
            try:
                with grpc_client_factory() as watcher:
                    for entry in watcher.watch_logs():
                        received.append(entry)
                        if len(received) >= 3:
                            break
            except Exception as exc:
                errors.append(exc)

        t = threading.Thread(target=_collect, daemon=True)
        t.start()
        time.sleep(0.4)  # let subscriber register on the server
        _safe_run(master_client)
        t.join(timeout=40)

        assert not errors, f"watcher raised: {errors[0]}"
        assert received, "no log entries received within timeout"

    @pytest.mark.timeout(45)
    def test_log_entry_fields_populated(self, master_client, grpc_client_factory):
        received = []

        def _collect() -> None:
            try:
                with grpc_client_factory() as watcher:
                    for entry in watcher.watch_logs():
                        received.append(entry)
                        break
            except Exception:
                pass

        t = threading.Thread(target=_collect, daemon=True)
        t.start()
        time.sleep(0.4)
        _safe_run(master_client)
        t.join(timeout=40)

        assert received, "no log entry received"
        e = received[0]
        assert e.level, "level is empty"
        assert e.message, "message is empty"
        assert e.time, "time is empty"

    @pytest.mark.timeout(45)
    def test_multiple_subscribers_receive_same_entries(self, grpc_client_factory):
        """Two concurrent watchers both receive entries from the same run."""
        queues: list[list] = [[], []]
        errors: list[Exception] = []

        def _collect(idx: int) -> None:
            try:
                with grpc_client_factory() as watcher:
                    for entry in watcher.watch_logs():
                        queues[idx].append(entry)
                        if len(queues[idx]) >= 2:
                            break
            except Exception as exc:
                errors.append(exc)

        threads = [threading.Thread(target=_collect, args=(i,), daemon=True) for i in range(2)]
        for t in threads:
            t.start()
        time.sleep(0.4)

        with grpc_client_factory() as trigger:
            _safe_run(trigger)

        for t in threads:
            t.join(timeout=40)

        assert not errors, f"watcher raised: {errors[0]}"
        assert queues[0], "subscriber 0 received no entries"
        assert queues[1], "subscriber 1 received no entries"
