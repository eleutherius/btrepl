package replication

import (
	"time"

	"github.com/eleutherius/btrepl/internal/btrfs"
)

// latestCommon returns the newest local snapshot that a complete remote
// snapshot was received from, or nil. It matches by UUID, as btrfs receive
// does when it resolves the parent of an incremental stream.
func latestCommon(local, remote []btrfs.Snapshot) *btrfs.Snapshot {
	received := make(map[string]bool, len(remote))
	for _, s := range remote {
		if s.Received() {
			received[s.ReceivedUUID] = true
		}
	}
	for i := len(local) - 1; i >= 0; i-- {
		if local[i].ReadOnly && received[local[i].SendUUID()] {
			return &local[i]
		}
	}
	return nil
}

// newestReadOnly returns the newest read-only snapshot, or nil.
func newestReadOnly(snaps []btrfs.Snapshot) *btrfs.Snapshot {
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].ReadOnly {
			return &snaps[i]
		}
	}
	return nil
}

// splitRemote separates complete received snapshots on a slave from garbage:
// leftovers of an interrupted receive (writable) and snapshots that were not
// received at all. Snapshots whose UUID is one of the local ones are this
// host's own (the slave address points back at the master) and are left out
// of both.
func splitRemote(remote, local []btrfs.Snapshot) (complete, garbage []btrfs.Snapshot) {
	own := make(map[string]bool, len(local))
	for _, s := range local {
		own[s.UUID] = true
	}
	for _, s := range remote {
		switch {
		case own[s.UUID]:
		case s.Received():
			complete = append(complete, s)
		default:
			garbage = append(garbage, s)
		}
	}
	return complete, garbage
}

// planRemotePrune returns the oldest complete remote snapshots beyond keep.
// The newest one is the parent of the next send and is always kept.
func planRemotePrune(complete []btrfs.Snapshot, keep int) []btrfs.Snapshot {
	keep = max(keep, 1)
	if len(complete) <= keep {
		return nil
	}
	return complete[:len(complete)-keep]
}

type localPrunePlan struct {
	Delete []btrfs.Snapshot
	// Held maps snapshots past keep_sender that are kept because they are the
	// last common snapshot with the listed slaves.
	Held map[string][]string
	// Expired maps snapshots that are deleted although they are the last
	// common snapshot with the listed slaves, because they are older than
	// max_anchor_age.
	Expired map[string][]string
}

// planLocalPrune picks the local snapshots to delete: everything but the
// newest keep, except the last common snapshot with each configured slave
// (its anchor) unless the anchor is older than maxAge (0 means no limit).
func planLocalPrune(local []btrfs.Snapshot, keep int, subvol string, slaves []string, cur cursors, maxAge time.Duration, now time.Time) localPrunePlan {
	anchors := map[string][]string{}
	expired := map[string][]string{}
	for _, slave := range slaves {
		c, ok := cur.get(slave, subvol)
		if !ok {
			continue
		}
		s := c.find(local)
		if s == nil {
			continue
		}
		if maxAge > 0 && now.Sub(s.Time) > maxAge {
			expired[s.Name] = append(expired[s.Name], slave)
			continue
		}
		anchors[s.Name] = append(anchors[s.Name], slave)
	}

	plan := localPrunePlan{Held: map[string][]string{}, Expired: map[string][]string{}}
	keep = max(keep, 1)
	for i := 0; i < len(local)-keep; i++ {
		s := local[i]
		if held := anchors[s.Name]; held != nil {
			plan.Held[s.Name] = held
			continue
		}
		if e := expired[s.Name]; e != nil {
			plan.Expired[s.Name] = e
		}
		plan.Delete = append(plan.Delete, s)
	}
	return plan
}
