"""
API access tests: the daemon listens on a root-only Unix socket, and on TCP
only with mutual TLS.
"""

import grpc
import pytest

from btrepl import BtreplClient
from conftest import GRPC_SOCKET, MASTER, rem, ssh_tunnel

PKI = "/root/btrepl-test-pki"
TLS_PORT = 50052
TLS_UNIT = "btrepl-mtls-test"


class TestSocket:
    def test_socket_and_dir_are_root_only(self):
        out = rem(MASTER, f"stat -c '%a %U %F' {GRPC_SOCKET} /run/btrepl").stdout.splitlines()
        assert out == ["600 root socket", "700 root directory"]

    def test_unprivileged_user_cannot_reach_socket(self):
        r = rem(MASTER, f"runuser -u nobody -- test -w {GRPC_SOCKET}", check=False)
        assert r.returncode != 0

    def test_nothing_listens_on_tcp(self):
        out = rem(MASTER, "ss -Hltnp").stdout
        assert "btrepl" not in out

    def test_serve_refuses_tcp_without_mutual_tls(self):
        r = rem(MASTER, "timeout 5 btrepl serve --addr 127.0.0.1:50059", check=False)
        assert r.returncode not in (0, 124), "serve started on TCP without TLS"
        assert "requires mutual TLS" in r.stdout + r.stderr


@pytest.fixture(scope="module")
def mtls_daemon():
    """A second daemon on the master that listens on TCP with mutual TLS."""
    rem(MASTER, f"""
        set -e
        rm -rf {PKI} && mkdir -m 700 {PKI} && cd {PKI}
        key() {{ openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$1.key" 2>/dev/null; }}
        sign() {{
            openssl req -new -key "$1.key" -subj "/CN=$1" -out "$1.csr"
            openssl x509 -req -in "$1.csr" -CA "$2.crt" -CAkey "$2.key" -CAcreateserial \\
                -days 1 -out "$1.crt" -extfile <(printf '%s\\n' "$3") 2>/dev/null
        }}
        key ca && openssl req -x509 -new -key ca.key -subj /CN=test-ca -days 1 -out ca.crt
        key other-ca && openssl req -x509 -new -key other-ca.key -subj /CN=other-ca -days 1 -out other-ca.crt
        key server && sign server ca "subjectAltName=IP:127.0.0.1
extendedKeyUsage=serverAuth"
        key client && sign client ca "extendedKeyUsage=clientAuth"
        key stranger && sign stranger other-ca "extendedKeyUsage=clientAuth"
        systemd-run --unit={TLS_UNIT} /usr/local/bin/btrepl serve --addr 127.0.0.1:{TLS_PORT} \\
            --tls-cert {PKI}/server.crt --tls-key {PKI}/server.key --tls-client-ca {PKI}/ca.crt
        for i in $(seq 1 50); do ss -Hltn "sport = :{TLS_PORT}" | grep -q . && break; sleep 0.1; done
    """)
    files = {n: rem(MASTER, f"cat {PKI}/{n}").stdout.encode()
             for n in ("ca.crt", "client.crt", "client.key", "stranger.crt", "stranger.key")}
    try:
        with ssh_tunnel(MASTER, TLS_PORT) as port:
            yield f"127.0.0.1:{port}", files
    finally:
        rem(MASTER, f"systemctl stop {TLS_UNIT}; rm -rf {PKI}", check=False)


class TestMutualTLS:
    def test_trusted_client_certificate(self, mtls_daemon):
        addr, f = mtls_daemon
        with BtreplClient(addr, root_certificates=f["ca.crt"],
                          certificate_chain=f["client.crt"], private_key=f["client.key"]) as c:
            assert c.status().subvolumes

    def test_no_client_certificate(self, mtls_daemon):
        addr, f = mtls_daemon
        with BtreplClient(addr, timeout=5, root_certificates=f["ca.crt"]) as c:
            with pytest.raises(grpc.RpcError):
                c.status()

    def test_certificate_from_another_ca(self, mtls_daemon):
        addr, f = mtls_daemon
        with BtreplClient(addr, timeout=5, root_certificates=f["ca.crt"],
                          certificate_chain=f["stranger.crt"], private_key=f["stranger.key"]) as c:
            with pytest.raises(grpc.RpcError):
                c.status()

    def test_plaintext_client(self, mtls_daemon):
        addr, _ = mtls_daemon
        with BtreplClient(addr, timeout=5) as c:
            with pytest.raises(grpc.RpcError):
                c.status()
