package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyCheck verifies host keys against a known_hosts file. With trustNew
// it records the key of a host that is not in the file yet (trust on first
// use); a key that differs from a recorded one is always refused.
type hostKeyCheck struct {
	path     string
	trustNew bool
	known    ssh.HostKeyCallback
	recorded string // SHA256 fingerprint of a key added to the file
}

func newHostKeyCheck(path string, trustNew bool) (*hostKeyCheck, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if !trustNew {
			return nil, fmt.Errorf("known_hosts file %s does not exist: run btrepl add-slave to record the slave's host key", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("read known_hosts: %w", err)
	}
	return &hostKeyCheck{path: path, trustNew: trustNew, known: known}, nil
}

func (h *hostKeyCheck) callback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	err := h.known(hostname, remote, key)
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return err
	}
	host := knownhosts.Normalize(hostname)
	if len(keyErr.Want) > 0 {
		return fmt.Errorf("host key of %s does not match %s, possible man-in-the-middle attack. "+
			"If the slave was reinstalled, remove its old key with `ssh-keygen -R '%s' -f %s` and run btrepl add-slave again",
			host, h.path, host, h.path)
	}
	if !h.trustNew {
		return fmt.Errorf("host key of %s is not in %s: run btrepl add-slave for this slave to record it", host, h.path)
	}
	if err := appendLine(h.path, knownhosts.Line([]string{host}, key)); err != nil {
		return fmt.Errorf("record host key of %s: %w", host, err)
	}
	h.recorded = ssh.FingerprintSHA256(key)
	return nil
}

func appendLine(path, line string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// algorithms returns the host key algorithms of the keys known for addr, or
// nil if the host is unknown. Without it the server may present a key of a
// type the file does not have (Go prefers ECDSA, OpenSSH records ed25519),
// which would look like a changed key.
func (h *hostKeyCheck) algorithms(addr string) []string {
	// A throwaway key makes the callback list every key known for addr.
	err := h.known(addr, &net.TCPAddr{IP: net.IPv4zero}, probeKey())
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}
	var algos []string
	for _, k := range keyErr.Want {
		if t := k.Key.Type(); t == ssh.KeyAlgoRSA {
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		} else {
			algos = append(algos, t)
		}
	}
	return slices.Compact(algos)
}

var probeKey = sync.OnceValue(func() ssh.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return k
})
