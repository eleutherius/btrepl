from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class RunRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RunResponse(_message.Message):
    __slots__ = ("ok", "error")
    OK_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    ok: bool
    error: str
    def __init__(self, ok: bool = ..., error: _Optional[str] = ...) -> None: ...

class StatusRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class StatusResponse(_message.Message):
    __slots__ = ("timer_active", "interval", "slaves", "subvolumes")
    TIMER_ACTIVE_FIELD_NUMBER: _ClassVar[int]
    INTERVAL_FIELD_NUMBER: _ClassVar[int]
    SLAVES_FIELD_NUMBER: _ClassVar[int]
    SUBVOLUMES_FIELD_NUMBER: _ClassVar[int]
    timer_active: bool
    interval: str
    slaves: _containers.RepeatedScalarFieldContainer[str]
    subvolumes: _containers.RepeatedCompositeFieldContainer[SubvolStatus]
    def __init__(self, timer_active: bool = ..., interval: _Optional[str] = ..., slaves: _Optional[_Iterable[str]] = ..., subvolumes: _Optional[_Iterable[_Union[SubvolStatus, _Mapping]]] = ...) -> None: ...

class SubvolStatus(_message.Message):
    __slots__ = ("name", "snapshot_count", "latest_snapshot")
    NAME_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_COUNT_FIELD_NUMBER: _ClassVar[int]
    LATEST_SNAPSHOT_FIELD_NUMBER: _ClassVar[int]
    name: str
    snapshot_count: int
    latest_snapshot: str
    def __init__(self, name: _Optional[str] = ..., snapshot_count: _Optional[int] = ..., latest_snapshot: _Optional[str] = ...) -> None: ...

class SlaveRequest(_message.Message):
    __slots__ = ("ip",)
    IP_FIELD_NUMBER: _ClassVar[int]
    ip: str
    def __init__(self, ip: _Optional[str] = ...) -> None: ...

class SlaveResponse(_message.Message):
    __slots__ = ("ok", "error")
    OK_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    ok: bool
    error: str
    def __init__(self, ok: bool = ..., error: _Optional[str] = ...) -> None: ...

class WatchLogsRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class LogEntry(_message.Message):
    __slots__ = ("level", "message", "time", "attrs")
    class AttrsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    LEVEL_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    TIME_FIELD_NUMBER: _ClassVar[int]
    ATTRS_FIELD_NUMBER: _ClassVar[int]
    level: str
    message: str
    time: str
    attrs: _containers.ScalarMap[str, str]
    def __init__(self, level: _Optional[str] = ..., message: _Optional[str] = ..., time: _Optional[str] = ..., attrs: _Optional[_Mapping[str, str]] = ...) -> None: ...
