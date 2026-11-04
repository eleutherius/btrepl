package replication

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eleutherius/btrepl/internal/btrfs"
)

var t0 = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

// localSnap is a snapshot taken on the master: read-only, no received UUID.
func localSnap(hour int) btrfs.Snapshot {
	name := "btrepl_data_" + t0.Add(time.Duration(hour)*time.Hour).Format("20060102T150405")
	return btrfs.Snapshot{
		Name: name, Subvol: "@data", Time: t0.Add(time.Duration(hour) * time.Hour),
		AbsPath: filepath.Join("/btrfs/.snapshots", name),
		UUID:    "local-" + name, ReadOnly: true,
	}
}

// received is the copy of s on a slave after a complete receive.
func received(s btrfs.Snapshot) btrfs.Snapshot {
	r := s
	r.UUID, r.ReceivedUUID, r.ReadOnly = "remote-"+s.Name, s.SendUUID(), true
	return r
}

// partial is what an interrupted receive of s leaves on a slave.
func partial(s btrfs.Snapshot) btrfs.Snapshot {
	r := s
	r.UUID, r.ReceivedUUID, r.ReadOnly = "partial-"+s.Name, "", false
	return r
}

func names(snaps []btrfs.Snapshot) []string {
	out := []string{}
	for _, s := range snaps {
		out = append(out, s.Name)
	}
	return out
}

func TestLatestCommonMatchesByUUID(t *testing.T) {
	a, b, c := localSnap(0), localSnap(1), localSnap(2)
	local := []btrfs.Snapshot{a, b, c}

	// b exists on the slave under its name, but only as a partial receive.
	complete, _ := splitRemote([]btrfs.Snapshot{received(a), partial(b)}, local)
	if got := latestCommon(local, complete); got == nil || got.Name != a.Name {
		t.Fatalf("parent = %v, want %s", got, a.Name)
	}
}

func TestLatestCommonIgnoresSameNameWithOtherUUID(t *testing.T) {
	a := localSnap(0)
	other := received(a)
	other.ReceivedUUID = "some-other-master"
	if got := latestCommon([]btrfs.Snapshot{a}, []btrfs.Snapshot{other}); got != nil {
		t.Fatalf("parent = %s, want none", got.Name)
	}
}

func TestLatestCommonNone(t *testing.T) {
	if got := latestCommon([]btrfs.Snapshot{localSnap(0)}, nil); got != nil {
		t.Fatalf("parent = %s, want none", got.Name)
	}
}

func TestSplitRemote(t *testing.T) {
	a, b := localSnap(0), localSnap(1)
	roNotReceived := received(b)
	roNotReceived.Name, roNotReceived.ReceivedUUID = "btrepl_data_20261104T120000", ""
	half := received(a)
	half.Name, half.ReadOnly = "btrepl_data_20261104T130000", false // received UUID set, read-only flag not yet

	complete, garbage := splitRemote([]btrfs.Snapshot{received(a), partial(b), roNotReceived, half}, nil)
	if got := names(complete); !reflect.DeepEqual(got, []string{a.Name}) {
		t.Errorf("complete = %v", got)
	}
	if got := names(garbage); !reflect.DeepEqual(got, []string{b.Name, roNotReceived.Name, half.Name}) {
		t.Errorf("garbage = %v", got)
	}
}

func TestSplitRemoteNeverDeletesOwnSnapshots(t *testing.T) {
	// A slave address that points back at the master lists the master's own
	// snapshots: they have no received UUID but must not be deleted.
	a := localSnap(0)
	complete, garbage := splitRemote([]btrfs.Snapshot{a}, []btrfs.Snapshot{a})
	if len(complete) != 0 || len(garbage) != 0 {
		t.Fatalf("complete = %v, garbage = %v", names(complete), names(garbage))
	}
}

