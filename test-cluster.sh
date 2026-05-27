#!/usr/bin/env bash
# Spin up a two-node btrepl cluster in OrbStack for local testing.
# Usage: ./test-cluster.sh [--clean]
#   --clean  delete btrepl1 and btrepl2 before starting
set -euo pipefail

SOURCE_MACHINE="debian-arm-13"
MASTER="btrepl1"
SLAVE="btrepl2"
BTRFS_IMG="/btrfs.img"
BTRFS_MNT="/btrfs"
SSH_KEY="/root/.ssh/id_ed25519"
SUBVOLS=("@eventsPictures" "@reports")
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── helpers ──────────────────────────────────────────────────────────────────

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
log()  { printf "${GREEN}==> %s${NC}\n" "$*"; }
warn() { printf "${YELLOW}warn: %s${NC}\n" "$*"; }
die()  { printf "${RED}error: %s${NC}\n" "$*" >&2; exit 1; }

machine_exists() {
    orbctl list 2>/dev/null | awk '{print $1}' | grep -qx "$1"
}

# Run a bash command on a machine as root.
rem() { # rem <machine> <bash-command>
    orbctl run -m "$1" -u root bash -c "$2"
}

# Push a local file to a remote path.
push() { # push <machine> <local> <remote>
    orbctl push -m "$1" "$2" "$3"
}

# Push a heredoc string as a script and execute it.
push_run() { # push_run <machine> <<'EOF' ... EOF
    local machine=$1 content
    content=$(cat)
    local tmp; tmp=$(mktemp /tmp/btrepl-XXXXXX.sh)
    printf '%s\n' "$content" > "$tmp"
    push "$machine" "$tmp" /tmp/_btrepl_setup.sh
    rm -f "$tmp"
    orbctl run -m "$machine" -u root bash /tmp/_btrepl_setup.sh
}

wait_ssh() {
    local machine=$1
    log "Waiting for $machine..."
    for i in $(seq 1 45); do
        orbctl run -m "$machine" -u root echo ok >/dev/null 2>&1 && return
        sleep 2
    done
    die "$machine not reachable after 90s"
}

# ── preflight ─────────────────────────────────────────────────────────────────

