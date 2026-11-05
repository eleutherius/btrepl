package config

import (
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/btrepl/config.yaml"

type Config struct {
	BtrfsRoot      string   `yaml:"btrfs_root"`
	SSHIdentity    string   `yaml:"ssh_identity"`
	SSHUser        string   `yaml:"ssh_user"`
	SSHPort        int      `yaml:"ssh_port"`
	SSHKnownHosts  string   `yaml:"ssh_known_hosts"`
	SSHCmdTimeout  string   `yaml:"ssh_command_timeout"`
	SendIdle       string   `yaml:"send_idle_timeout"`
	SnapshotDir    string   `yaml:"snapshot_dir"`
	SnapshotPrefix string   `yaml:"snapshot_prefix"`
	Interval       string   `yaml:"interval"`
	KeepSender     int      `yaml:"keep_sender"`
	KeepReceiver   int      `yaml:"keep_receiver"`
	MaxAnchorAge   string   `yaml:"max_anchor_age"`
	Subvolumes     []string `yaml:"subvolumes"`
	Slaves         []string `yaml:"slaves"`
}

func Default() *Config {
	return &Config{
		BtrfsRoot:      "/btrfs",
		SSHIdentity:    "/root/.ssh/id_ed25519",
		SSHUser:        "root",
		SSHPort:        22,
		SSHKnownHosts:  "/root/.ssh/known_hosts",
		SSHCmdTimeout:  "2m",
		SendIdle:       "10m",
		SnapshotDir:    ".snapshots",
		SnapshotPrefix: "btrepl_",
		Interval:       "1h",
		KeepSender:     428,
		KeepReceiver:   10,
		Subvolumes:     []string{"@data", "@postgres"},
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

// CommandTimeout is the limit for one remote command other than the send
// (ssh_command_timeout). An empty, invalid or non-positive value gives 2m.
func (c *Config) CommandTimeout() time.Duration { return duration(c.SSHCmdTimeout, 2*time.Minute) }

// SendIdleTimeout aborts a send that moves no data for this long
// (send_idle_timeout). An empty, invalid or non-positive value gives 10m.
func (c *Config) SendIdleTimeout() time.Duration { return duration(c.SendIdle, 10*time.Minute) }

func duration(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// SlaveAddr returns the address to dial for a slave entry. An entry is a
// host, or host:port ([v6addr]:port for IPv6) to override ssh_port.
func (c *Config) SlaveAddr(slave string) string {
	if host, port, err := net.SplitHostPort(slave); err == nil && host != "" && port != "" {
		return slave
	}
	port := c.SSHPort
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(slave, "["), "]"), strconv.Itoa(port))
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
