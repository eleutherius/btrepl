package btrfs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const timestampLayout = "20060102T150405"

type Snapshot struct {
	Name    string
	Subvol  string
	Time    time.Time
	AbsPath string
}

type Manager struct {
	root        string
	snapshotDir string
	prefix      string
}

func NewManager(root, snapshotDir, prefix string) *Manager {
	return &Manager{root: root, snapshotDir: snapshotDir, prefix: prefix}
}

func (m *Manager) SnapshotDirPath() string {
	return filepath.Join(m.root, m.snapshotDir)
}

func (m *Manager) SubvolPath(subvol string) string {
	return filepath.Join(m.root, subvol)
}

func (m *Manager) snapshotName(subvol, ts string) string {
	return m.prefix + stripAt(subvol) + "_" + ts
}

func (m *Manager) EnsureSnapshotDir() error {
	return os.MkdirAll(m.SnapshotDirPath(), 0755)
}

// CreateSnapshot creates a read-only snapshot of subvol and returns its descriptor.
func (m *Manager) CreateSnapshot(subvol string) (Snapshot, error) {
	if err := m.EnsureSnapshotDir(); err != nil {
		return Snapshot{}, fmt.Errorf("ensure snapshot dir: %w", err)
	}

	ts := time.Now().UTC().Format(timestampLayout)
	name := m.snapshotName(subvol, ts)
	snapPath := filepath.Join(m.SnapshotDirPath(), name)
	subvolPath := m.SubvolPath(subvol)

	cmd := exec.Command("btrfs", "subvolume", "snapshot", "-r", subvolPath, snapPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Snapshot{}, fmt.Errorf("btrfs snapshot %s: %w\n%s", subvol, err, out)
	}

	t, _ := time.ParseInLocation(timestampLayout, ts, time.UTC)
	return Snapshot{Name: name, Subvol: subvol, Time: t, AbsPath: snapPath}, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *Manager) DeleteSnapshot(snap Snapshot) error {
	cmd := exec.Command("btrfs", "subvolume", "delete", snap.AbsPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("btrfs delete %s: %w\n%s", snap.Name, err, out)
	}
	return nil
}

// ListSnapshots returns snapshots for subvol sorted oldest-first.
func (m *Manager) ListSnapshots(subvol string) ([]Snapshot, error) {
	prefix := m.snapshotName(subvol, "")
	entries, err := os.ReadDir(m.SnapshotDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot dir: %w", err)
	}

	var snaps []Snapshot
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		ts := strings.TrimPrefix(name, prefix)
		t, err := time.ParseInLocation(timestampLayout, ts, time.UTC)
		if err != nil {
			continue
		}
		snaps = append(snaps, Snapshot{
			Name:    name,
			Subvol:  subvol,
			Time:    t,
			AbsPath: filepath.Join(m.SnapshotDirPath(), name),
		})
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].Time.Before(snaps[j].Time)
	})
	return snaps, nil
}

// SubvolumeExists reports whether path is a btrfs subvolume.
func (m *Manager) SubvolumeExists(path string) (bool, error) {
	cmd := exec.Command("btrfs", "subvolume", "show", path)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, err
}

// CreateSubvolume creates a new btrfs subvolume at path.
func (m *Manager) CreateSubvolume(path string) error {
	cmd := exec.Command("btrfs", "subvolume", "create", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("btrfs create %s: %w\n%s", path, err, out)
	}
	return nil
}

// DeleteSubvolume deletes the subvolume at path if it exists.
func (m *Manager) DeleteSubvolume(path string) error {
	exists, err := m.SubvolumeExists(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	cmd := exec.Command("btrfs", "subvolume", "delete", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("btrfs delete subvol %s: %w\n%s", path, err, out)
	}
	return nil
}

// SendCmd returns the btrfs-send command for snap, optionally using parent for incremental send.
func (m *Manager) SendCmd(snap Snapshot, parent *Snapshot) *exec.Cmd {
	args := []string{"send"}
	if parent != nil {
		args = append(args, "-p", parent.AbsPath)
	}
	args = append(args, snap.AbsPath)
	return exec.Command("btrfs", args...)
}

// WritableSnapshot creates a writable snapshot of src at dst.
func (m *Manager) WritableSnapshot(src, dst string) error {
	cmd := exec.Command("btrfs", "subvolume", "snapshot", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("btrfs writable snapshot %s -> %s: %w\n%s", src, dst, err, out)
	}
	return nil
}

// EnableQuota enables btrfs quota on the root.
func (m *Manager) EnableQuota() error {
	cmd := exec.Command("btrfs", "quota", "enable", m.root)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("btrfs quota enable: %w\n%s", err, out)
	}
	return nil
}

func stripAt(s string) string {
	return strings.TrimPrefix(s, "@")
}