DEB=$(ls "$SCRIPT_DIR"/build/*arm64.deb 2>/dev/null | sort -V | tail -1 || true)
[[ -z "$DEB" ]] && die "No arm64 .deb in build/. Run: make deb"
log "Package: $DEB"

# ── --clean ───────────────────────────────────────────────────────────────────

if [[ "${1:-}" == "--clean" ]]; then
    for m in "$MASTER" "$SLAVE"; do
        if machine_exists "$m"; then
            log "Deleting $m..."
            orbctl delete -f "$m"
        fi
    done
fi

# ── 1. clone ──────────────────────────────────────────────────────────────────

for m in "$MASTER" "$SLAVE"; do
    if machine_exists "$m"; then
        warn "$m already exists — skipping clone"
    else
        log "Cloning $SOURCE_MACHINE → $m ..."
        orbctl clone "$SOURCE_MACHINE" "$m"
    fi
done

# ── 2. start ──────────────────────────────────────────────────────────────────

log "Starting machines..."
orbctl start "$MASTER" 2>/dev/null || true
orbctl start "$SLAVE"  2>/dev/null || true
wait_ssh "$MASTER"
wait_ssh "$SLAVE"

# ── 3. btrfs + btrepl on both nodes ──────────────────────────────────────────

SUBVOL_LIST=$(printf ' %s' "${SUBVOLS[@]}")

for m in "$MASTER" "$SLAVE"; do
    log "[$m] Setting up btrfs..."
    push_run "$m" << EOF
#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq btrfs-progs

if [ ! -f "$BTRFS_IMG" ]; then
    fallocate -l 2G "$BTRFS_IMG"
    mkfs.btrfs "$BTRFS_IMG"
fi

mkdir -p "$BTRFS_MNT"
if ! mountpoint -q "$BTRFS_MNT"; then
    mount -o loop "$BTRFS_IMG" "$BTRFS_MNT"
fi

grep -q "$BTRFS_IMG" /etc/fstab || \
    echo "$BTRFS_IMG $BTRFS_MNT btrfs loop,nofail 0 0" >> /etc/fstab

for sv in $SUBVOL_LIST; do
    btrfs subvolume show "$BTRFS_MNT/\$sv" &>/dev/null || \
        btrfs subvolume create "$BTRFS_MNT/\$sv"
done
echo "btrfs OK"
EOF

    log "[$m] Installing btrepl..."
    push "$m" "$DEB" /tmp/btrepl.deb
    rem "$m" "dpkg -i /tmp/btrepl.deb || apt-get install -f -y -qq"
    rem "$m" "systemctl stop btrepl.service 2>/dev/null || true"
done

# ── 4. write configs ──────────────────────────────────────────────────────────

write_config() {
    local machine=$1
    local tmp; tmp=$(mktemp)
    cat > "$tmp" << YAML
btrfs_root: $BTRFS_MNT
ssh_identity: $SSH_KEY
ssh_user: root
snapshot_dir: .snapshots
snapshot_prefix: btrepl_
interval: 1h
keep_sender: 10
keep_receiver: 5
subvolumes:
$(printf '  - "%s"\n' "${SUBVOLS[@]}")
slaves: []
YAML
    push "$machine" "$tmp" /tmp/btrepl_config.yaml
    rem "$machine" "mkdir -p /etc/btrepl && mv /tmp/btrepl_config.yaml /etc/btrepl/config.yaml"
    rm -f "$tmp"
    log "[$machine] Config written"
}

write_config "$MASTER"
write_config "$SLAVE"

# ── 5. SSH key: master → slave ────────────────────────────────────────────────

log "[$MASTER] Generating SSH key..."
rem "$MASTER" "
    mkdir -p /root/.ssh && chmod 700 /root/.ssh
    [ -f $SSH_KEY ] || ssh-keygen -t ed25519 -N '' -f $SSH_KEY -q
"

PUBKEY=$(rem "$MASTER" "cat ${SSH_KEY}.pub")

log "[$SLAVE] Authorizing master's key..."
TMPKEY=$(mktemp)
echo "$PUBKEY" > "$TMPKEY"
push "$SLAVE" "$TMPKEY" /tmp/_master.pub
rm -f "$TMPKEY"
rem "$SLAVE" "
    mkdir -p /root/.ssh && chmod 700 /root/.ssh
    touch /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys
    cat /tmp/_master.pub >> /root/.ssh/authorized_keys
    sort -u /root/.ssh/authorized_keys -o /root/.ssh/authorized_keys
"

SLAVE_IP=$(rem "$SLAVE" "hostname -I | awk '{print \$1}'")
log "Slave IP: $SLAVE_IP"

rem "$MASTER" "ssh-keyscan -H '$SLAVE_IP' >> /root/.ssh/known_hosts 2>/dev/null || true"

# ── 6. init master, add slave ─────────────────────────────────────────────────

log "[$MASTER] Initializing master..."
rem "$MASTER" "btrepl init-master"

log "[$MASTER] Adding slave $SLAVE_IP..."
rem "$MASTER" "btrepl add-slave -s '$SLAVE_IP'"

# ── 7. start services ─────────────────────────────────────────────────────────

log "Starting btrepl services..."
for m in "$MASTER" "$SLAVE"; do
    rem "$m" "systemctl daemon-reload && systemctl enable btrepl.service && systemctl start btrepl.service"
    rem "$m" "systemctl is-active btrepl.service"
done

# ── done ──────────────────────────────────────────────────────────────────────

MASTER_IP=$(rem "$MASTER" "hostname -I | awk '{print \$1}'")

printf "\n${GREEN}Cluster is up!${NC}\n"
printf "  master: %-10s  %s  (gRPC :50051)\n" "$MASTER" "$MASTER_IP"
printf "  slave:  %-10s  %s  (gRPC :50051)\n" "$SLAVE"  "$SLAVE_IP"
printf "\nUseful commands:\n"
printf "  status:     orbctl run -m %s -u root btrepl status\n" "$MASTER"
printf "  run cycle:  orbctl run -m %s -u root btrepl run\n"    "$MASTER"
printf "  logs:       orbctl run -m %s -u root journalctl -u btrepl -f\n" "$MASTER"
printf "  teardown:   %s --clean\n" "$0"
