# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`btrepl` pushes read-only btrfs snapshots from a master node to slave nodes, using incremental `btrfs send | ssh btrfs receive`. The repo has three parts:

- `src/btrepl/`: the Go module (`github.com/eleutherius/btrepl`). It holds the CLI, the gRPC daemon and the proto. **The Go module root is here, not at the repo root**, so run `go` commands with `-C src/btrepl` or from inside that directory.
- `src/pybtrepl/`: a Python gRPC client package (`btrepl.BtreplClient`) that wraps generated stubs in `btrepl/_pb/`.
- `tests/integration/`: pytest integration tests. They run against a real 4-VM OrbStack cluster. Go unit tests (`*_test.go`) cover only the pure logic: parsing, parent choice, prune plans.

README install/build paths such as `./cmd/btrepl` and `api/btrepl.proto` date from before the move to `src/btrepl/`. The real paths are `src/btrepl/cmd/btrepl` and `src/btrepl/api/btrepl.proto`.

## Commands

```bash
# Build linux amd64+arm64 binaries into build/bin/ (version is taken from git describe)
make build
# Build .deb packages into build/ (this is also the default `make` target)
make deb
# Build .rpm packages into build/ (nfpm via `go run`, config in src/btrepl/deploy/rpm/)
make rpm
# Both; this is what .github/workflows/release.yml runs on a v* tag
make packages
# Quick local compile/vet/unit tests
go -C src/btrepl build ./...
go -C src/btrepl vet ./...
go -C src/btrepl test ./...

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

The pytest options `--spin-up`, `--teardown` and `--destructive` are defined in `tests/integration/conftest.py`. Tests marked `@pytest.mark.destructive` are skipped unless you pass `--destructive`. The tests reach the VMs through `orbctl run`. They reach gRPC through an SSH tunnel that forwards a local port to the daemon's Unix socket `/run/btrepl/btrepl.sock`; `test_api.py` also starts a second daemon on TCP with mutual TLS.

### Lint

Lint runs through pre-commit (`pre-commit run --all-files`): `gofmt -w`, shellcheck, and YAML/JSON/TOML/whitespace checks. CI runs the same hooks. MkDocs docs (`docs/`, `mkdocs.yml`) are built with `mkdocs build --strict` and deployed to GitHub Pages.

## Architecture

**Two run modes that share one replication engine:**
- **Timer mode:** `init-master` writes a `btrepl.timer` systemd unit (`internal/systemd`), and `add-slave` enables it. Each tick runs `btrepl run` for one cycle.
- **Daemon mode:** `btrepl serve`. The packaged `deploy/btrepl.service` runs this, and the `.deb` postinst enables it. `server.Listen` opens the API: by default the root-only Unix socket `/run/btrepl/btrepl.sock` (0600 in a 0700 dir), or TCP only with mutual TLS (`--tls-cert/--tls-key/--tls-client-ca`). Never add an unauthenticated TCP listener: the API can redirect replication. `internal/server.RunLoop` runs cycles on a ticker set to `cfg.Interval`. The gRPC `Run` RPC triggers a cycle early through a 1-slot channel (`runCh`); if a run is already queued, it returns `ok=false`.

**Config is re-read from disk on every operation.** The server calls `config.Load(cfgPath)` on each cycle and RPC, and `AddSlave`/`DelSlave` persist through `config.Save`. `RunLoop` also re-reads `interval` every minute and resets its ticker. The YAML file at `/etc/btrepl/config.yaml` is the single source of truth, and no config state is held in memory.

**Slave access:** `add-slave` (and the `AddSlave` RPC) call `replication.RestrictKey`, which rewrites the master's key line in the slave's `authorized_keys` to `restrict,from=…,command="btrepl ssh-filter"`, re-checks a cycle through it and restores the file if that fails. `sshclient` sends keepalives (30s × 3), limits `Run` to `ssh_command_timeout` and aborts `PipeFrom` after `send_idle_timeout` without progress.

**One cycle at a time:** `RunCycle` takes a non-blocking `flock` on `/run/btrepl.lock` (`replication/lock.go`) and returns `ErrCycleRunning` if the daemon or another `btrepl run` holds it.

**Replication cycle** (`internal/replication/replicator.go`) runs in three phases, and only when at least one slave is configured (slave nodes run the same daemon with `slaves: []`):
1. **Snapshot:** one read-only snapshot per subvolume, independent of slave availability.
2. **Replicate:** for each slave, open an SSH connection with `replication.Dial` (`internal/sshclient`, built on `x/crypto/ssh` with key auth only, host keys checked against `ssh_known_hosts`; `add-slave` dials with `trustNewHostKey` to record a new key) and list its subvolumes with `btrfs subvolume list -u -R` / `-r`. Remote snapshots that are writable or have no `received_uuid` are leftovers of an interrupted receive and are deleted. `latestCommon` picks the parent **by UUID** (local `SendUUID` == remote `received_uuid`), never by name. `sshclient.PipeFrom` streams the newest local snapshot's `btrfs send` stdout into the remote `btrfs receive` stdin, with no temp files. Then remote snapshots are pruned (`keep_receiver`, the newest always kept).
3. **Prune local:** keep the newest `keep_sender`, plus each configured slave's last common snapshot (its anchor, unless older than `max_anchor_age`). Anchors persist across cycles in `<snapshot_dir>/.btrepl-cursors.json` (`cursors.go`), so an unreachable slave's anchor survives.

The decision logic is in pure functions in `plan.go`, which have unit tests. Errors for one slave or subvolume are logged, and the cycle continues; the last error is returned.

**Remote commands** are built only by `internal/remote` (paths quoted with `remote.Quote`; `btrfs receive --chroot`; the snapshot dir is kept at 0700). `btrepl ssh-filter`, the forced command for the master's key on a slave, checks `SSH_ORIGINAL_COMMAND` against the same builders (`Commands.Allow`) and execs it without a shell, so a new remote command must be added there.

**Snapshot naming is load-bearing.** Names follow `<snapshot_prefix><subvol without leading @>_<YYYYMMDDTHHMMSS UTC>`, stored in `<btrfs_root>/<snapshot_dir>`. Listing, sorting, parent matching and pruning all depend on parsing this format (`internal/btrfs`), so changing it breaks the ability to find parents in existing deployments.

**Log streaming:** `main` installs `logbroadcast.Broadcaster` as the default `slog` handler. It writes to stderr and also fans each record out to subscribers, and `WatchLogs` subscribes a channel to stream `LogEntry` messages to gRPC clients.

`standalone` promotes the latest complete received snapshot (read-only with a `received_uuid`) to a writable subvolume so that a slave can take over. `btrfs.Manager.Restore` creates it next to the old subvolume and swaps it in by rename. `clear` stops replication and deletes local snapshots.
