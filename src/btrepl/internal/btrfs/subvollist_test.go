package btrfs

import (
	"reflect"
	"testing"
	"time"
)

// Output of btrfs-progs v6.14 after one complete and one interrupted receive
// into rcv/ (see the partial receive in issue #1).
const listAll = `ID 256 gen 12 top level 5 received_uuid -                                    uuid 5bee83ba-8d04-bc4e-b94f-1b3a73496eb9 path @data
ID 257 gen 9 top level 5 received_uuid -                                    uuid ca172933-a364-c84d-9767-911791a3d0a8 path .snapshots/btrepl_data_20261104T100000
ID 258 gen 13 top level 5 received_uuid ca172933-a364-c84d-9767-911791a3d0a8 uuid 2a77e06f-809d-8841-a0f1-34f078559b55 path rcv/btrepl_data_20261104T100000
ID 260 gen 13 top level 5 received_uuid -                                    uuid 672548ba-1c84-1545-bc09-f09477a98058 path rcv/btrepl_data_20261104T110000
ID 261 gen 14 top level 5 received_uuid -                                    uuid 11111111-2222-3333-4444-555555555555 path dir with spaces/sub vol
`

const listReadOnly = `ID 257 gen 9 top level 5 path .snapshots/btrepl_data_20261104T100000
ID 258 gen 13 top level 5 path rcv/btrepl_data_20261104T100000
`

func TestParseSubvolumeList(t *testing.T) {
	got, err := ParseSubvolumeList(listAll, listReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	want := []SubvolInfo{
		{Path: "@data", UUID: "5bee83ba-8d04-bc4e-b94f-1b3a73496eb9"},
		{Path: ".snapshots/btrepl_data_20261104T100000", UUID: "ca172933-a364-c84d-9767-911791a3d0a8", ReadOnly: true},
		{Path: "rcv/btrepl_data_20261104T100000", UUID: "2a77e06f-809d-8841-a0f1-34f078559b55", ReceivedUUID: "ca172933-a364-c84d-9767-911791a3d0a8", ReadOnly: true},
		{Path: "rcv/btrepl_data_20261104T110000", UUID: "672548ba-1c84-1545-bc09-f09477a98058"},
		{Path: "dir with spaces/sub vol", UUID: "11111111-2222-3333-4444-555555555555"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseSubvolumeListEmpty(t *testing.T) {
	got, err := ParseSubvolumeList("", "")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestParseSubvolumeListMissingUUID(t *testing.T) {
	if _, err := ParseSubvolumeList("ID 256 gen 12 top level 5 path @data\n", ""); err == nil {
		t.Fatal("expected an error for a line without uuid")
	}
}

func TestSnapshotsFiltersAndSorts(t *testing.T) {
	m := NewManager("/btrfs", "rcv", "btrepl_")
	infos, err := ParseSubvolumeList(listAll, listReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	infos = append(infos,
		SubvolInfo{Path: "rcv/btrepl_data_notatime", UUID: "x"},
		SubvolInfo{Path: "rcv/btrepl_other_20261104T090000", UUID: "y"},
		SubvolInfo{Path: "elsewhere/btrepl_data_20261104T090000", UUID: "z"},
	)
	got := m.Snapshots("@data", infos)
	if len(got) != 2 {
		t.Fatalf("want 2 snapshots, got %+v", got)
	}
	complete, partial := got[0], got[1]
	if complete.Name != "btrepl_data_20261104T100000" || !complete.Received() {
		t.Errorf("first snapshot should be the complete received one: %+v", complete)
	}
	if complete.AbsPath != "/btrfs/rcv/btrepl_data_20261104T100000" {
		t.Errorf("AbsPath = %s", complete.AbsPath)
	}
	if !complete.Time.Equal(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("Time = %s", complete.Time)
	}
	if partial.Name != "btrepl_data_20261104T110000" || partial.Received() || partial.ReadOnly {
		t.Errorf("second snapshot should be the partial receive: %+v", partial)
	}
}

func TestSnapshotsMatchesNestedRoot(t *testing.T) {
	// With init-master --force btrfs_root may be a subvolume mount; paths from
	// `btrfs subvolume list` are then prefixed with that subvolume.
	m := NewManager("/btrfs", ".snapshots", "btrepl_")
	got := m.Snapshots("@data", []SubvolInfo{{Path: "@root/.snapshots/btrepl_data_20261104T100000", UUID: "u", ReadOnly: true}})
	if len(got) != 1 {
		t.Fatalf("want 1 snapshot, got %+v", got)
	}
}

func TestSendUUID(t *testing.T) {
	if got := (Snapshot{UUID: "a"}).SendUUID(); got != "a" {
		t.Errorf("SendUUID = %s, want a", got)
	}
	// A snapshot that was itself received is sent under its received UUID.
	if got := (Snapshot{UUID: "a", ReceivedUUID: "b"}).SendUUID(); got != "b" {
		t.Errorf("SendUUID = %s, want b", got)
	}
}
