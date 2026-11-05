# Commands

| Command | Description |
|---|---|
| `btrepl init-master [--force]` | Initialize config, snapshot dir, btrfs quota. Fails if `btrfs_root` is not the top level of a btrfs mount, unless `--force` |
| `btrepl serve [--addr unix:<path> \| host:port --tls-cert … --tls-key … --tls-client-ca …]` | Start gRPC daemon with internal replication loop. Listens on the root-only socket `/run/btrepl/btrepl.sock` by default; TCP requires mutual TLS, see [gRPC API](grpc.md) |
| `btrepl add-slave -s <host>[:port] [--no-restrict]` | Add a slave, record its SSH host key if it is new, and restrict the master's key on it to `btrepl ssh-filter` |
| `btrepl del-slave -s <IP>` | Remove a slave |
| `btrepl run` | Run one replication cycle manually. Fails if another cycle is running on this host |
| `btrepl status [-s <IP>]` | Show timer state, snapshot counts, optionally remote latest snapshot |
| `btrepl standalone` | Promote latest snapshot to writable, detach from replication |
| `btrepl clear` | Stop replication and delete all local snapshots |
| `btrepl ssh-filter` | Forced command for the master's key on a slave, see [SSH access](ssh.md#restricting-the-masters-key) |

All commands accept `-c /path/to/config.yaml` to override the default config path (`/etc/btrepl/config.yaml`).
