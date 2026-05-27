"""btrepl gRPC client."""

from __future__ import annotations

from collections.abc import Iterator
from dataclasses import dataclass

import grpc

from btrepl._pb import btrepl_pb2 as pb
from btrepl._pb import btrepl_pb2_grpc as pb_grpc


@dataclass
class SubvolStatus:
    name: str
    snapshot_count: int
    latest_snapshot: str


@dataclass
class Status:
    timer_active: bool
    interval: str
    slaves: list[str]
    subvolumes: list[SubvolStatus]


@dataclass
class LogEntry:
    time: str
    level: str
    message: str
    attrs: dict[str, str]


class BtreplClient:
    """Synchronous gRPC client for the btrepl daemon.

    Usage::

        with BtreplClient("192.168.1.10:50051") as c:
            print(c.status())
            c.run()
    """

    def __init__(self, addr: str, timeout: float = 10.0) -> None:
        self._addr = addr
        self._timeout = timeout
        self._channel: grpc.Channel | None = None
        self._stub: pb_grpc.BtreplStub | None = None

    def connect(self) -> "BtreplClient":
        self._channel = grpc.insecure_channel(self._addr)
        self._stub = pb_grpc.BtreplStub(self._channel)
        return self

    def close(self) -> None:
        if self._channel is not None:
            self._channel.close()
            self._channel = None
            self._stub = None

    def __enter__(self) -> "BtreplClient":
        return self.connect()

    def __exit__(self, *_: object) -> None:
        self.close()

    @property
    def stub(self) -> pb_grpc.BtreplStub:
        if self._stub is None:
            raise RuntimeError("not connected — call connect() or use as context manager")
        return self._stub

    def status(self) -> Status:
        resp: pb.StatusResponse = self.stub.Status(
            pb.StatusRequest(), timeout=self._timeout
        )
        return Status(
            timer_active=resp.timer_active,
            interval=resp.interval,
            slaves=list(resp.slaves),
            subvolumes=[
                SubvolStatus(
                    name=sv.name,
                    snapshot_count=sv.snapshot_count,
                    latest_snapshot=sv.latest_snapshot,
                )
                for sv in resp.subvolumes
            ],
        )

    def run(self) -> None:
        """Trigger one replication cycle."""
        resp: pb.RunResponse = self.stub.Run(
            pb.RunRequest(), timeout=self._timeout
        )
        if not resp.ok:
            raise RuntimeError(f"btrepl run failed: {resp.error}")

    def add_slave(self, ip: str) -> None:
        resp: pb.SlaveResponse = self.stub.AddSlave(
            pb.SlaveRequest(ip=ip), timeout=self._timeout
        )
        if not resp.ok:
            raise RuntimeError(f"add_slave failed: {resp.error}")

    def del_slave(self, ip: str) -> None:
        resp: pb.SlaveResponse = self.stub.DelSlave(
            pb.SlaveRequest(ip=ip), timeout=self._timeout
        )
        if not resp.ok:
            raise RuntimeError(f"del_slave failed: {resp.error}")

    def watch_logs(self) -> Iterator[LogEntry]:
        """Stream log entries from the daemon (blocking iterator)."""
        for entry in self.stub.WatchLogs(pb.WatchLogsRequest()):
            yield LogEntry(
                time=entry.time,
                level=entry.level,
                message=entry.message,
                attrs=dict(entry.attrs),
            )
