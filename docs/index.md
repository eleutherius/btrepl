# Introduction

Automatic btrfs geo-replication with incremental send/receive over SSH, managed by a systemd timer.

## What it does

`btrepl` runs on a **master** node and periodically pushes read-only btrfs snapshots to one or more **slave** nodes. Each replication cycle:

1. Takes one read-only snapshot of every configured subvolume, whether or not the slaves are reachable.
2. For each slave, finds the newest snapshot both sides have (the *parent*). Snapshots are matched by UUID, the way `btrfs receive` resolves the parent, so a snapshot on the slave counts only if its receive completed.
3. Streams `btrfs send -p <parent> <snap>` directly into `btrfs receive` on the slave over SSH — no temporary files, no intermediate buffer.
4. Prunes old snapshots on both sides (configurable retention). The master keeps the last snapshot it has in common with each slave, so a slave that was offline catches up with an incremental send.

When a slave needs to take over, `btrepl standalone` promotes the latest complete received snapshot to a writable subvolume, stopping replication cleanly.

## How incremental send works

```
master                              slave
------                              -----
btrepl_data_20240601T120000  <-->  btrepl_data_20240601T120000  ← parent
btrepl_data_20240601T130000         (new snapshot being sent)
        │
        └─ btrfs send -p parent snap ──SSH──▶ btrfs receive /btrfs/.snapshots
```

If no common snapshot exists, a full send is performed automatically. An interrupted receive leaves a writable snapshot without a received UUID on the slave; the next cycle deletes it before sending.

## Run modes

`btrepl` can run in two ways, sharing the same replication engine:

- **Daemon mode** — `btrepl serve` runs a gRPC server (on a root-only Unix socket, or TCP with mutual TLS) and an internal replication loop on the `interval` from the config. This is what the packages and the install script set up by default. See [gRPC API](grpc.md).
- **Timer mode** — a `btrepl.timer` systemd unit runs `btrepl run` once per tick. See [Systemd setup](systemd.md).

## Next steps

- [Installation](installation.md)
- [Getting started](getting-started.md)
- [Configuration](configuration.md)
- [Commands](commands.md)
- [Comparison with btrbk and snapper](comparison.md)

## License

MIT
