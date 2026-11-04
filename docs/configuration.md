# Configuration

The config lives at `/etc/btrepl/config.yaml` (override with `-c`). `btrepl init-master` creates it with defaults. It is re-read on every run and RPC, so edits take effect without a restart.

| Key | Default | Description |
|---|---|---|
| `btrfs_root` | `/btrfs` | Mount point of the btrfs filesystem |
| `snapshot_dir` | `.snapshots` | Directory inside `btrfs_root` for snapshots |
| `snapshot_prefix` | `btrepl_` | Prefix added to every snapshot name |
| `ssh_user` | `root` | SSH username for slave connections |
| `ssh_identity` | `/root/.ssh/id_ed25519` | Path to the SSH private key |
| `keep_sender` | `428` | Number of snapshots to keep on master (~18 days at 1h interval). The last common snapshot with each slave is kept on top of these |
| `keep_receiver` | `10` | Number of snapshots to keep on each slave |
| `max_anchor_age` | `""` | Let pruning delete the last common snapshot with a slave once it is older than this, as a Go duration such as `720h`. Empty means keep it until `del-slave`. The next send to that slave is then a full send |
| `subvolumes` | `[]` | List of btrfs subvolume names to replicate |
| `slaves` | `[]` | Managed automatically by `add-slave` / `del-slave` |

## Retention

Each cycle takes one snapshot per subvolume, so `keep_sender` counts cycles. The master records the last snapshot it has in common with each slave in `<btrfs_root>/<snapshot_dir>/.btrepl-cursors.json` and does not prune it, even while the slave is unreachable: btrfs needs that snapshot on both sides for an incremental send. Removing a slave with `del-slave` releases its snapshot.
