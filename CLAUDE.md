# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`btrepl` pushes read-only btrfs snapshots from a master node to slave nodes, using incremental `btrfs send | ssh btrfs receive`. The repo has three parts:

- `src/btrepl/`: the Go module (`github.com/eleutherius/btrepl`). It holds the CLI, the gRPC daemon and the proto. **The Go module root is here, not at the repo root**, so run `go` commands with `-C src/btrepl` or from inside that directory.
- `src/pybtrepl/`: a Python gRPC client package (`btrepl.BtreplClient`) that wraps generated stubs in `btrepl/_pb/`.
- `tests/integration/`: pytest integration tests. They run against a real 4-VM OrbStack cluster. There are no Go unit tests.

README install/build paths such as `./cmd/btrepl` and `api/btrepl.proto` date from before the move to `src/btrepl/`. The real paths are `src/btrepl/cmd/btrepl` and `src/btrepl/api/btrepl.proto`.

## Commands

```bash
# Build linux amd64+arm64 binaries into build/bin/ (version is taken from git describe)
make build
# Build .deb packages into build/ (this is also the default `make` target)
make deb
# Quick local compile/vet
go -C src/btrepl build ./...
go -C src/btrepl vet ./...

# Regenerate stubs after editing src/btrepl/api/btrepl.proto (needs protoc, protoc-gen-go(-grpc), grpcio-tools)
make proto          # or: make proto-go / make proto-py
```

`proto-py` rewrites the generated import to `from btrepl._pb import btrepl_pb2` using `sed -i ''`, which is BSD/macOS syntax. Commit the generated files (`*.pb.go`, `_pb/*.py`, `*.pyi`) together with the proto change.

### Integration tests

Requirements: OrbStack with a base VM named `debian-arm-13`, plus an arm64 `.deb` in `build/` (from `make deb`). The Python env is managed with `uv` from the root `pyproject.toml`, which installs `src/pybtrepl` as a package.

```bash
make cluster-up                 # create btrepl1 (master) + btrepl2-4 (slaves)
make test-integration           # run against an already-running cluster
make test-integration-clean     # build deb, spin up, test, tear down
make cluster-down

uv run pytest tests/integration/test_grpc.py::test_name -v   # single test
uv run pytest tests/integration/ -v --destructive            # also run clear/standalone tests
```

The pytest options `--spin-up`, `--teardown` and `--destructive` are defined in `tests/integration/conftest.py`. Tests marked `@pytest.mark.destructive` are skipped unless you pass `--destructive`. The tests reach the VMs through `orbctl run`. They reach gRPC through an SSH port-forward tunnel, because port 50051 isn't exposed to the host.

### Lint

Lint runs through pre-commit (`pre-commit run --all-files`): `gofmt -w`, shellcheck, and YAML/JSON/TOML/whitespace checks. CI runs the same hooks. MkDocs docs (`docs/`, `mkdocs.yml`) are built with `mkdocs build --strict` and deployed to GitHub Pages.

## Architecture

**Two run modes that share one replication engine:**
- **Timer mode:** `init-master` writes a `btrepl.timer` systemd unit (`internal/systemd`), and `add-slave` enables it. Each tick runs `btrepl run` for one cycle.
- **Daemon mode:** `btrepl serve`. The packaged `deploy/btrepl.service` runs this, and the `.deb` postinst enables it. `internal/server.RunLoop` runs cycles on a ticker set to `cfg.Interval`. The gRPC `Run` RPC triggers a cycle early through a 1-slot channel (`runCh`); if a run is already queued, it returns `ok=false`.

**Config is re-read from disk on every operation.** The server calls `config.Load(cfgPath)` on each cycle and RPC, and `AddSlave`/`DelSlave` persist through `config.Save`. The YAML file at `/etc/btrepl/config.yaml` is the single source of truth, and no config state is held in memory.

**Replication cycle** (`internal/replication/replicator.go`): for each slave, it opens an SSH connection (`internal/sshclient`, built on `x/crypto/ssh` with key auth only). Then for each subvolume it:
1. creates a new read-only snapshot;
2. lists remote snapshots;
3. `findParent` picks the newest local snapshot that also exists remotely (if there is none, it does a full send);
4. `sshclient.PipeFrom` streams the local `btrfs send` stdout into the remote `btrfs receive` stdin, with no temp files;
5. prunes old snapshots locally (`keep_sender`) and remotely (`keep_receiver`).

Errors for one slave or subvolume are logged, and the cycle continues; the last error is returned. Note that a new snapshot is created for **each slave × subvolume** pair.

**Snapshot naming is load-bearing.** Names follow `<snapshot_prefix><subvol without leading @>_<YYYYMMDDTHHMMSS UTC>`, stored in `<btrfs_root>/<snapshot_dir>`. Listing, sorting, parent matching and pruning all depend on parsing this format (`internal/btrfs`), so changing it breaks the ability to find parents in existing deployments.

**Log streaming:** `main` installs `logbroadcast.Broadcaster` as the default `slog` handler. It writes to stderr and also fans each record out to subscribers, and `WatchLogs` subscribes a channel to stream `LogEntry` messages to gRPC clients.

`standalone` promotes the latest received snapshot to a writable subvolume so that a slave can take over. `clear` stops replication and deletes local snapshots.
