"""
Install tests on fresh instances of several distros. They do not use the
cluster.

--distros   installs the packages from build/ (make packages): the package
            installs and starts btrepl.service, the .deb pulls in its
            dependencies, removing the package stops and removes everything.
--channels  installs what users get: install.sh from the site, the APT and RPM
            repositories, and an APT upgrade from the oldest release to the
            newest.

    uv run pytest tests/integration/test_install.py -v --distros
    uv run pytest tests/integration/test_install.py -v --channels

BTREPL_TEST_DISTROS overrides the distro list: comma-separated images, each
optionally prefixed with its package family (deb, dnf, zypper, pacman, apk),
e.g. "images:debian/13,dnf=images:almalinux/9". Instances are named
btrepl-t-<n> and deleted after their tests.
"""

import json
import os
import re
import subprocess
import time
import urllib.request
from dataclasses import dataclass
from pathlib import Path

import pytest

from conftest import REPO_ROOT, incus, launch, push, ref

SITE = "https://eleutherius.github.io/btrepl"
FAMILIES = {
    "debian": "deb", "ubuntu": "deb",
    "fedora": "dnf", "rocky": "dnf", "alma": "dnf", "centos": "dnf",
    "opensuse": "zypper", "sles": "zypper",
    "arch": "pacman", "alpine": "apk",
}
PACKAGE_DISTROS = [
    "images:debian/13", "images:ubuntu/24.04", "images:fedora/42",
    "images:rockylinux/9", "images:opensuse/15.6",
]
CHANNEL_DISTROS = PACKAGE_DISTROS + ["images:archlinux", "images:alpine/3.22"]


@dataclass
class Distro:
    image: str
    family: str
    name: str

    def __str__(self) -> str:
        return self.image


def distros(default: list[str]) -> list[Distro]:
    entries = [e.strip() for e in os.environ.get("BTREPL_TEST_DISTROS", "").split(",") if e.strip()]
    result = []
    for i, entry in enumerate(entries or default):
        family, _, image = entry.rpartition("=")
        if not family:
            family = next((f for key, f in FAMILIES.items() if key in image), "")
        if not family:
            raise ValueError(f"cannot tell the package family of {image}: write family=image")
        result.append(Distro(image=image, family=family, name=f"btrepl-t-{i}"))
    return result


def sh(machine: str, cmd: str, *, check: bool = True) -> subprocess.CompletedProcess:
    """Run a POSIX sh command; not every distro has bash."""
    r = subprocess.run(["incus", "exec", ref(machine), "--", "sh", "-c", cmd],
                       capture_output=True, text=True)
    if check and r.returncode != 0:
        raise AssertionError(f"[{machine}] {cmd}\nrc={r.returncode}\n{r.stdout}\n{r.stderr}")
    return r


def has_systemd(machine: str) -> bool:
    return sh(machine, "test -d /run/systemd/system", check=False).returncode == 0


