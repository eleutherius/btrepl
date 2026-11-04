"""
Hardening tests: host key verification, SSH port, run lock, ssh-filter.

Cycles are run with the `btrepl run` CLI on the master. The master daemon is
stopped for this module so that its cycles do not hold the run lock.
"""

import re

import pytest

from conftest import MASTER, SLAVES, SSH_KEY, rem

KNOWN_HOSTS = "/root/.ssh/known_hosts"
AUTHORIZED_KEYS = "/root/.ssh/authorized_keys"


def run_cycle(master_cli, *, expect_ok: bool = True) -> str:
    r = master_cli("run", check=False)
    out = r.stdout + r.stderr
    if expect_ok:
        assert r.returncode == 0, f"btrepl run failed:\n{out}"
    return out


@pytest.fixture(scope="module", autouse=True)
def master_daemon_stopped():
    rem(MASTER, "systemctl stop btrepl.service")
    yield
    rem(MASTER, "systemctl start btrepl.service")


@pytest.fixture
def known_hosts_restored():
    rem(MASTER, f"cp {KNOWN_HOSTS} {KNOWN_HOSTS}.bak")
    yield
    rem(MASTER, f"mv {KNOWN_HOSTS}.bak {KNOWN_HOSTS}")


class TestHostKeys:
    def test_unknown_slave_is_refused_until_add_slave(self, master_cli, slave_ips, known_hosts_restored):
        ip = slave_ips[0]
        rem(MASTER, f"ssh-keygen -q -R {ip} -f {KNOWN_HOSTS}")

        log = run_cycle(master_cli, expect_ok=False)
        assert f"host key of {ip} is not in {KNOWN_HOSTS}" in log

        r = master_cli("add-slave", "-s", ip)
        assert "recorded slave host key" in r.stdout + r.stderr
        run_cycle(master_cli)

    def test_changed_host_key_is_refused(self, master_cli, slave_ips, known_hosts_restored):
        ip = slave_ips[0]
        rem(MASTER, f"""
            ssh-keygen -q -R {ip} -f {KNOWN_HOSTS}
            rm -f /tmp/fakehost /tmp/fakehost.pub
            ssh-keygen -q -t ed25519 -N '' -f /tmp/fakehost
            echo "{ip} $(cut -d' ' -f1,2 /tmp/fakehost.pub)" >> {KNOWN_HOSTS}
            rm -f /tmp/fakehost /tmp/fakehost.pub
        """)

        log = run_cycle(master_cli, expect_ok=False)
        assert f"host key of {ip} does not match" in log

        r = master_cli("add-slave", "-s", ip, check=False)
        assert r.returncode != 0
        assert "does not match" in r.stdout + r.stderr

    def test_only_ed25519_key_known(self, master_cli, slave_ips, known_hosts_restored):
        """OpenSSH records the ed25519 key; Go would ask for ECDSA first."""
        ip = slave_ips[0]
        rem(MASTER, f"ssh-keygen -q -R {ip} -f {KNOWN_HOSTS} && ssh-keyscan -t ed25519 {ip} >> {KNOWN_HOSTS} 2>/dev/null")
        run_cycle(master_cli)


class TestSSHPort:
    def test_slave_on_custom_port(self, master_cli, slave_ips, known_hosts_restored):
        slave, ip = SLAVES[1], slave_ips[1]
        addr = f"{ip}:2222"
        rem(slave, "printf 'Port 22\\nPort 2222\\n' > /etc/ssh/sshd_config.d/btrepl-test.conf && systemctl restart ssh")
        try:
            r = master_cli("add-slave", "-s", addr)
            assert "recorded slave host key" in r.stdout + r.stderr
            assert f"[{ip}]:2222 " in rem(MASTER, f"cat {KNOWN_HOSTS}").stdout

            log = run_cycle(master_cli)
            assert f'msg="replication ok" slave={addr}' in log
        finally:
            master_cli("del-slave", "-s", addr, check=False)
            rem(slave, "rm -f /etc/ssh/sshd_config.d/btrepl-test.conf && systemctl restart ssh")


class TestRunLock:
    def test_concurrent_cycle_is_refused(self, master_cli):
        r = rem(MASTER, "flock /run/btrepl.lock sleep 10 >/dev/null 2>&1 & sleep 1; btrepl run; echo rc=$?", check=False)
        out = r.stdout + r.stderr
        assert "another replication cycle is running" in out
        assert "rc=1" in out
        rem(MASTER, "flock /run/btrepl.lock true")  # wait for the holder to exit
        run_cycle(master_cli)


class TestSSHFilter:
    def test_master_key_restricted_to_btrepl_commands(self, master_cli, slave_ips):
        slave, ip = SLAVES[2], slave_ips[2]
        pubkey = rem(MASTER, f"cat {SSH_KEY}.pub").stdout.strip()
        restricted = f'restrict,command="/usr/local/bin/btrepl ssh-filter" {pubkey}'
        rem(slave, f"cp {AUTHORIZED_KEYS} {AUTHORIZED_KEYS}.bak && "
                   f"grep -vF '{pubkey}' {AUTHORIZED_KEYS}.bak > {AUTHORIZED_KEYS}; "
                   f"echo '{restricted}' >> {AUTHORIZED_KEYS}")
        try:
            log = run_cycle(master_cli)
            assert f'msg="replication ok" slave={ip}' in log
            # keep_receiver is 5 and the slave already has that many, so the
            # cycle also deleted an old snapshot through the filter.
            assert re.search(rf'msg="pruned remote snapshot" snap=\S+ slave={re.escape(ip)}\b', log)

            r = rem(MASTER, f"ssh -o BatchMode=yes -i {SSH_KEY} root@{ip} id", check=False)
            assert r.returncode != 0
            assert "command not allowed" in r.stderr
        finally:
            rem(slave, f"mv {AUTHORIZED_KEYS}.bak {AUTHORIZED_KEYS}")
