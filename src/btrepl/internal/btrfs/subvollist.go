package btrfs

import (
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// SubvolInfo is one subvolume as reported by `btrfs subvolume list`.
type SubvolInfo struct {
	Path         string // relative to the filesystem top level
	UUID         string
	ReceivedUUID string // empty unless the subvolume was created by btrfs receive
	ReadOnly     bool
}

// ListAllArgs and ListReadOnlyArgs are the `btrfs` arguments (before the
// filesystem path) whose outputs ParseSubvolumeList expects.
var (
	ListAllArgs      = []string{"subvolume", "list", "-u", "-R"}
	ListReadOnlyArgs = []string{"subvolume", "list", "-r"}
)

// ParseSubvolumeList parses the output of `btrfs subvolume list -u -R` (all)
// and `btrfs subvolume list -r` (readOnly) taken on the same filesystem.
func ParseSubvolumeList(all, readOnly string) ([]SubvolInfo, error) {
	ro := make(map[string]bool)
	for _, line := range strings.Split(readOnly, "\n") {
		if _, p, ok := splitPath(line); ok {
			ro[p] = true
		}
	}

	var infos []SubvolInfo
	for _, line := range strings.Split(all, "\n") {
		head, p, ok := splitPath(line)
		if !ok {
			continue
		}
		fields := strings.Fields(head)
		uuid := listField(fields, "uuid")
		if uuid == "" {
			return nil, fmt.Errorf("no uuid in btrfs subvolume list line %q", line)
		}
		infos = append(infos, SubvolInfo{
			Path:         p,
			UUID:         uuid,
			ReceivedUUID: listField(fields, "received_uuid"),
			ReadOnly:     ro[p],
		})
	}
	return infos, nil
}

// splitPath splits a `btrfs subvolume list` line into the key/value part and
// the path, which comes last and may contain spaces.
func splitPath(line string) (head, p string, ok bool) {
	line = strings.TrimRight(line, "\r")
	i := strings.Index(line, " path ")
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i+len(" path "):], true
}

// listField returns the value after key, or "" when it is missing or "-".
func listField(fields []string, key string) string {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == key {
			if v := fields[i+1]; v != "-" {
				return v
			}
			return ""
		}
	}
	return ""
}

// ListSubvolumes lists every subvolume of the local filesystem at the root.
func (m *Manager) ListSubvolumes() ([]SubvolInfo, error) {
	all, err := btrfsOutput(slices.Concat(ListAllArgs, []string{m.root})...)
	if err != nil {
		return nil, err
	}
	ro, err := btrfsOutput(slices.Concat(ListReadOnlyArgs, []string{m.root})...)
	if err != nil {
		return nil, err
	}
	return ParseSubvolumeList(all, ro)
}

// Snapshots picks the snapshots of subvol in the snapshot directory out of
// infos, sorted oldest first. infos may come from this host or a slave: both
// use the same btrfs_root and snapshot_dir, so AbsPath is valid on either.
func (m *Manager) Snapshots(subvol string, infos []SubvolInfo) []Snapshot {
	prefix := m.snapshotName(subvol, "")
	dir := strings.Trim(path.Clean("/"+filepath.ToSlash(m.snapshotDir)), "/")

	var snaps []Snapshot
	for _, info := range infos {
		name := path.Base(info.Path)
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix("/"+info.Path, "/"+dir+"/"+name) {
			continue
		}
		t, err := time.ParseInLocation(timestampLayout, strings.TrimPrefix(name, prefix), time.UTC)
		if err != nil {
			continue
		}
		snaps = append(snaps, Snapshot{
			Name:         name,
			Subvol:       subvol,
			Time:         t,
			AbsPath:      filepath.Join(m.SnapshotDirPath(), name),
			UUID:         info.UUID,
			ReceivedUUID: info.ReceivedUUID,
			ReadOnly:     info.ReadOnly,
		})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.Before(snaps[j].Time) })
	return snaps
}

func btrfsOutput(args ...string) (string, error) {
	cmd := exec.Command("btrfs", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("btrfs %s: %w\n%s", strings.Join(args, " "), err, ee.Stderr)
		}
		return "", fmt.Errorf("btrfs %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
