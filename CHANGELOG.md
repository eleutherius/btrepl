# Changelog

## [0.2.1] - 2026-11-05

### Fixed

- `install.sh` stopped with "no valid build provenance attestation" when an old `gh` without the `attestation` command (such as 2.46 from Debian) was logged in. It now skips the provenance check in that case, as it does without `gh`.

## [0.2.0] - 2026-11-05

This release fixes a critical vulnerability: the gRPC API was open to the network without authentication ([GHSA-c424-8r7c-ccf3](https://github.com/eleutherius/btrepl/security/advisories/GHSA-c424-8r7c-ccf3)). Read [Upgrading from 0.1.0](#upgrading-from-010) before you upgrade.

### Security

- The daemon listens on the Unix socket `/run/btrepl/btrepl.sock`, which only root can open, instead of `:50051` without authentication. It listens on TCP only with mutual TLS (`serve --tls-cert --tls-key --tls-client-ca`). The Python client supports both.
- The master verifies slave host keys against `ssh_known_hosts`; `add-slave` records the key of a new slave (#4).
- `add-slave` restricts the master's key on the slave to `btrepl ssh-filter` from the master's address (#4, #7).
- `btrfs receive` runs with `--chroot`, and snapshot directories are mode `0700` on the master and the slaves (#7).
- Updated `golang.org/x/crypto`, `google.golang.org/grpc` and `golang.org/x/net`. Releases are built with Go 1.26.10. CI runs govulncheck.
- `install.sh` stops when it cannot verify a download. Release files carry build provenance attestations (#8).

### Added

- `ssh_port`, and slaves as `host:port` (#4).
- `btrepl ssh-filter`, a forced command for the master's key on a slave (#4).
- `max_anchor_age` (#3), `ssh_command_timeout` and `send_idle_timeout` (#7).
- SSH keepalives: a slave that stops responding no longer stalls the cycle (#7).
- A run lock: the daemon and `btrepl run` never run cycles at the same time (#4).

### Changed

- Each cycle takes one snapshot per subvolume, whether or not the slaves are reachable, and sends the newest to every slave (#2).
- The incremental parent is matched by UUID instead of by name (#1).
- The daemon picks up a changed `interval` without a restart (#4).
- `standalone` promotes only complete received snapshots, and swaps them in without deleting the old subvolume first (#1).
- `init-master` and `add-slave` require `btrfs_root` to be the top level of a btrfs filesystem.
- Missing subvolumes on the master are created before snapshotting.

### Fixed

- An interrupted receive no longer blocks replication to that slave (#1).
- Local pruning keeps the last snapshot in common with each slave, so a slave that was offline catches up with an incremental send (#3).
- `btrepl run` failed with two or more slaves when two snapshots fell into the same second.

### Upgrading from 0.1.0

1. Upgrade the master and all slaves together: `ssh-filter` on a slave accepts only the commands of its own version.
2. On the master, run `btrepl add-slave -s <host>` once for each slave. It records the slave's host key if `known_hosts` does not have it (replication refuses unknown keys now) and restricts the master's key on the slave.
3. Clients of the gRPC API: connect to the socket on the node, through an SSH tunnel to it, or over mutual TLS. Port 50051 is closed. See [gRPC API](https://eleutherius.github.io/btrepl/grpc/).

## [0.1.0] - 2026-09-29

First release.
