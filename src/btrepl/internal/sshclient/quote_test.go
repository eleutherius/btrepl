package sshclient

import "testing"

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/btrfs/.snapshots/btrepl_data_20261104T100000": "/btrfs/.snapshots/btrepl_data_20261104T100000",
		"":              "''",
		"/my btrfs/x":   "'/my btrfs/x'",
		"it's":          `'it'\''s'`,
		"$(rm -rf /)":   "'$(rm -rf /)'",
		"a;b":           "'a;b'",
		"--opt=@x+y,z:": "--opt=@x+y,z:",
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
