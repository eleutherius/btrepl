"""
Helpers shared by the replication tests: read snapshots from btrfs, run a
cycle, parse the master's log, compare directory trees.
"""

import re
from dataclasses import dataclass

from conftest import BTRFS_MNT, SNAP_DIR, SNAP_PREFIX, rem

CONFIG = "/etc/btrepl/config.yaml"
SENDING = re.compile(r'msg=sending snap=(\S+) parent=("[^"]*"|\S+) slave=(\S+)')


@dataclass
class Snap:
    name: str
    uuid: str
    received_uuid: str
    read_only: bool

    @property
    def path(self) -> str:
        return f"{BTRFS_MNT}/{SNAP_DIR}/{self.name}"

    @property
    def received(self) -> bool:
        return self.read_only and self.received_uuid != ""


def snaps(machine: str, subvol: str) -> list[Snap]:
    """Snapshots of subvol on machine, oldest first, read from btrfs."""
    ro = {
        line.split(" path ", 1)[1]
        for line in rem(machine, f"btrfs subvolume list -r {BTRFS_MNT}").stdout.splitlines()
        if " path " in line
    }
    prefix = f"{SNAP_DIR}/{SNAP_PREFIX}{subvol.lstrip('@')}_"
    result = []
    for line in rem(machine, f"btrfs subvolume list -u -R {BTRFS_MNT}").stdout.splitlines():
        head, _, path = line.partition(" path ")
        if not path.startswith(prefix):
            continue
        fields = head.split()
        ruuid = fields[fields.index("received_uuid") + 1]
        result.append(Snap(
            name=path.rsplit("/", 1)[1],
            uuid=fields[fields.index("uuid") + 1],
            received_uuid="" if ruuid == "-" else ruuid,
            read_only=path in ro,
        ))
    return sorted(result, key=lambda s: s.name)


def run_cycle(master_cli, *, expect_ok: bool = True) -> str:
    r = master_cli("run", check=False)
    out = r.stdout + r.stderr
    if expect_ok:
        assert r.returncode == 0, f"btrepl run failed:\n{out}"
    return out


def sends(log: str, slave_ip: str) -> dict[str, str]:
    """snapshot name -> parent from the 'sending' log lines for one slave."""
    return {m[1]: m[2].strip('"') for m in SENDING.finditer(log) if m[3] == slave_ip}


def manifest(machine: str, path: str) -> str:
    """
    Everything btrfs send must carry over, for each entry under path: type,
    mode, owner, link count, symlink target, and for files size, mtime and
    sha256. Two trees with equal manifests hold the same data.
    """
    return rem(machine, f"""
        set -e
        cd {path}
        find . -mindepth 1 \\( -type f -printf '%y %m %U:%G %n %s %T@ %p\\n' \\) \\
             -o \\( -printf '%y %m %U:%G %p -> %l\\n' \\) | LC_ALL=C sort
        find . -type f -print0 | LC_ALL=C sort -z | xargs -0r sha256sum
    """).stdout
