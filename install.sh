#!/bin/sh
# btrepl installer.
#
#   curl -LsSf https://github.com/eleutherius/btrepl/releases/latest/download/install.sh | sh
#
# Environment variables:
#   BTREPL_VERSION      release tag to install (default: latest), e.g. v0.2.0
#   BTREPL_INSTALL_DIR  where to put the binary (default: /usr/local/bin)
#   BTREPL_NO_DEPS=1    do not install btrfs-progs / openssh-client
#   BTREPL_NO_SERVICE=1 do not install/enable the systemd unit
#   BTREPL_INSECURE_SKIP_VERIFY=1
#                       install even if the download cannot be verified
#                       (no SHA256SUMS, no sha256 tool, no provenance)
#
# Downloads are checked against the release's SHA256SUMS, and against its
# build provenance attestation when an authenticated `gh` is available.
#
# Supports Debian/Ubuntu, RHEL/Fedora/Rocky/Alma, openSUSE/SLES, Arch, Alpine
# on amd64 and arm64.

set -eu

REPO="eleutherius/btrepl"
BINARY="btrepl"
VERSION="${BTREPL_VERSION:-latest}"
INSTALL_DIR="${BTREPL_INSTALL_DIR:-/usr/local/bin}"
INSECURE="${BTREPL_INSECURE_SKIP_VERIFY:-0}"
UNIT_DIR="/etc/systemd/system"

say() { printf 'btrepl-installer: %s\n' "$*"; }
err() { printf 'btrepl-installer: error: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# ── privileges ────────────────────────────────────────────────────────────────

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if has sudo; then
        SUDO="sudo"
    elif has doas; then
        SUDO="doas"
    else
        err "run as root or install sudo/doas"
    fi
fi

# ── platform detection ────────────────────────────────────────────────────────

[ "$(uname -s)" = "Linux" ] || err "only Linux is supported (got $(uname -s))"

case "$(uname -m)" in
    x86_64 | amd64) ARCH="amd64" ;;
    aarch64 | arm64) ARCH="arm64" ;;
    *) err "unsupported architecture: $(uname -m)" ;;
esac

# Source os-release in a subshell: it defines VERSION, which would clobber ours.
os_release() {
    # shellcheck disable=SC1091
    [ -r /etc/os-release ] && (. /etc/os-release && eval "printf '%s' \"\${$1:-}\"")
}
DISTRO_ID="$(os_release ID || true)"
DISTRO_LIKE="$(os_release ID_LIKE || true)"

detect_pkg_manager() {
    for id in $DISTRO_ID $DISTRO_LIKE; do
        case "$id" in
            debian | ubuntu) echo apt; return ;;
            fedora | rhel | centos | rocky | almalinux | ol | amzn)
                if has dnf; then echo dnf; else echo yum; fi
                return ;;
            opensuse* | sles | suse) echo zypper; return ;;
            arch | manjaro | endeavouros) echo pacman; return ;;
            alpine) echo apk; return ;;
        esac
    done
    # Unknown or missing os-release: fall back to whatever is on PATH.
    for pm in apt-get dnf yum zypper pacman apk; do
        if has "$pm"; then
            [ "$pm" = "apt-get" ] && pm="apt"
            echo "$pm"
            return
        fi
    done
    echo none
}

PKG_MANAGER="$(detect_pkg_manager)"

# ── dependencies ──────────────────────────────────────────────────────────────

install_deps() {
    need=""
    has btrfs || need="$need btrfs"
    has ssh || need="$need ssh"
    if [ -z "$need" ]; then
        return
    fi

    say "installing dependencies via $PKG_MANAGER:$need"
    # A missing package (e.g. btrfs-progs on RHEL/Rocky/Alma 8+, where it lives
    # in EPEL) should not stop the binary install, so only warn on failure.
    install_deps_with_pm || say "warning: failed to install dependencies; install btrfs-progs and an ssh client manually (RHEL-based: enable EPEL first)"
}

install_deps_with_pm() {
    case "$PKG_MANAGER" in
        apt)
            $SUDO env DEBIAN_FRONTEND=noninteractive apt-get update -qq
            $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq btrfs-progs openssh-client
            ;;
        dnf) $SUDO dnf install -y -q btrfs-progs openssh-clients ;;
        yum) $SUDO yum install -y -q btrfs-progs openssh-clients ;;
        zypper) $SUDO zypper --non-interactive install btrfsprogs openssh-clients ;;
        pacman) $SUDO pacman -Sy --noconfirm --needed btrfs-progs openssh ;;
        apk) $SUDO apk add --no-cache btrfs-progs openssh-client ;;
        *) say "warning: unknown package manager, install btrfs-progs and an ssh client manually" ;;
    esac
}

