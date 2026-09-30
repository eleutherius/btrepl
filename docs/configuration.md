# Configuration

The config lives at `/etc/btrepl/config.yaml` (override with `-c`). `btrepl init-master` creates it with defaults. It is re-read on every run and RPC, so edits take effect without a restart.

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
