#!/bin/sh
# Build a signed RPM (dnf/yum/zypper) repository from a directory of .rpm packages.
#
#   scripts/build-rpm-repo.sh <rpm-dir> <out-dir> <base-url>
#
# Layout of <out-dir>:
#   packages/*.rpm
#   repodata/repomd.xml{,.asc}, repodata/*-primary.xml.gz, ...
#   btrepl.repo   repo file for /etc/yum.repos.d and zypper
#   btrepl.asc    public key, ASCII-armored
#
# The repository metadata is signed (repo_gpgcheck=1). It lists the sha256 of
# every package, so dnf and zypper verify each package through it. The
# packages themselves are not signed: older rpm versions cannot verify
# Ed25519 package signatures.
#
# Signs with the GPG key APT_GPG_KEY_ID, or the only secret key in the keyring.
# APT_GPG_PASSPHRASE is used when the key has a passphrase.
# Needs createrepo_c and gpg.

set -eu

RPM_DIR="${1:?usage: build-rpm-repo.sh <rpm-dir> <out-dir> <base-url>}"
OUT="${2:?usage: build-rpm-repo.sh <rpm-dir> <out-dir> <base-url>}"
BASE_URL="${3:?usage: build-rpm-repo.sh <rpm-dir> <out-dir> <base-url>}"
BASE_URL="${BASE_URL%/}"

err() { printf 'build-rpm-repo: error: %s\n' "$*" >&2; exit 1; }

command -v createrepo_c >/dev/null || err "createrepo_c not found (install createrepo-c)"
command -v gpg >/dev/null || err "gpg not found"

KEY="${APT_GPG_KEY_ID:-}"
if [ -z "$KEY" ]; then
    KEY=$(gpg --batch --list-secret-keys --with-colons | awk -F: '$1 == "sec" { print $5 }')
    [ "$(printf '%s\n' "$KEY" | grep -c .)" = "1" ] \
        || err "set APT_GPG_KEY_ID: expected exactly one secret key in the keyring"
fi

set -- "$RPM_DIR"/*.rpm
[ -e "$1" ] || err "no .rpm files in $RPM_DIR"

rm -rf "$OUT"
mkdir -p "$OUT/packages"
cp "$@" "$OUT/packages/"

cd "$OUT"
createrepo_c --quiet --unique-md-filenames --simple-md-filenames .

if [ -n "${APT_GPG_PASSPHRASE:-}" ]; then
    printf '%s' "$APT_GPG_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback \
        --passphrase-fd 0 --local-user "$KEY" \
        --armor --detach-sign -o repodata/repomd.xml.asc repodata/repomd.xml
else
    gpg --batch --yes --local-user "$KEY" \
        --armor --detach-sign -o repodata/repomd.xml.asc repodata/repomd.xml
fi

gpg --batch --armor --export "$KEY" > btrepl.asc

cat > btrepl.repo <<EOF
[btrepl]
name=btrepl
baseurl=$BASE_URL
enabled=1
repo_gpgcheck=1
gpgcheck=0
gpgkey=$BASE_URL/btrepl.asc
type=rpm-md
EOF

printf 'build-rpm-repo: %s packages, signed with %s\n' "$#" "$KEY"
