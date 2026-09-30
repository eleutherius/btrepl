# Installation

## Requirements

- Linux with btrfs
- `btrfs-progs` installed on master **and** slaves
- On every node, `btrfs_root` (default `/btrfs`) must be a mount point of the **top level** of a btrfs filesystem (`subvolid=5`), not a directory on the root filesystem or a mounted subvolume. Incremental `btrfs receive` cannot find the parent snapshot otherwise. `init-master` and `add-slave` check this. Check it yourself with `findmnt -no FSTYPE,OPTIONS /btrfs`: it should print `btrfs` and `subvol=/`.
- Passwordless SSH access from master to each slave (key-based, default: `/root/.ssh/id_ed25519`)
- Go 1.21+ to build from source

## Install script (recommended)

```bash
curl -LsSf https://github.com/eleutherius/btrepl/releases/latest/download/install.sh | sh
```

or with `wget`:

```bash
wget -qO- https://github.com/eleutherius/btrepl/releases/latest/download/install.sh | sh
```

The script works on Debian/Ubuntu, Fedora/RHEL/Rocky/Alma, openSUSE/SLES, Arch and Alpine, on `amd64` and `arm64`. It:

1. installs `btrfs-progs` and an SSH client with the distro package manager (`apt`, `dnf`/`yum`, `zypper`, `pacman` or `apk`);
2. downloads the `btrepl` binary from the GitHub release and verifies it against `SHA256SUMS`;
3. installs it to `/usr/local/bin/btrepl`;
4. on systemd hosts, installs `btrepl.service` and enables and starts it (daemon mode).

It needs root. When run as a regular user it calls `sudo` (or `doas`).

Options are passed as environment variables:

| Variable | Default | Description |
|---|---|---|
| `BTREPL_VERSION` | `latest` | Release tag to install, e.g. `v0.2.0` |
| `BTREPL_INSTALL_DIR` | `/usr/local/bin` | Where to put the binary |
| `BTREPL_NO_DEPS` | unset | Set to `1` to skip installing `btrfs-progs` / SSH client |
| `BTREPL_NO_SERVICE` | unset | Set to `1` to skip installing the systemd unit |

Example: pin a version and skip the service:

```bash
curl -LsSf https://github.com/eleutherius/btrepl/releases/latest/download/install.sh \
  | BTREPL_VERSION=v0.2.0 BTREPL_NO_SERVICE=1 sh
```

To read the script before running it:

```bash
curl -LsSf https://github.com/eleutherius/btrepl/releases/latest/download/install.sh -o install.sh
less install.sh
sh install.sh
```

!!! note "RHEL, Rocky and Alma 8+"
    These distros do not ship `btrfs-progs` in their base repos, and their kernels do not support btrfs. The script prints a warning and installs `btrepl` anyway. Enable [EPEL](https://docs.fedoraproject.org/en-US/epel/) (and use a btrfs-capable kernel) before running it.

## .deb / .rpm packages

Each [release](https://github.com/eleutherius/btrepl/releases) also contains `.deb` and `.rpm` packages for `amd64` and `arm64`. They install the binary and `btrepl.service`, then enable and start the service:

```bash
# Debian / Ubuntu
sudo apt install ./btrepl_<version>_amd64.deb

# Fedora / RHEL / openSUSE
sudo dnf install ./btrepl_<version>_amd64.rpm
```

The packages do not pull in `btrfs-progs`, so install it separately.

## Build from source

Requires Go 1.21+. The Go module lives in `src/btrepl/`:

```bash
git clone https://github.com/eleutherius/btrepl
cd btrepl
go -C src/btrepl build -o /usr/local/bin/btrepl ./cmd/btrepl

# or build release binaries and packages into build/
make packages
```

Next: [Getting started](getting-started.md).
