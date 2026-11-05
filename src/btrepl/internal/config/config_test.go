package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSlaveAddr(t *testing.T) {
	c := Default()
	for in, want := range map[string]string{
		"10.0.0.2":           "10.0.0.2:22",
		"10.0.0.2:2222":      "10.0.0.2:2222",
		"slave.example":      "slave.example:22",
		"slave.example:2200": "slave.example:2200",
		"fe80::1":            "[fe80::1]:22",
		"[fe80::1]":          "[fe80::1]:22",
		"[fe80::1]:2222":     "[fe80::1]:2222",
	} {
		if got := c.SlaveAddr(in); got != want {
			t.Errorf("SlaveAddr(%q) = %s, want %s", in, got, want)
		}
	}
	c.SSHPort = 2022
	if got := c.SlaveAddr("10.0.0.2"); got != "10.0.0.2:2022" {
		t.Errorf("ssh_port 2022: SlaveAddr = %s", got)
	}
}

func TestLoadOldConfigGetsSSHDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("btrfs_root: /data\nslaves: [10.0.0.2]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.SSHPort != 22 || c.SSHKnownHosts != "/root/.ssh/known_hosts" || c.BtrfsRoot != "/data" {
		t.Fatalf("got %+v", c)
	}
}

func TestTimeouts(t *testing.T) {
	c := Default()
	if c.CommandTimeout() != 2*time.Minute || c.SendIdleTimeout() != 10*time.Minute {
		t.Fatalf("defaults: %s %s", c.CommandTimeout(), c.SendIdleTimeout())
	}
	c.SSHCmdTimeout, c.SendIdle = "30s", "1h"
	if c.CommandTimeout() != 30*time.Second || c.SendIdleTimeout() != time.Hour {
		t.Fatalf("set: %s %s", c.CommandTimeout(), c.SendIdleTimeout())
	}
	for _, bad := range []string{"", "0s", "-1m", "soon"} {
		c.SSHCmdTimeout = bad
		if c.CommandTimeout() != 2*time.Minute {
			t.Errorf("%q: got %s", bad, c.CommandTimeout())
		}
	}
}
