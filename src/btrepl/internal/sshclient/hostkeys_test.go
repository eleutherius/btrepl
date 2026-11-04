package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var remoteAddr = &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 22}

func TestUnknownHostRefusedWithoutTrust(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := newHostKeyCheck(path, false)
	if err != nil {
		t.Fatal(err)
	}
	err = check.callback("10.0.0.2:22", remoteAddr, newKey(t))
	if err == nil || !strings.Contains(err.Error(), "not in") || !strings.Contains(err.Error(), "add-slave") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingKnownHostsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	if _, err := newHostKeyCheck(path, false); err == nil {
		t.Fatal("strict check without a known_hosts file should fail")
	}
	if _, err := newHostKeyCheck(path, true); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts not created with 0600: %v %v", st, err)
	}
}

func TestTrustOnFirstUseThenStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	// An existing file without a trailing newline must not get its last
	// line merged with the new one.
	other := knownhosts.Line([]string{"10.0.0.9"}, newKey(t))
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	key := newKey(t)

	check, err := newHostKeyCheck(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := check.callback("10.0.0.2:22", remoteAddr, key); err != nil {
		t.Fatal(err)
	}
	if check.recorded != ssh.FingerprintSHA256(key) {
		t.Fatalf("recorded = %q", check.recorded)
	}
	data, _ := os.ReadFile(path)
	if want := other + "\n" + knownhosts.Line([]string{"10.0.0.2"}, key) + "\n"; string(data) != want {
		t.Fatalf("known_hosts =\n%s\nwant\n%s", data, want)
	}

	strict, err := newHostKeyCheck(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := strict.callback("10.0.0.2:22", remoteAddr, key); err != nil {
		t.Fatalf("recorded key refused: %v", err)
	}
	for _, trust := range []bool{false, true} {
		c, _ := newHostKeyCheck(path, trust)
		err := c.callback("10.0.0.2:22", remoteAddr, newKey(t))
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Errorf("trust=%v: changed key: err = %v", trust, err)
		}
	}
}

func TestRecordsNonDefaultPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	check, err := newHostKeyCheck(path, true)
	if err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	if err := check.callback("10.0.0.2:2222", remoteAddr, key); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "[10.0.0.2]:2222 ssh-ed25519 ") {
		t.Fatalf("known_hosts = %s", data)
	}
}

func TestAlgorithmsFromKnownHosts(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, _ := ssh.NewPublicKey(&rsaKey.PublicKey)
	path := filepath.Join(t.TempDir(), "known_hosts")
	lines := knownhosts.Line([]string{"10.0.0.2"}, newKey(t)) + "\n" +
		knownhosts.Line([]string{"10.0.0.3"}, rsaPub) + "\n" +
		knownhosts.Line([]string{"10.0.0.3"}, newKey(t)) + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := newHostKeyCheck(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string][]string{
		"10.0.0.2:22": {ssh.KeyAlgoED25519},
		"10.0.0.3:22": {ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA, ssh.KeyAlgoED25519},
		"10.0.0.4:22": nil,
	} {
		if got := check.algorithms(addr); !reflect.DeepEqual(got, want) {
			t.Errorf("algorithms(%s) = %v, want %v", addr, got, want)
		}
	}
}
