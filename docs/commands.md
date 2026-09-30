# Commands

| Command | Description |
|---|---|
| `btrepl init-master [--force]` | Initialize config, snapshot dir, btrfs quota. Fails if `btrfs_root` is not the top level of a btrfs mount, unless `--force` |
| `btrepl serve [--addr :50051]` | Start gRPC daemon with internal replication loop |
| `btrepl add-slave -s <IP>` | Add a slave |
| `btrepl del-slave -s <IP>` | Remove a slave |
| `btrepl run` | Run one replication cycle manually |
| `btrepl status [-s <IP>]` | Show timer state, snapshot counts, optionally remote latest snapshot |
| `btrepl standalone` | Promote latest snapshot to writable, detach from replication |
| `btrepl clear` | Stop replication and delete all local snapshots |

All commands accept `-c /path/to/config.yaml` to override the default config path (`/etc/btrepl/config.yaml`).
