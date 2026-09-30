package btrfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CheckRoot verifies that the manager's root is the top level of a dedicated
// btrfs filesystem on this host.
func (m *Manager) CheckRoot() error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}
	return CheckMountInfo(string(data), m.root)
}

// CheckMountInfo verifies, using the contents of /proc/self/mountinfo, that
// path is a btrfs mount point of the filesystem's top level (subvolid=5).
// Incremental `btrfs receive` cannot resolve the parent snapshot otherwise.
func CheckMountInfo(mountinfo, path string) error {
	path = filepath.Clean(path)

	var fsRoot, fsType string
	found := false
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		sep := indexOf(fields, "-")
		if len(fields) < 5 || sep < 0 || sep+1 >= len(fields) {
			continue
		}
		if unescapeMount(fields[4]) != path {
			continue
		}
		// Later entries shadow earlier ones mounted on the same path.
		fsRoot, fsType, found = unescapeMount(fields[3]), fields[sep+1], true
	}

	switch {
	case !found:
		return fmt.Errorf("%s is not a mount point: mount a dedicated btrfs filesystem there with -o subvolid=5", path)
	case fsType != "btrfs":
		return fmt.Errorf("%s is %s, not btrfs", path, fsType)
	case fsRoot != "/":
		return fmt.Errorf("%s is btrfs subvolume %s, not the top level: remount it with -o subvolid=5", path, fsRoot)
	}
	return nil
}

func indexOf(fields []string, s string) int {
	for i, f := range fields {
		if f == s {
			return i
		}
	}
	return -1
}

// unescapeMount decodes the octal escapes (\040 for space etc.) used in mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