func TestNewestReadOnly(t *testing.T) {
	a, b := localSnap(0), localSnap(1)
	b.ReadOnly = false
	if got := newestReadOnly([]btrfs.Snapshot{a, b}); got == nil || got.Name != a.Name {
		t.Fatalf("newest = %v, want %s", got, a.Name)
	}
	if got := newestReadOnly(nil); got != nil {
		t.Fatalf("newest = %s, want none", got.Name)
	}
}

func TestPlanRemotePrune(t *testing.T) {
	var remote []btrfs.Snapshot
	for h := 0; h < 5; h++ {
		remote = append(remote, received(localSnap(h)))
	}
	if got := names(planRemotePrune(remote, 2)); !reflect.DeepEqual(got, names(remote[:3])) {
		t.Errorf("keep 2: delete %v", got)
	}
	// The newest snapshot is the next parent and is always kept.
	if got := names(planRemotePrune(remote, 0)); !reflect.DeepEqual(got, names(remote[:4])) {
		t.Errorf("keep 0: delete %v", got)
	}
	if got := planRemotePrune(remote, 10); len(got) != 0 {
		t.Errorf("keep 10: delete %v", names(got))
	}
}

func TestPlanLocalPruneKeepsAnchorOfOfflineSlave(t *testing.T) {
	var local []btrfs.Snapshot
	for h := 0; h < 6; h++ {
		local = append(local, localSnap(h))
	}
	cur := cursors{}
	cur.set("10.0.0.2", "@data", local[5]) // up to date
	cur.set("10.0.0.3", "@data", local[1]) // offline since hour 1

	p := planLocalPrune(local, 2, "@data", []string{"10.0.0.2", "10.0.0.3"}, cur, 0, t0.Add(6*time.Hour))
	if got, want := names(p.Delete), []string{local[0].Name, local[2].Name, local[3].Name}; !reflect.DeepEqual(got, want) {
		t.Errorf("delete = %v, want %v", got, want)
	}
	if got := p.Held; !reflect.DeepEqual(got, map[string][]string{local[1].Name: {"10.0.0.3"}}) {
		t.Errorf("held = %v", got)
	}
}

func TestPlanLocalPruneIgnoresRemovedSlaves(t *testing.T) {
	local := []btrfs.Snapshot{localSnap(0), localSnap(1), localSnap(2)}
	cur := cursors{}
	cur.set("10.0.0.9", "@data", local[0]) // no longer in config

	p := planLocalPrune(local, 1, "@data", []string{"10.0.0.2"}, cur, 0, t0)
	if got := names(p.Delete); !reflect.DeepEqual(got, names(local[:2])) {
		t.Errorf("delete = %v", got)
	}
}

func TestPlanLocalPruneReleasesAnchorsOlderThanMaxAge(t *testing.T) {
	local := []btrfs.Snapshot{localSnap(0), localSnap(1), localSnap(2), localSnap(3)}
	cur := cursors{}
	cur.set("10.0.0.3", "@data", local[0])

	now := t0.Add(48 * time.Hour)
	p := planLocalPrune(local, 1, "@data", []string{"10.0.0.3"}, cur, 24*time.Hour, now)
	if got := names(p.Delete); !reflect.DeepEqual(got, names(local[:3])) {
		t.Errorf("delete = %v", got)
	}
	if got := p.Expired; !reflect.DeepEqual(got, map[string][]string{local[0].Name: {"10.0.0.3"}}) {
		t.Errorf("expired = %v", got)
	}

	// Younger than max_anchor_age: still held.
	p = planLocalPrune(local, 1, "@data", []string{"10.0.0.3"}, cur, 72*time.Hour, now)
	if got := names(p.Delete); !reflect.DeepEqual(got, names(local[1:3])) {
		t.Errorf("delete = %v", got)
	}
}

func TestPlanLocalPruneAlwaysKeepsNewest(t *testing.T) {
	local := []btrfs.Snapshot{localSnap(0), localSnap(1)}
	p := planLocalPrune(local, 0, "@data", nil, cursors{}, 0, t0)
	if got := names(p.Delete); !reflect.DeepEqual(got, names(local[:1])) {
		t.Errorf("delete = %v", got)
	}
}
