#!/bin/sh
# Build a signed APT repository from a directory of .deb packages.
#
#   scripts/build-apt-repo.sh <deb-dir> <out-dir>
#
# Layout of <out-dir>:
#   pool/main/b/btrepl/*.deb
#   dists/stable/{Release,Release.gpg,InRelease}
#   dists/stable/main/binary-<arch>/Packages{,.gz}
#   btrepl.gpg   public key, binary keyring for signed-by=
#   btrepl.asc   public key, ASCII-armored
#
# Signs with the GPG key APT_GPG_KEY_ID, or the only secret key in the keyring.
# APT_GPG_PASSPHRASE is used when the key has a passphrase.
# Needs apt-ftparchive (apt-utils) and gpg.

set -eu

DEB_DIR="${1:?usage: build-apt-repo.sh <deb-dir> <out-dir>}"
OUT="${2:?usage: build-apt-repo.sh <deb-dir> <out-dir>}"
SUITE="stable"
COMPONENT="main"
ARCHS="amd64 arm64"

err() { printf 'build-apt-repo: error: %s\n' "$*" >&2; exit 1; }

command -v apt-ftparchive >/dev/null || err "apt-ftparchive not found (install apt-utils)"
command -v gpg >/dev/null || err "gpg not found"

KEY="${APT_GPG_KEY_ID:-}"
if [ -z "$KEY" ]; then
    KEY=$(gpg --batch --list-secret-keys --with-colons | awk -F: '$1 == "sec" { print $5 }')
    [ "$(printf '%s\n' "$KEY" | grep -c .)" = "1" ] \
        || err "set APT_GPG_KEY_ID: expected exactly one secret key in the keyring"
fi

set -- "$DEB_DIR"/*.deb
[ -e "$1" ] || err "no .deb files in $DEB_DIR"

rm -rf "$OUT"
mkdir -p "$OUT/pool/$COMPONENT/b/btrepl"
cp "$@" "$OUT/pool/$COMPONENT/b/btrepl/"

cd "$OUT"
for arch in $ARCHS; do
    dir="dists/$SUITE/$COMPONENT/binary-$arch"
    mkdir -p "$dir"
    apt-ftparchive --arch "$arch" packages pool > "$dir/Packages"
    gzip -9n < "$dir/Packages" > "$dir/Packages.gz"
done

# shellcheck disable=SC2086 # ARCHS is a space-separated list on purpose
apt-ftparchive \
    -o APT::FTPArchive::Release::Origin=btrepl \
    -o APT::FTPArchive::Release::Label=btrepl \
    -o APT::FTPArchive::Release::Suite="$SUITE" \
    -o APT::FTPArchive::Release::Codename="$SUITE" \
    -o APT::FTPArchive::Release::Architectures="$ARCHS" \
    -o APT::FTPArchive::Release::Components="$COMPONENT" \
    -o APT::FTPArchive::Release::Description="btrepl packages" \
    release "dists/$SUITE" > "dists/$SUITE/Release.tmp"
mv "dists/$SUITE/Release.tmp" "dists/$SUITE/Release"

sign() {
    if [ -n "${APT_GPG_PASSPHRASE:-}" ]; then
        printf '%s' "$APT_GPG_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback \
            --passphrase-fd 0 --local-user "$KEY" "$@"
    else
        gpg --batch --yes --local-user "$KEY" "$@"
    fi
}
sign --clearsign -o "dists/$SUITE/InRelease" "dists/$SUITE/Release"
sign --armor --detach-sign -o "dists/$SUITE/Release.gpg" "dists/$SUITE/Release"

gpg --batch --export "$KEY" > btrepl.gpg
gpg --batch --armor --export "$KEY" > btrepl.asc

printf 'build-apt-repo: %s packages, signed with %s\n' "$#" "$KEY"
