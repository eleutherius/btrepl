# gRPC API

`btrepl serve` starts a long-running daemon that exposes a gRPC server (default `:50051`) and runs the replication loop internally based on `interval` from the config. No systemd timer needed.

## Proto

```protobuf
service Btrepl {
  rpc Run(RunRequest)             returns (RunResponse);
  rpc Status(StatusRequest)       returns (StatusResponse);
  rpc AddSlave(SlaveRequest)      returns (SlaveResponse);
  rpc DelSlave(SlaveRequest)      returns (SlaveResponse);
  rpc WatchLogs(WatchLogsRequest) returns (stream LogEntry);
}
```

Full definition: [`api/btrepl.proto`](https://github.com/eleutherius/btrepl/blob/main/src/btrepl/api/btrepl.proto)

## Python example

Install the generated stubs or regenerate from the proto:

```bash
pip install grpcio grpcio-tools
python -m grpc_tools.protoc -I api --python_out=. --grpc_python_out=. api/btrepl.proto
```

```python
import grpc
import btrepl_pb2, btrepl_pb2_grpc

channel = grpc.insecure_channel("192.168.139.232:50051")
stub = btrepl_pb2_grpc.BtreplStub(channel)

# trigger replication immediately
stub.Run(btrepl_pb2.RunRequest())

# add a slave
stub.AddSlave(btrepl_pb2.SlaveRequest(ip="192.168.139.196"))

# get status
resp = stub.Status(btrepl_pb2.StatusRequest())
print(resp.slaves, resp.subvolumes)

# stream logs in real time
for entry in stub.WatchLogs(btrepl_pb2.WatchLogsRequest()):
    print(f"[{entry.level}] {entry.message}")
```
