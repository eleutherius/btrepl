package config

import (
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/btrepl/config.yaml"

type Config struct {
	BtrfsRoot      string   `yaml:"btrfs_root"`
	SSHIdentity    string   `yaml:"ssh_identity"`
	SSHUser        string   `yaml:"ssh_user"`
	SnapshotDir    string   `yaml:"snapshot_dir"`
	SnapshotPrefix string   `yaml:"snapshot_prefix"`
	Interval       string   `yaml:"interval"`
	KeepSender     int      `yaml:"keep_sender"`
	KeepReceiver   int      `yaml:"keep_receiver"`
	Subvolumes     []string `yaml:"subvolumes"`
	Slaves         []string `yaml:"slaves"`
}

func Default() *Config {
	return &Config{
		BtrfsRoot:      "/btrfs",
		SSHIdentity:    "/root/.ssh/id_ed25519",
		SSHUser:        "root",
		SnapshotDir:    ".snapshots",
		SnapshotPrefix: "btrepl_",
		Interval:       "1h",
		KeepSender:     428,
		KeepReceiver:   10,
		Subvolumes:     []string{"@eventsPictures", "@reports"},
		Slaves:         []string{},
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Save(cfg *Config, path string) error {
	if err := os.MkdirAll(dirOf(path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) HasSlave(ip string) bool {
	return slices.Contains(c.Slaves, ip)
}

func (c *Config) AddSlave(ip string) bool {
	if c.HasSlave(ip) {
		return false
	}
	c.Slaves = append(c.Slaves, ip)
	return true
}

func (c *Config) RemoveSlave(ip string) bool {
	for i, s := range c.Slaves {
		if s == ip {
			c.Slaves = slices.Delete(c.Slaves, i, i+1)
			return true
		}
	}
	return false
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
