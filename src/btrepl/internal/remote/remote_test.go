package remote

import (
	"reflect"
	"strings"
	"testing"
)

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

func TestUnquote(t *testing.T) {
	for _, s := range []string{"/btrfs/x", "/my btrfs/x", "it's", "$(rm -rf /)", "a'b'c", ""} {
		got, ok := unquote(Quote(s))
		if !ok || got != s {
			t.Errorf("unquote(Quote(%q)) = %q, %v", s, got, ok)
		}
	}
	for _, s := range []string{"'unterminated", "two words", "a'b", "'a'b", `'a'\''`, "x;y"} {
		if got, ok := unquote(s); ok {
			t.Errorf("unquote(%q) = %q, want rejection", s, got)
		}
	}
}

func TestCommands(t *testing.T) {
	c := New("/btrfs", ".snapshots")
	for got, want := range map[string]string{
		c.MountInfo():                 "cat /proc/self/mountinfo",
		c.MkdirSnapshotDir():          "mkdir -p -m 0700 /btrfs/.snapshots",
		c.ChmodSnapshotDir():          "chmod 0700 /btrfs/.snapshots",
		c.ListSubvolumes():            "btrfs subvolume list -u -R /btrfs",
		c.ListReadOnly():              "btrfs subvolume list -r /btrfs",
		c.Receive():                   "btrfs receive --chroot /btrfs/.snapshots",
		c.Delete("btrepl_x"):          "btrfs subvolume delete /btrfs/.snapshots/btrepl_x",
		New("/my fs/", "s").Receive(): "btrfs receive --chroot '/my fs/s'",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestAllow(t *testing.T) {
	for _, c := range []Commands{New("/btrfs", ".snapshots"), New("/my fs", "snap shots")} {
		for cmd, want := range map[string][]string{
			c.MountInfo():                           {"cat", "/proc/self/mountinfo"},
			c.MkdirSnapshotDir():                    {"mkdir", "-p", "-m", "0700", c.snapDir},
			c.ChmodSnapshotDir():                    {"chmod", "0700", c.snapDir},
			c.ListSubvolumes():                      {"btrfs", "subvolume", "list", "-u", "-R", c.root},
			c.ListReadOnly():                        {"btrfs", "subvolume", "list", "-r", c.root},
			c.Receive():                             {"btrfs", "receive", "--chroot", c.snapDir},
			c.Delete("btrepl_data_20261104T100000"): {"btrfs", "subvolume", "delete", c.snapDir + "/btrepl_data_20261104T100000"},
		} {
			got, err := c.Allow(cmd)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Allow(%q) = %q, %v; want %q", cmd, got, err, want)
			}
		}
	}
}

func TestAllowRejects(t *testing.T) {
	c := New("/btrfs", ".snapshots")
	for _, cmd := range []string{
		"",
		"id",
		"sh -c id",
		"cat /etc/shadow",
		"mkdir -p /etc",
		"btrfs receive /",
		"btrfs receive /btrfs/.snapshots", // without --chroot
		"mkdir -p /btrfs/.snapshots",
		"chmod 0777 /btrfs/.snapshots",
		"btrfs subvolume delete /btrfs/@data",
		"btrfs subvolume delete /btrfs/.snapshots",
		"btrfs subvolume delete /btrfs/.snapshots/",
		"btrfs subvolume delete /btrfs/.snapshots/..",
		"btrfs subvolume delete /btrfs/.snapshots/a/b",
		"btrfs subvolume delete /btrfs/.snapshots/x;reboot",
		"btrfs subvolume delete '/btrfs/.snapshots/x;reboot'",
		"btrfs subvolume delete /btrfs/.snapshots/x /btrfs/@data",
		"cat /proc/self/mountinfo; id",
		c.Receive() + " && id",
	} {
		if got, err := c.Allow(cmd); err == nil {
			t.Errorf("Allow(%q) = %q, want rejection", cmd, got)
		} else if !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("Allow(%q): unexpected error %v", cmd, err)
		}
	}
}
