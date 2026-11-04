package config

import (
	"os"
	"path/filepath"
	"testing"
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