# ── download ──────────────────────────────────────────────────────────────────

download() {
    # download <url> <dest>
    if has curl; then
        curl --proto '=https' --tlsv1.2 -fLsS "$1" -o "$2"
    elif has wget; then
        wget -q --https-only "$1" -O "$2"
    else
        err "curl or wget is required"
    fi
}

if [ "$VERSION" = "latest" ]; then
    BASE_URL="https://github.com/$REPO/releases/latest/download"
else
    BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

ASSET="${BINARY}_linux_${ARCH}"

# ── verification ──────────────────────────────────────────────────────────────

unverified() {
    # unverified <reason>: stop, unless the user opted out of verification
    if [ "$INSECURE" = "1" ]; then
        say "warning: $1; installing anyway because BTREPL_INSECURE_SKIP_VERIFY=1"
    else
        err "$1 (set BTREPL_INSECURE_SKIP_VERIFY=1 to install without verification)"
    fi
}

sha256_check() {
    # sha256_check: verify the "hash  file" lines on stdin, in the current dir
    if has sha256sum; then
        sha256sum -c - >/dev/null
    else
        shasum -a 256 -c - >/dev/null
    fi
}

SUMS_OK=0
fetch_sums() {
    if ! download "$BASE_URL/SHA256SUMS" "$TMP/SHA256SUMS" 2>/dev/null; then
        unverified "SHA256SUMS not found in the release"
        return
    fi
    if ! has sha256sum && ! has shasum; then
        unverified "neither sha256sum nor shasum is installed"
        return
    fi
    SUMS_OK=1
}

USE_GH=0
if has gh && gh auth status >/dev/null 2>&1; then
    USE_GH=1
fi

verify() {
    # verify <file in $TMP>: checksum and build provenance
    if [ "$SUMS_OK" = "1" ]; then
        line=$(grep " $1\$" "$TMP/SHA256SUMS" || true)
        if [ -z "$line" ]; then
            unverified "$1 is not listed in SHA256SUMS"
        else
            (cd "$TMP" && printf '%s\n' "$line" | sha256_check) || err "checksum mismatch for $1"
            say "checksum OK: $1"
        fi
    fi
    if [ "$USE_GH" = "1" ]; then
        if gh attestation verify "$TMP/$1" --repo "$REPO" >/dev/null 2>&1; then
            say "build provenance OK: $1"
        else
            unverified "no valid build provenance attestation for $1 from $REPO"
        fi
    fi
}

main() {
    say "platform: linux/$ARCH, distro: ${DISTRO_ID:-unknown}, package manager: $PKG_MANAGER"

    if [ "${BTREPL_NO_DEPS:-0}" != "1" ]; then
        install_deps
    fi

    say "downloading $ASSET ($VERSION)"
    download "$BASE_URL/$ASSET" "$TMP/$ASSET" || err "failed to download $BASE_URL/$ASSET"

    fetch_sums
    if [ "$USE_GH" = "0" ]; then
        say "gh is not installed or not logged in: skipping the build provenance check"
    fi
    verify "$ASSET"

    # Download and verify everything before installing anything.
    service=0
    if [ "${BTREPL_NO_SERVICE:-0}" != "1" ]; then
        if has_systemd; then
            fetch_service
            service=1
        else
            say "systemd not detected, skipping service install"
        fi
    fi

    $SUDO mkdir -p "$INSTALL_DIR"
    $SUDO install -m 755 "$TMP/$ASSET" "$INSTALL_DIR/$BINARY"
    say "installed $INSTALL_DIR/$BINARY"

    if [ "$service" = "1" ]; then
        install_service
    fi

    say "done. Next: $BINARY init-master  (see https://github.com/$REPO#quick-start)"
}

has_systemd() { has systemctl && [ -d /run/systemd/system ]; }

fetch_service() {
    download "$BASE_URL/$BINARY.service" "$TMP/$BINARY.service" \
        || err "failed to download $BINARY.service"
    verify "$BINARY.service"
    # The unit hardcodes /usr/local/bin; follow a custom install dir.
    sed "s|/usr/local/bin/$BINARY|$INSTALL_DIR/$BINARY|" "$TMP/$BINARY.service" > "$TMP/$BINARY.service.out"
}

install_service() {
    $SUDO install -m 644 "$TMP/$BINARY.service.out" "$UNIT_DIR/$BINARY.service"
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable "$BINARY.service"
    $SUDO systemctl restart "$BINARY.service"
    say "systemd unit $BINARY.service enabled and started"
}

main
