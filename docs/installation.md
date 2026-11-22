# Installation

## Requirements

- Linux with btrfs
- `btrfs-progs` installed on master **and** slaves
- On every node, `btrfs_root` (default `/btrfs`) must be a mount point of the **top level** of a btrfs filesystem (`subvolid=5`), not a directory on the root filesystem or a mounted subvolume. Incremental `btrfs receive` cannot find the parent snapshot otherwise. `init-master` and `add-slave` check this. Check it yourself with `findmnt -no FSTYPE,OPTIONS /btrfs`: it should print `btrfs` and `subvol=/`.
- Passwordless SSH access from master to each slave (key-based, default: `/root/.ssh/id_ed25519`). See [SSH access](ssh.md) for host keys, ports and restricting the key
- Go 1.21+ to build from source

## Install script (recommended)

```bash
curl -LsSf https://eleutherius.github.io/btrepl/install.sh | sh
```

or with `wget`:

```bash
wget -qO- https://eleutherius.github.io/btrepl/install.sh | sh
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
| `BTREPL_INSECURE_SKIP_VERIFY` | unset | Set to `1` to install even if the download cannot be verified. A checksum mismatch always stops the install |

Example: pin a version and skip the service:

```bash
curl -LsSf https://eleutherius.github.io/btrepl/install.sh \
  | BTREPL_VERSION=v0.2.0 BTREPL_NO_SERVICE=1 sh
```

To read the script before running it:

```bash
curl -LsSf https://eleutherius.github.io/btrepl/install.sh -o install.sh
less install.sh
sh install.sh
```

!!! note "RHEL, Rocky and Alma 8+"
    These distros do not ship `btrfs-progs` in their base repos, and their kernels do not support btrfs. The script prints a warning and installs `btrepl` anyway. Enable [EPEL](https://docs.fedoraproject.org/en-US/epel/) (and use a btrfs-capable kernel) before running it.

### Verification

The script checks the binary and the systemd unit against the release's `SHA256SUMS` and stops if either is missing, the checksum does not match, or neither `sha256sum` nor `shasum` is installed. When [`gh`](https://cli.github.com/) 2.49 or later is installed and logged in, it also checks the [build provenance attestation](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations): proof that the file was built by this repository's release workflow from a tagged commit.

## Verifying a download

Checksums detect a damaged download, but they come from the same release as the files. The attestation shows who built the file:

```bash
sha256sum -c --ignore-missing SHA256SUMS
gh attestation verify btrepl_linux_amd64 --repo eleutherius/btrepl
```

Attestations exist for releases from v0.2.0 on. They cover the binaries, the `.deb` and `.rpm` packages, `btrepl.service` and `install.sh`. The `install.sh` on the site is the file from the latest release, so you can check it the same way:

```bash
curl -LsSfO https://eleutherius.github.io/btrepl/install.sh
gh attestation verify install.sh --repo eleutherius/btrepl
```

## APT repository (Debian / Ubuntu)

Packages for `amd64` and `arm64` are published to a signed APT repository, so `btrepl` updates with the rest of the system:

```bash
sudo install -d -m 755 /etc/apt/keyrings
curl -fsSL https://eleutherius.github.io/btrepl/apt/btrepl.gpg \
  | sudo tee /etc/apt/keyrings/btrepl.gpg >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/btrepl.gpg] https://eleutherius.github.io/btrepl/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/btrepl.list
sudo apt update
sudo apt install btrepl
```

The package depends on `btrfs-progs` and `openssh-client`, installs the binary and `btrepl.service`, then enables and starts the service. The repository keeps every release, so you can pin an older one with `apt install btrepl=<version>`.

## .deb / .rpm files

Each [release](https://github.com/eleutherius/btrepl/releases) also contains `.deb` and `.rpm` files for `amd64` and `arm64`:

```bash
# Debian / Ubuntu
sudo apt install ./btrepl_<version>_amd64.deb

# Fedora / RHEL / openSUSE
sudo dnf install ./btrepl_<version>_amd64.rpm
```

The `.rpm` does not pull in `btrfs-progs`, because the package name differs between distros, so install it separately.

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
