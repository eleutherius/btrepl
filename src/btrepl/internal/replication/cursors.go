package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/eleutherius/btrepl/internal/btrfs"
)

// cursorFile sits in the snapshot directory next to the snapshots it refers
// to. Its name does not match the snapshot prefix, so listings skip it.
const cursorFile = ".btrepl-cursors.json"

// cursor is the last snapshot known to exist on both the master and a slave:
// the parent of the next incremental send to that slave. It is persisted so
// that pruning can keep it while the slave is unreachable.
type cursor struct {
	Name string `json:"name"`
	UUID string `json:"uuid"` // SendUUID of the local snapshot
}

// find returns the local snapshot the cursor points to, or nil.
func (c cursor) find(local []btrfs.Snapshot) *btrfs.Snapshot {
	for i := range local {
		if local[i].SendUUID() == c.UUID {
			return &local[i]
		}
	}
	return nil
}

// cursors maps slave -> subvolume -> cursor.
type cursors map[string]map[string]cursor

type cursorState struct {
	Slaves cursors `json:"slaves"`
}

// CursorPath returns the path of the replication cursor file for a snapshot
// directory.
func CursorPath(snapshotDir string) string {
	return filepath.Join(snapshotDir, cursorFile)
}

// loadCursors reads the cursor file; a missing file yields empty cursors.
func loadCursors(path string) (cursors, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cursors{}, nil
	}
	if err != nil {
		return nil, err
	}
	var st cursorState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if st.Slaves == nil {
		st.Slaves = cursors{}
	}
	return st.Slaves, nil
}

func (c cursors) get(slave, subvol string) (cursor, bool) {
	cur, ok := c[slave][subvol]
	return cur, ok
}

func (c cursors) set(slave, subvol string, s btrfs.Snapshot) {
	if c[slave] == nil {
		c[slave] = map[string]cursor{}
	}
	c[slave][subvol] = cursor{Name: s.Name, UUID: s.SendUUID()}
}

func (c cursors) clear(slave, subvol string) {
	delete(c[slave], subvol)
}

// save writes the cursors of the configured slaves atomically.
func (c cursors) save(path string, slaves []string) error {
	st := cursorState{Slaves: cursors{}}
	for slave, subvols := range c {
		if slices.Contains(slaves, slave) && len(subvols) > 0 {
			st.Slaves[slave] = subvols
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
