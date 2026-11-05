package server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// DefaultAddr is where `btrepl serve` listens unless told otherwise: a Unix
// socket that only root can open.
const DefaultAddr = "unix:/run/btrepl/btrepl.sock"

// TLSFiles are the PEM files for mutual TLS on a TCP listener.
type TLSFiles struct {
	Cert     string // server certificate
	Key      string // server private key
	ClientCA string // CA that signs the certificates of allowed clients
}

func (f TLSFiles) empty() bool { return f.Cert == "" && f.Key == "" && f.ClientCA == "" }

// Listen opens the gRPC listener for addr and returns the server options it
// needs. addr is "unix:<path>" (or "unix://<path>") or "host:port". The API
// can change where data is replicated to, so a Unix socket is created with
// mode 0600 in a 0700 directory, and a TCP listener requires mutual TLS.
func Listen(addr string, files TLSFiles) (net.Listener, []grpc.ServerOption, error) {
	if path, ok := unixPath(addr); ok {
		if !files.empty() {
			return nil, nil, errors.New("TLS options apply only to a TCP address; a Unix socket is protected by file permissions")
		}
		lis, err := listenUnix(path)
		return lis, nil, err
	}

	if files.Cert == "" || files.Key == "" || files.ClientCA == "" {
		return nil, nil, fmt.Errorf("listening on TCP %s requires mutual TLS: set --tls-cert, --tls-key and --tls-client-ca", addr)
	}
	cfg, err := mutualTLS(files)
	if err != nil {
		return nil, nil, err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	return lis, []grpc.ServerOption{grpc.Creds(credentials.NewTLS(cfg))}, nil
}

func unixPath(addr string) (string, bool) {
	if p, ok := strings.CutPrefix(addr, "unix://"); ok {
		return p, true
	}
	return strings.CutPrefix(addr, "unix:")
}

func listenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		// Left behind by a daemon that did not shut down cleanly.
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	// The umask makes the socket 0600 from the moment it exists. Nothing else
	// runs yet when serve starts, so changing the process umask is safe.
	old := syscall.Umask(0o177)
	lis, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	return lis, nil
}

func mutualTLS(files TLSFiles) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(files.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificates in client CA file %s", files.ClientCA)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