def wait_booted(machine: str, timeout: float = 300.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = sh(machine, "ip -4 -o addr show scope global 2>/dev/null | grep -q inet "
                        "&& { ! command -v systemctl >/dev/null || systemctl is-system-running --wait >/dev/null; true; }",
               check=False)
        if r.returncode == 0:
            return
        time.sleep(2)
    raise TimeoutError(f"{machine} not ready after {timeout:.0f} s")


INSTALL = {
    "deb": "export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq {pkgs}",
    "dnf": "dnf install -y -q {pkgs}",
    "zypper": "zypper --non-interactive --quiet install {pkgs}",
    "pacman": "pacman -Sy --noconfirm --needed {pkgs}",
    "apk": "apk add --no-cache {pkgs}",
}


def ensure(machine: str, family: str, *pkgs: str) -> None:
    sh(machine, INSTALL[family].format(pkgs=" ".join(pkgs)))


def ensure_curl(machine: str, family: str) -> None:
    # Rocky and Alma ship curl-minimal, which conflicts with the curl package.
    if sh(machine, "command -v curl", check=False).returncode != 0:
        ensure(machine, family, "curl")


@pytest.fixture
def instance(request):
    """A fresh, booted instance of the distro the test is parametrized with."""
    distro: Distro = request.param
    incus("delete", "--force", ref(distro.name), check=False)
    launch(distro.name, distro.image)
    try:
        wait_booted(distro.name)
        yield distro
    finally:
        incus("delete", "--force", ref(distro.name), check=False)


def arch(machine: str) -> str:
    m = sh(machine, "uname -m").stdout.strip()
    return {"x86_64": "amd64", "aarch64": "arm64"}[m]


def local_package(ext: str, arch_: str) -> Path:
    files = sorted(REPO_ROOT.glob(f"build/*_{arch_}.{ext}"), key=lambda p: p.stat().st_mtime)
    if not files:
        pytest.fail(f"no {arch_} .{ext} in build/ — run: make packages")
    return files[-1]


def package_version(machine: str, family: str) -> str:
    q = "dpkg-query -W -f '${Version}' btrepl" if family == "deb" else "rpm -q --qf '%{VERSION}' btrepl"
    return sh(machine, q).stdout.strip()


def assert_service_running(machine: str) -> None:
    assert sh(machine, "systemctl is-enabled btrepl.service").stdout.strip() == "enabled"
    assert sh(machine, "systemctl is-active btrepl.service", check=False).stdout.strip() == "active"


# ── packages from build/ ──────────────────────────────────────────────────────


@pytest.mark.distros
@pytest.mark.parametrize("instance", [d for d in distros(PACKAGE_DISTROS) if d.family in ("deb", "dnf", "zypper")],
                         indirect=True, ids=str)
def test_local_package(instance: Distro):
    m, family = instance.name, instance.family
    ext = "deb" if family == "deb" else "rpm"
    pkg = local_package(ext, arch(m))
    push(m, pkg, f"/tmp/btrepl.{ext}")

    if family == "deb":
        ensure(m, "deb", f"/tmp/btrepl.{ext}")
        assert sh(m, "command -v btrfs && command -v ssh", check=False).returncode == 0, \
            "the .deb did not pull in btrfs-progs and openssh-client"
    elif family == "dnf":
        sh(m, f"dnf install -y -q /tmp/btrepl.{ext}")
    else:
        sh(m, f"zypper --non-interactive --quiet install --allow-unsigned-rpm /tmp/btrepl.{ext}")

    sh(m, "btrepl --help")
    version = package_version(m, family)
    out = sh(m, "btrepl --version", check=False)
    assert version in out.stdout, f"btrepl --version does not print {version}: {out.stdout}{out.stderr}"
    assert_service_running(m)

    remove = {"deb": "apt-get remove -y -qq btrepl", "dnf": "dnf remove -y -q btrepl",
              "zypper": "zypper --non-interactive --quiet remove btrepl"}[family]
    sh(m, remove)
    assert sh(m, "command -v btrepl", check=False).returncode != 0
    assert sh(m, "systemctl is-active btrepl.service", check=False).stdout.strip() != "active"


# ── published channels ────────────────────────────────────────────────────────


@pytest.mark.channels
@pytest.mark.parametrize("instance", distros(CHANNEL_DISTROS), indirect=True, ids=str)
def test_install_script(instance: Distro):
    m = instance.name
    ensure_curl(m, instance.family)
    sh(m, f"curl -LsSf {SITE}/install.sh | sh")
    sh(m, "btrepl --help")
    if has_systemd(m):
        assert_service_running(m)


def apt_repo_setup(m: str) -> None:
    ensure(m, "deb", "ca-certificates")
    ensure_curl(m, "deb")
    sh(m, f"""
        install -d -m 755 /etc/apt/keyrings
        curl -fsSL {SITE}/apt/btrepl.gpg > /etc/apt/keyrings/btrepl.gpg
        echo "deb [signed-by=/etc/apt/keyrings/btrepl.gpg] {SITE}/apt stable main" \\
            > /etc/apt/sources.list.d/btrepl.list
        apt-get update -qq
    """)


def apt_versions(m: str) -> list[str]:
    out = sh(m, "apt-cache madison btrepl").stdout
    return [line.split("|")[1].strip() for line in out.splitlines() if "|" in line]


@pytest.mark.channels
@pytest.mark.parametrize("instance", [d for d in distros(CHANNEL_DISTROS) if d.family == "deb"],
                         indirect=True, ids=str)
def test_apt_repository(instance: Distro):
    m = instance.name
    apt_repo_setup(m)
    versions = apt_versions(m)
    assert versions, "the APT repository has no btrepl"
    sh(m, "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq btrepl")
    assert package_version(m, "deb") == versions[0]
    assert_service_running(m)


@pytest.mark.channels
@pytest.mark.parametrize("instance", [d for d in distros(CHANNEL_DISTROS) if d.family == "deb"][:1],
                         indirect=True, ids=str)
def test_apt_upgrade_from_oldest_release(instance: Distro):
    m = instance.name
    apt_repo_setup(m)
    versions = apt_versions(m)
    if len(versions) < 2:
        pytest.skip("the APT repository has only one version")
    sh(m, f"DEBIAN_FRONTEND=noninteractive apt-get install -y -qq btrepl={versions[-1]}")
    assert package_version(m, "deb") == versions[-1]
    sh(m, "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --only-upgrade btrepl")
    assert package_version(m, "deb") == versions[0]
    assert_service_running(m)


@pytest.mark.channels
@pytest.mark.parametrize("instance", [d for d in distros(CHANNEL_DISTROS) if d.family in ("dnf", "zypper")],
                         indirect=True, ids=str)
def test_rpm_repository(instance: Distro):
    m = instance.name
    if instance.family == "dnf":
        ensure_curl(m, "dnf")
        sh(m, f"curl -fsSL {SITE}/rpm/btrepl.repo -o /etc/yum.repos.d/btrepl.repo && dnf install -y -q btrepl")
    else:
        sh(m, f"zypper --non-interactive --gpg-auto-import-keys addrepo {SITE}/rpm/btrepl.repo && "
              "zypper --non-interactive --gpg-auto-import-keys --quiet install btrepl")
    latest = latest_release()
    assert package_version(m, "rpm") == latest
    assert_service_running(m)


def latest_release() -> str:
    with urllib.request.urlopen("https://api.github.com/repos/eleutherius/btrepl/releases/latest", timeout=20) as r:
        tag = json.load(r)["tag_name"]
    return re.sub(r"^v", "", tag)
