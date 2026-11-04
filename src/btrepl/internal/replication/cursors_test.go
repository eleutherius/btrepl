package replication

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eleutherius/btrepl/internal/btrfs"
)

func TestCursorsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), cursorFile)

	cur, err := loadCursors(path)
	if err != nil || len(cur) != 0 {
		t.Fatalf("missing file: got %v, %v", cur, err)
	}

	a, b := localSnap(0), localSnap(1)
	cur.set("10.0.0.2", "@data", a)
	cur.set("10.0.0.2", "@pg", b)
	cur.set("10.0.0.3", "@data", b)
	cur.set("10.0.0.9", "@data", b)
	cur.clear("10.0.0.2", "@pg")

	// Slaves that are no longer configured are dropped on save.
	if err := cur.save(path, []string{"10.0.0.2", "10.0.0.3"}); err != nil {
		t.Fatal(err)
	}
	got, err := loadCursors(path)
	if err != nil {
		t.Fatal(err)
	}
	want := cursors{
		"10.0.0.2": {"@data": {Name: a.Name, UUID: a.SendUUID()}},
		"10.0.0.3": {"@data": {Name: b.Name, UUID: b.SendUUID()}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLoadCursorsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), cursorFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCursors(path); err == nil {
		t.Fatal("expected an error for a corrupt file")
	}
}

func TestCursorFindsSnapshotByUUID(t *testing.T) {
	a, b := localSnap(0), localSnap(1)
	cur := cursors{}
	cur.set("s", "@data", b)
	c, _ := cur.get("s", "@data")
	if got := c.find([]btrfs.Snapshot{a, b}); got == nil || got.Name != b.Name {
		t.Fatalf("find = %v, want %s", got, b.Name)
	}
	// Same name, different UUID: not the snapshot the slave has.
	renamed := b
	renamed.UUID = "other"
	if got := c.find([]btrfs.Snapshot{a, renamed}); got != nil {
		t.Fatalf("find = %s, want none", got.Name)
	}
}
