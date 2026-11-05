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

	// Set only by listings that read btrfs metadata (Manager.Snapshots).
	UUID         string
	ReceivedUUID string
	ReadOnly     bool
}

// SendUUID is the UUID that btrfs send puts in the stream for this snapshot,
// and that the receiver stores as its received_uuid: the snapshot's own
// received UUID if it was itself received, else its UUID.
func (s Snapshot) SendUUID() string {
	if s.ReceivedUUID != "" {
		return s.ReceivedUUID
	}
	return s.UUID
}

// Received reports whether s is a complete received snapshot. btrfs receive
// sets the received UUID and the read-only flag only after the whole stream
// has been applied, so an interrupted receive leaves the snapshot writable.
func (s Snapshot) Received() bool {
	return s.ReadOnly && s.ReceivedUUID != ""
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

// EnsureSnapshotDir creates the snapshot directory and makes it readable
// only by root, also when it already existed: snapshots keep old versions of
// files that their owners may have deleted or protected since.
func (m *Manager) EnsureSnapshotDir() error {
	if err := os.MkdirAll(m.SnapshotDirPath(), 0o700); err != nil {
		return err
	}
	return os.Chmod(m.SnapshotDirPath(), 0o700)
}

// CreateSnapshot creates a read-only snapshot of subvol and returns its descriptor.
// Snapshot names have one-second resolution: if this second's name is taken
// (the previous cycle finished less than a second ago), it waits for the next
// second rather than let btrfs create the snapshot inside the existing one.
func (m *Manager) CreateSnapshot(subvol string) (Snapshot, error) {
	if err := m.EnsureSnapshotDir(); err != nil {
		return Snapshot{}, fmt.Errorf("ensure snapshot dir: %w", err)
	}

	var ts, name, snapPath string
	for attempt := 0; ; attempt++ {
		now := time.Now().UTC()
		ts = now.Format(timestampLayout)
		name = m.snapshotName(subvol, ts)
		snapPath = filepath.Join(m.SnapshotDirPath(), name)
		if _, err := os.Lstat(snapPath); os.IsNotExist(err) {
			break
		} else if err != nil {
			return Snapshot{}, err
		}
		if attempt == 1 {
			return Snapshot{}, fmt.Errorf("snapshot %s already exists", snapPath)
		}
		time.Sleep(time.Until(now.Truncate(time.Second).Add(time.Second)))
	}
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

// ListAllSubvolumes scans the snapshot directory and returns unique subvolume
// names derived from snapshot names matching the configured prefix.
func (m *Manager) ListAllSubvolumes() ([]string, error) {
	entries, err := os.ReadDir(m.SnapshotDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot dir: %w", err)
	}

	seen := make(map[string]bool)
	var subvols []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, m.prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, m.prefix)
		idx := strings.LastIndex(rest, "_")
		if idx < 0 {
			continue
		}
		ts := rest[idx+1:]
		if _, err := time.ParseInLocation(timestampLayout, ts, time.UTC); err != nil {
			continue
		}
		subvol := "@" + rest[:idx]
		if !seen[subvol] {
			seen[subvol] = true
			subvols = append(subvols, subvol)
		}
	}
	return subvols, nil
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

// EnsureSubvolume creates the configured subvolume under root if nothing
// exists at its path. It reports whether the subvolume was created and fails
// if the path exists but is not a subvolume.
func (m *Manager) EnsureSubvolume(subvol string) (bool, error) {
	path := m.SubvolPath(subvol)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := m.CreateSubvolume(path); err != nil {
			return false, err
		}
		return true, nil
	} else if err != nil {
		return false, err
	}
	exists, err := m.SubvolumeExists(path)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("%s exists but is not a btrfs subvolume: create a subvolume and move the data into it", path)
	}
	return false, nil
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

// Restore replaces subvol with a writable snapshot of snap. The new subvolume
// is created next to the old one first and swapped in by rename, so a failure
// at any step leaves the existing subvolume in place. If the old data could
// not be deleted after the swap, it returns the path it was moved to.
func (m *Manager) Restore(snap Snapshot, subvol string) (kept string, err error) {
	dst := m.SubvolPath(subvol)
	tmp := dst + ".btrepl-new"
	if err := m.DeleteSubvolume(tmp); err != nil {
		return "", fmt.Errorf("remove leftover %s: %w", tmp, err)
	}
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		return "", fmt.Errorf("%s is in the way and is not a subvolume: remove it", tmp)
	}
	if err := m.WritableSnapshot(snap.AbsPath, tmp); err != nil {
		return "", err
	}

	old := ""
	if _, err := os.Lstat(dst); err == nil {
		old = fmt.Sprintf("%s.btrepl-old-%s", dst, time.Now().UTC().Format(timestampLayout))
		if err := os.Rename(dst, old); err != nil {
			_ = m.DeleteSubvolume(tmp)
			return "", fmt.Errorf("move %s aside: %w", dst, err)
		}
	} else if !os.IsNotExist(err) {
		_ = m.DeleteSubvolume(tmp)
		return "", err
	}

	if err := os.Rename(tmp, dst); err != nil {
		if old != "" {
			if rerr := os.Rename(old, dst); rerr != nil {
				return old, fmt.Errorf("move %s into place: %w (old data left at %s: %v)", tmp, err, old, rerr)
			}
		}
		_ = m.DeleteSubvolume(tmp)
		return "", fmt.Errorf("move %s into place: %w", tmp, err)
	}

	if old == "" {
		return "", nil
	}
	// A plain directory that was in the way is kept for the operator.
	if isSubvol, _ := m.SubvolumeExists(old); !isSubvol {
		return old, nil
	}
	if err := m.DeleteSubvolume(old); err != nil {
		return old, nil
	}
	return "", nil
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
