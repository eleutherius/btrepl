package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/eleutherius/btrepl/api"
)

// shortDir returns a temp dir with a short path: Unix socket paths are
// limited to about 100 bytes, and t.TempDir can exceed that on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "btrepl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestListenUnixSocketIsPrivate(t *testing.T) {
	path := filepath.Join(shortDir(t), "run", "btrepl.sock")
	lis, opts, err := Listen("unix:"+path, TLSFiles{})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	if len(opts) != 0 {
		t.Errorf("unix socket should not use TLS options")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %s, want socket with 0600", st.Mode())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("socket dir mode = %s, want 0700", st.Mode().Perm())
	}
}

func TestListenUnixReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(shortDir(t), "btrepl.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	lis, _, err := Listen("unix://"+path, TLSFiles{})
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	lis.Close()
}

func TestListenUnixRefusesToReplaceOtherFiles(t *testing.T) {
	path := filepath.Join(shortDir(t), "btrepl.sock")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen("unix:"+path, TLSFiles{}); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	if data, _ := os.ReadFile(path); string(data) != "data" {
		t.Fatal("regular file was modified")
	}
}

func TestListenTCPRequiresMutualTLS(t *testing.T) {
	for _, files := range []TLSFiles{{}, {Cert: "c", Key: "k"}, {ClientCA: "ca"}} {
		_, _, err := Listen("127.0.0.1:0", files)
		if err == nil || !strings.Contains(err.Error(), "mutual TLS") {
			t.Errorf("TLSFiles %+v: err = %v", files, err)
		}
	}
}

func TestListenUnixRejectsTLSOptions(t *testing.T) {
	path := filepath.Join(shortDir(t), "btrepl.sock")
	if _, _, err := Listen("unix:"+path, TLSFiles{Cert: "c"}); err == nil {
		t.Fatal("TLS options on a Unix socket should be rejected")
	}
}

// testPKI holds a CA with a server certificate for 127.0.0.1 and a client
// certificate, plus a client certificate from an unrelated CA.
type testPKI struct {
	dir                       string
	caCert                    string
	serverCert, serverKey     string
	caPool                    *x509.CertPool
	clientPair, untrustedPair tls.Certificate
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	p := &testPKI{dir: t.TempDir()}
	ca, caKey := makeCert(t, nil, nil, x509.Certificate{IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign})
	srv, srvKey := makeCert(t, ca, caKey, x509.Certificate{
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	cli, cliKey := makeCert(t, ca, caKey, x509.Certificate{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	other, otherKey := makeCert(t, nil, nil, x509.Certificate{IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign})
	stranger, strangerKey := makeCert(t, other, otherKey, x509.Certificate{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})

	p.caCert = p.write(t, "ca.crt", certPEM(ca))
	p.serverCert = p.write(t, "server.crt", certPEM(srv))
	p.serverKey = p.write(t, "server.key", keyPEM(t, srvKey))
	p.caPool = x509.NewCertPool()
	p.caPool.AddCert(ca)
	p.clientPair = tls.Certificate{Certificate: [][]byte{cli.Raw}, PrivateKey: cliKey}
	p.untrustedPair = tls.Certificate{Certificate: [][]byte{stranger.Raw}, PrivateKey: strangerKey}
	return p
}

func (p *testPKI) write(t *testing.T, name string, data []byte) string {
	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var serial int64

func makeCert(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, tmpl x509.Certificate) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial++
	tmpl.SerialNumber = big.NewInt(serial)
	tmpl.Subject = pkix.Name{CommonName: "btrepl-test"}
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(time.Hour)
	if parent == nil {
		parent, parentKey = &tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func keyPEM(t *testing.T, k *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func TestMutualTLS(t *testing.T) {
	pki := newTestPKI(t)
	lis, opts, err := Listen("127.0.0.1:0", TLSFiles{Cert: pki.serverCert, Key: pki.serverKey, ClientCA: pki.caCert})
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterBtreplServer(srv, New(filepath.Join(t.TempDir(), "config.yaml"), slog.New(slog.DiscardHandler), nil))
	go srv.Serve(lis)
	defer srv.Stop()

	call := func(creds credentials.TransportCredentials) error {
		conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(creds))
		if err != nil {
			return err
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = pb.NewBtreplClient(conn).Run(ctx, &pb.RunRequest{})
		return err
	}

	if err := call(credentials.NewTLS(&tls.Config{RootCAs: pki.caPool, Certificates: []tls.Certificate{pki.clientPair}})); err != nil {
		t.Errorf("client with a trusted certificate: %v", err)
	}
	if err := call(credentials.NewTLS(&tls.Config{RootCAs: pki.caPool})); err == nil {
		t.Error("client without a certificate was accepted")
	}
	if err := call(credentials.NewTLS(&tls.Config{RootCAs: pki.caPool, Certificates: []tls.Certificate{pki.untrustedPair}})); err == nil {
		t.Error("client with a certificate from another CA was accepted")
	}
	if err := call(insecure.NewCredentials()); err == nil {
		t.Error("plaintext client was accepted")
	}
}
