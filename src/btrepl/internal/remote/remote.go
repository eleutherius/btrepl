// Package remote builds the shell commands btrepl runs on a slave over SSH,
// and checks incoming commands against the same set for `btrepl ssh-filter`,
// the forced command that restricts the master's key on a slave.
package remote

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Commands builds the commands for one btrfs_root and snapshot_dir. Every
// path is quoted with Quote.
type Commands struct {
	root    string
	snapDir string
}

func New(btrfsRoot, snapshotDir string) Commands {
	root := filepath.Clean(btrfsRoot)
	return Commands{root: root, snapDir: filepath.Join(root, snapshotDir)}
}

func (c Commands) MountInfo() string        { return "cat /proc/self/mountinfo" }
func (c Commands) MkdirSnapshotDir() string { return "mkdir -p " + Quote(c.snapDir) }
func (c Commands) ListSubvolumes() string   { return "btrfs subvolume list -u -R " + Quote(c.root) }
func (c Commands) ListReadOnly() string     { return "btrfs subvolume list -r " + Quote(c.root) }
func (c Commands) Receive() string          { return "btrfs receive " + Quote(c.snapDir) }

// Delete deletes the snapshot called name in the snapshot directory.
func (c Commands) Delete(name string) string {
	return deletePrefix + Quote(filepath.Join(c.snapDir, name))
}

const deletePrefix = "btrfs subvolume delete "

// Allow returns the argv to execute for cmd, an SSH_ORIGINAL_COMMAND, if it
// is one of the commands above. The argv is meant for exec, not for a shell,
// so nothing in cmd is interpreted by a shell.
func (c Commands) Allow(cmd string) ([]string, error) {
	switch cmd {
	case c.MountInfo():
		return []string{"cat", "/proc/self/mountinfo"}, nil
	case c.MkdirSnapshotDir():
		return []string{"mkdir", "-p", c.snapDir}, nil
	case c.ListSubvolumes():
		return []string{"btrfs", "subvolume", "list", "-u", "-R", c.root}, nil
	case c.ListReadOnly():
		return []string{"btrfs", "subvolume", "list", "-r", c.root}, nil
	case c.Receive():
		return []string{"btrfs", "receive", c.snapDir}, nil
	}
	if arg, ok := strings.CutPrefix(cmd, deletePrefix); ok {
		path, ok := unquote(arg)
		name := filepath.Base(path)
		if ok && filepath.Dir(path) == c.snapDir && validName(name) && cmd == c.Delete(name) {
			return []string{"btrfs", "subvolume", "delete", path}, nil
		}
	}
	return nil, fmt.Errorf("command not allowed: %q", cmd)
}

// validName accepts snapshot names: btrepl creates them from the prefix, the
// subvolume name and a timestamp.
func validName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool { return !safe(r) || r == '/' }) < 0
}

func safe(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r)
}

// Quote returns s quoted as a single word for a POSIX shell.
func Quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unquote reverses Quote. It accepts only words Quote can produce.
func unquote(w string) (string, bool) {
	if !strings.HasPrefix(w, "'") {
		if w == "" || Quote(w) != w {
			return "", false
		}
		return w, true
	}
	var b strings.Builder
	for rest := w; rest != ""; {
		if !strings.HasPrefix(rest, "'") {
			return "", false
		}
		end := strings.Index(rest[1:], "'")
		if end < 0 {
			return "", false
		}
		b.WriteString(rest[1 : 1+end])
		rest = rest[2+end:]
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, `\''`) {
			return "", false
		}
		b.WriteByte('\'')
		rest = rest[2:]
	}
	s := b.String()
	if Quote(s) != w {
		return "", false
	}
	return s, true
}
