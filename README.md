# btrepl

Automatic btrfs geo-replication with incremental send/receive over SSH, managed by a systemd timer.

## What it does

`btrepl` runs on a **master** node and periodically pushes read-only btrfs snapshots to one or more **slave** nodes. Each replication cycle:

1. Takes a fresh read-only snapshot of every configured subvolume.
2. Finds the latest snapshot that already exists on the slave (the *parent*).
3. Streams `btrfs send -p <parent> <snap>` directly into `btrfs receive` on the slave over SSH — no temporary files, no intermediate buffer.
4. Prunes old snapshots on both sides (configurable retention).

When a slave needs to take over, `btrepl standalone` promotes the latest snapshot to a writable subvolume, stopping replication cleanly.

## Requirements

- Linux with btrfs
- `btrfs-progs` installed on master **and** slaves
- Passwordless SSH access from master to each slave (key-based, default: `/root/.ssh/id_ed25519`)
- Go 1.21+ to build from source

## Installation

```bash
go install github.com/liakhov/btrepl/cmd/btrepl@latest
```

Or build locally:

```bash
git clone https://github.com/liakhov/btrepl
cd btrepl
go build -o /usr/local/bin/btrepl ./cmd/btrepl
```

## Quick start

### 1. Initialize the master

```bash
btrepl init-master
```

Creates `/etc/btrepl/config.yaml` with defaults and enables btrfs quota on the root.

### 2. Edit the config

```bash
vim /etc/btrepl/config.yaml
```

```yaml
btrfs_root: /btrfs
ssh_identity: /root/.ssh/id_ed25519
ssh_user: root
snapshot_dir: .snapshots
snapshot_prefix: btrepl_
keep_sender: 428       # snapshots to keep on master
keep_receiver: 10      # snapshots to keep on each slave
subvolumes:
  - "@data"
  - "@postgres"
slaves: []             # managed by add-slave / del-slave
```

### 3. Add a slave

```bash
btrepl add-slave -s 192.168.1.10
```

- Connects to the slave via SSH, creates the snapshot directory.
- Adds the slave to the config.
- Enables and starts `btrepl.timer` (see [Systemd setup](#systemd-setup)).

## Commands

| Command | Description |
|---|---|
| `btrepl init-master` | Initialize config, snapshot dir, btrfs quota |
| `btrepl serve [--addr :50051]` | Start gRPC daemon with internal replication loop |
| `btrepl add-slave -s <IP>` | Add a slave |
| `btrepl del-slave -s <IP>` | Remove a slave |
| `btrepl run` | Run one replication cycle manually |
| `btrepl status [-s <IP>]` | Show timer state, snapshot counts, optionally remote latest snapshot |
| `btrepl standalone` | Promote latest snapshot to writable, detach from replication |
| `btrepl clear` | Stop replication and delete all local snapshots |

All commands accept `-c /path/to/config.yaml` to override the default config path (`/etc/btrepl/config.yaml`).

## gRPC API

`btrepl serve` starts a long-running daemon that exposes a gRPC server (default `:50051`) and runs the replication loop internally based on `interval` from the config. No systemd timer needed.

### Proto

```protobuf
service Btrepl {
  rpc Run(RunRequest)             returns (RunResponse);
  rpc Status(StatusRequest)       returns (StatusResponse);
  rpc AddSlave(SlaveRequest)      returns (SlaveResponse);
  rpc DelSlave(SlaveRequest)      returns (SlaveResponse);
  rpc WatchLogs(WatchLogsRequest) returns (stream LogEntry);
}
```

Full definition: [`api/btrepl.proto`](api/btrepl.proto)

### Python example

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

### Systemd setup (daemon mode)

```bash
cp deploy/btrepl.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now btrepl.service
```

## Systemd setup

`btrepl add-slave` and `btrepl del-slave` call `systemctl enable/start/stop/disable` automatically. Unit files are in [`deploy/`](deploy/) — install them first:

Reload and the CLI will handle the rest:

```bash
cp deploy/btrepl.{service,timer} /etc/systemd/system/
systemctl daemon-reload
btrepl add-slave -s <IP>   # enables + starts the timer
```

## Failover: promoting a slave

On the slave node:

```bash
# Install btrepl and copy the master config, then:
btrepl standalone
```

This stops the timer, deletes the existing writable subvolume, and replaces it with a writable snapshot of the latest received replica. The node is now fully independent.

## How incremental send works

```
master                              slave
------                              -----
btrepl_data_20240601T120000  <-->  btrepl_data_20240601T120000  ← parent
btrepl_data_20240601T130000         (new snapshot being sent)
        │
        └─ btrfs send -p parent snap ──SSH──▶ btrfs receive /btrfs/.snapshots
```

If no common snapshot exists, a full send is performed automatically.

## Configuration reference

| Key | Default | Description |
|---|---|---|
| `btrfs_root` | `/btrfs` | Mount point of the btrfs filesystem |
| `snapshot_dir` | `.snapshots` | Directory inside `btrfs_root` for snapshots |
| `snapshot_prefix` | `btrepl_` | Prefix added to every snapshot name |
| `ssh_user` | `root` | SSH username for slave connections |
| `ssh_identity` | `/root/.ssh/id_ed25519` | Path to the SSH private key |
| `keep_sender` | `428` | Number of snapshots to keep on master (~18 days at 1h interval) |
| `keep_receiver` | `10` | Number of snapshots to keep on each slave |
| `subvolumes` | `[]` | List of btrfs subvolume names to replicate |
| `slaves` | `[]` | Managed automatically by `add-slave` / `del-slave` |

## License

MIT
