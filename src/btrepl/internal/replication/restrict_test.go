package replication

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func pubKey(t *testing.T) ssh.PublicKey {
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

func authLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

func TestRestrictAuthorizedKeys(t *testing.T) {
	master, other := pubKey(t), pubKey(t)
	in := "# managed by hand\n" +
		authLine(other) + " admin@laptop\n" +
		authLine(master) + " root@master\n" +
		"\n" +
		`from="10.0.0.1",no-pty ` + authLine(master) + "\n"

	out, n := restrictAuthorizedKeys(in, master, "10.0.0.5", "/usr/local/bin/btrepl")
	if n != 2 {
		t.Fatalf("replaced %d lines, want 2", n)
	}
	want := "# managed by hand\n" +
		authLine(other) + " admin@laptop\n" +
		`restrict,from="10.0.0.5",command="/usr/local/bin/btrepl ssh-filter" ` + authLine(master) + " root@master\n" +
		"\n" +
		`restrict,from="10.0.0.5",command="/usr/local/bin/btrepl ssh-filter" ` + authLine(master) + "\n"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestRestrictAuthorizedKeysKeyMissing(t *testing.T) {
	in := authLine(pubKey(t)) + " someone\n"
	out, n := restrictAuthorizedKeys(in, pubKey(t), "10.0.0.5", "/usr/local/bin/btrepl")
	if n != 0 || out != in {
		t.Fatalf("n = %d, out = %q", n, out)
	}
}

func TestRestrictAuthorizedKeysKeepsMissingNewline(t *testing.T) {
	master := pubKey(t)
	out, n := restrictAuthorizedKeys(authLine(master), master, "fe80::1", "/usr/bin/btrepl")
	want := `restrict,from="fe80::1",command="/usr/bin/btrepl ssh-filter" ` + authLine(master)
	if n != 1 || out != want {
		t.Fatalf("got %q", out)
	}
}

func TestClientAddr(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.5 51234 22":   "10.0.0.5",
		"fe80::1 51234 22\n":  "fe80::1",
		"":                    "",
		"not-an-ip 1 2":       "",
		"10.0.0.5\" evil 1 2": "",
	} {
		if got := clientAddr(in); got != want {
			t.Errorf("clientAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
