# btrepl

Automatic btrfs geo-replication with incremental send/receive over SSH.

📖 **Documentation: <https://eleutherius.github.io/btrepl/>**

## What it does

`btrepl` runs on a **master** node and periodically pushes read-only btrfs snapshots to one or more **slave** nodes. Each replication cycle:

1. Takes a fresh read-only snapshot of every configured subvolume.
2. Finds the latest snapshot that already exists on the slave (the *parent*).
3. Streams `btrfs send -p <parent> <snap>` directly into `btrfs receive` on the slave over SSH — no temporary files, no intermediate buffer.
4. Prunes old snapshots on both sides (configurable retention).

When a slave needs to take over, `btrepl standalone` promotes the latest snapshot to a writable subvolume, stopping replication cleanly.

It runs either as a **daemon** (`btrepl serve`, with a gRPC API for triggering runs, managing slaves and streaming logs) or from a **systemd timer**. Read more in the [Introduction](https://eleutherius.github.io/btrepl/).

## Install

```bash
curl -LsSf https://github.com/eleutherius/btrepl/releases/latest/download/install.sh | sh
```

The script supports Debian/Ubuntu, Fedora/RHEL, openSUSE, Arch and Alpine on `amd64` and `arm64`. `.deb` and `.rpm` packages are also attached to every [release](https://github.com/eleutherius/btrepl/releases). See [Installation](https://eleutherius.github.io/btrepl/installation/) for options, packages and building from source.

## Quick start

```bash
btrepl init-master                  # create /etc/btrepl/config.yaml
vim /etc/btrepl/config.yaml         # set btrfs_root and subvolumes
btrepl add-slave -s 192.168.1.10    # register a slave and start replicating
btrepl status -s 192.168.1.10       # check snapshots on both sides
```

The master needs key-based root SSH access to each slave, and `btrfs-progs` on both sides. The full walkthrough is in [Getting started](https://eleutherius.github.io/btrepl/getting-started/).

## Documentation

| Page | What's inside |
|---|---|
| [Introduction](./docs/index.md) | How replication and incremental send work, run modes |
| [Installation](./docs/installation.md) | Requirements, install script, packages, build from source |
| [Getting started](./docs/getting-started.md) | Set up a master and the first slave |
| [Configuration](./docs/configuration.md) | All `config.yaml` keys |
| [Commands](./docs/commands.md) | CLI reference |
| [Systemd setup](./docs/systemd.md) | Daemon mode and timer mode |
| [gRPC API](./docs/grpc.md) | Proto and a Python client example |
| [Failover](./docs/failover.md) | Promote a slave to a standalone node |

## Repository layout

- [`src/btrepl/`](src/btrepl/) — Go module: CLI, gRPC daemon, [proto](src/btrepl/api/btrepl.proto)
- [`src/pybtrepl/`](src/pybtrepl/) — Python gRPC client
- [`tests/integration/`](tests/integration/) — pytest integration tests against an OrbStack VM cluster
- [`docs/`](docs/) — MkDocs sources of the documentation site

Build with `make build` (binaries), `make deb` / `make rpm` (packages), or `make packages` (both).

## License

MIT
