# Comparison

`btrepl` is not the only tool that manages btrfs snapshots. The two most common alternatives are [btrbk](https://github.com/digint/btrbk) and [snapper](http://snapper.io/). They solve overlapping but different problems:

- **snapper** takes and cleans up *local* snapshots, mainly for rollback on a single machine.
- **btrbk** is a general-purpose snapshot and backup tool: local snapshots plus send/receive to backup targets.
- **btrepl** keeps one or more *replica* nodes in sync with a master and can promote a replica when the master is lost.

## Feature table

| | btrepl | btrbk | snapper |
|---|---|---|---|
| Main purpose | Master → slaves replication and failover | Snapshots and backups | Local snapshots and rollback |
| Language / packaging | Go, single static binary, `.deb` / `.rpm` | Perl script | C++, packaged by most distros |
| Local snapshots | Yes | Yes | Yes |
| Send to other hosts | Yes, push over SSH | Yes, push or pull over SSH | Via `snbk` in newer versions (0.11+) |
| Incremental send | Yes, parent picked automatically | Yes | With `snbk` |
| Several targets | Yes, any number of slaves | Yes | With `snbk` |
| Retention | Snapshot count per side (`keep_sender`, `keep_receiver`) | Time-based: hourly / daily / weekly / monthly | Timeline and number cleanup |
| Scheduling | Built-in daemon loop or systemd timer | cron / systemd timer | systemd timer |
| Long-running daemon | Yes (`btrepl serve`) | No | Yes (`snapperd`, D-Bus) |
| Remote API | gRPC: run, status, add/remove slaves, log streaming | No | D-Bus, local only |
| Add a target at runtime | `btrepl add-slave` or gRPC `AddSlave` | Edit config | — |
| Failover / promote replica | `btrepl standalone` | Manual (`btrfs subvolume snapshot`) | — |
| Rollback of the running system | No | No | Yes (`snapper rollback`, pre/post snapshots around package updates) |
| Compression, rate limit, raw/encrypted targets | No | Yes | No |
| Restricted SSH on target | No, root with a key | Yes (`ssh_filter_btrbk.sh`) | — |
| Filesystems | btrfs | btrfs | btrfs, LVM thin, ext4 (limited) |

The information about btrbk and snapper reflects their documentation at the time of writing. Check the projects for current features.

## Which one to use

**Use btrepl** when you want warm standby nodes: the same subvolumes kept on several machines, a short interval between cycles, and a single command to turn a replica into a writable node. The gRPC API lets you drive it from automation or a control plane without SSH-ing into the master.

**Use btrbk** when you need classic backups: long time-based retention (daily/weekly/monthly), pulling backups from a backup server, compressed or encrypted archives on non-btrfs storage, or a locked-down SSH user on the target.

**Use snapper** when you care about one machine: automatic snapshots before and after package updates and rolling back the root filesystem.

They can also run side by side. For example, snapper can protect the root filesystem locally while btrepl replicates data subvolumes such as `@data` or `@postgres` to standby nodes. Keep their snapshot directories separate so the tools do not prune each other's snapshots.
