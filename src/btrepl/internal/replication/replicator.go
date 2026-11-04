package replication

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/eleutherius/btrepl/internal/btrfs"
	"github.com/eleutherius/btrepl/internal/config"
	"github.com/eleutherius/btrepl/internal/remote"
	"github.com/eleutherius/btrepl/internal/sshclient"
)

type Replicator struct {
	cfg      *config.Config
	btrfs    *btrfs.Manager
	cmds     remote.Commands
	log      *slog.Logger
	lockPath string
}

func New(cfg *config.Config, log *slog.Logger) *Replicator {
	bm := btrfs.NewManager(cfg.BtrfsRoot, cfg.SnapshotDir, cfg.SnapshotPrefix)
	return &Replicator{
		cfg: cfg, btrfs: bm, log: log,
		cmds:     remote.New(cfg.BtrfsRoot, cfg.SnapshotDir),
		lockPath: LockPath,
	}
}

// Dial connects to a slave. With trustNewHostKey the slave's host key is
// recorded in ssh_known_hosts if it is not there yet (add-slave); otherwise
// the key must already be there.
func Dial(cfg *config.Config, slave string, trustNewHostKey bool) (*sshclient.Client, error) {
	return sshclient.New(cfg.SlaveAddr(slave), sshclient.Config{
		User:            cfg.SSHUser,
		KeyPath:         cfg.SSHIdentity,
		KnownHosts:      cfg.SSHKnownHosts,
		TrustNewHostKey: trustNewHostKey,
	})
}

// RunCycle runs one cycle in three phases: take one snapshot of every
// subvolume, send the newest snapshot to every slave, then prune local
// snapshots while keeping the last common snapshot with each slave. A failure
// for one slave or subvolume is logged and the cycle goes on; the last error
// is returned. Only one cycle runs at a time on a host: while another holds
// the lock, RunCycle returns ErrCycleRunning.
func (r *Replicator) RunCycle() error {
	if len(r.cfg.Slaves) == 0 {
		r.log.Info("no slaves configured, nothing to replicate")
		return nil
	}
	unlock, err := lock(r.lockPath)
	if err != nil {
		return err
	}
	defer unlock()
	var lastErr error

	for _, subvol := range r.cfg.Subvolumes {
		if err := r.snapshot(subvol); err != nil {
			r.log.Error("snapshot failed", "subvol", subvol, "err", err)
			lastErr = err
		}
	}

	infos, err := r.btrfs.ListSubvolumes()
	if err != nil {
		return fmt.Errorf("list local subvolumes: %w", err)
	}
	local := make(map[string][]btrfs.Snapshot, len(r.cfg.Subvolumes))
	for _, subvol := range r.cfg.Subvolumes {
		local[subvol] = r.btrfs.Snapshots(subvol, infos)
	}

	cursorPath := CursorPath(r.btrfs.SnapshotDirPath())
	cur, cursorErr := loadCursors(cursorPath)
	if cursorErr != nil {
		r.log.Error("read replication cursors, skipping local prune this cycle", "err", cursorErr)
		cur = cursors{}
	}

	for _, slave := range r.cfg.Slaves {
		r.log.Info("replicating", "slave", slave)
		if err := r.replicateSlave(slave, local, cur); err != nil {
			r.log.Error("replication failed", "slave", slave, "err", err)
			lastErr = err
		} else {
			r.log.Info("replication ok", "slave", slave)
		}
	}

	if err := cur.save(cursorPath, r.cfg.Slaves); err != nil {
		r.log.Error("save replication cursors, skipping local prune this cycle", "err", err)
		return err
	}
	if cursorErr != nil {
		return cursorErr
	}
	for _, subvol := range r.cfg.Subvolumes {
		r.pruneLocal(subvol, local[subvol], cur)
	}
	return lastErr
}

func (r *Replicator) snapshot(subvol string) error {
	created, err := r.btrfs.EnsureSubvolume(subvol)
	if err != nil {
		return fmt.Errorf("ensure subvolume: %w", err)
	}
	if created {
		r.log.Warn("subvolume was missing, created an empty one", "subvol", subvol, "path", r.btrfs.SubvolPath(subvol))
	}
	snap, err := r.btrfs.CreateSnapshot(subvol)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	r.log.Info("snapshot created", "snap", snap.Name, "subvol", subvol)
	return nil
}

func (r *Replicator) replicateSlave(slave string, local map[string][]btrfs.Snapshot, cur cursors) error {
	ssh, err := Dial(r.cfg, slave, false)
	if err != nil {
		return fmt.Errorf("connect %s: %w", slave, err)
	}
	defer ssh.Close()

	if err := PrepareSlave(ssh, r.cfg); err != nil {
		return err
	}
	infos, err := listRemoteSubvolumes(ssh, r.cmds)
	if err != nil {
		return err
	}

	var lastErr error
	for _, subvol := range r.cfg.Subvolumes {
		remote := r.btrfs.Snapshots(subvol, infos)
		if err := r.replicateSubvol(ssh, slave, subvol, local[subvol], remote, cur); err != nil {
			r.log.Error("subvol replication failed", "subvol", subvol, "slave", slave, "err", err)
			lastErr = err
		}
	}
	return lastErr
}

// PrepareSlave checks that btrfs_root on the slave is the top level of a
// btrfs filesystem and creates the snapshot directory there.
func PrepareSlave(ssh *sshclient.Client, cfg *config.Config) error {
	cmds := remote.New(cfg.BtrfsRoot, cfg.SnapshotDir)
	mountinfo, err := ssh.Run(cmds.MountInfo())
	if err != nil {
		return fmt.Errorf("read slave mountinfo: %w", err)
	}
	if err := btrfs.CheckMountInfo(mountinfo, cfg.BtrfsRoot); err != nil {
		return fmt.Errorf("slave: %w", err)
	}
	if _, err := ssh.Run(cmds.MkdirSnapshotDir()); err != nil {
		return fmt.Errorf("prepare slave snapshot dir: %w", err)
	}
	return nil
}

func (r *Replicator) replicateSubvol(ssh *sshclient.Client, slave, subvol string, local, remote []btrfs.Snapshot, cur cursors) error {
	complete, garbage := splitRemote(remote, local)
	for _, g := range garbage {
		r.log.Warn("deleting incomplete snapshot on slave (interrupted receive?)",
			"snap", g.Name, "slave", slave, "read_only", g.ReadOnly, "received_uuid", g.ReceivedUUID)
		if _, err := ssh.Run(r.cmds.Delete(g.Name)); err != nil {
			r.log.Warn("delete remote snapshot", "snap", g.Name, "slave", slave, "err", err)
		}
	}

	latest := newestReadOnly(local)
	if latest == nil {
		return fmt.Errorf("no local snapshot of %s to send", subvol)
	}

	parent := latestCommon(local, complete)
	if parent == nil {
		cur.clear(slave, subvol)
	} else {
		cur.set(slave, subvol, *parent)
	}

	if parent != nil && parent.Name == latest.Name {
		r.log.Info("slave is up to date", "snap", latest.Name, "slave", slave)
	} else {
		r.log.Info("sending", "snap", latest.Name, "parent", snapName(parent), "slave", slave)
		if err := ssh.PipeFrom(r.btrfs.SendCmd(*latest, parent), r.cmds.Receive()); err != nil {
			return fmt.Errorf("send %s: %w", latest.Name, err)
		}
		cur.set(slave, subvol, *latest)
		// The received copy has the same name and path on the slave, and is
		// the newest there, so planRemotePrune keeps it.
		complete = append(complete, *latest)
	}

	for _, s := range planRemotePrune(complete, r.cfg.KeepReceiver) {
		if _, err := ssh.Run(r.cmds.Delete(s.Name)); err != nil {
			r.log.Warn("delete remote snapshot", "snap", s.Name, "slave", slave, "err", err)
		} else {
			r.log.Info("pruned remote snapshot", "snap", s.Name, "slave", slave)
		}
	}
	return nil
}

func (r *Replicator) pruneLocal(subvol string, local []btrfs.Snapshot, cur cursors) {
	plan := planLocalPrune(local, r.cfg.KeepSender, subvol, r.cfg.Slaves, cur, r.maxAnchorAge(), time.Now())
	for name, slaves := range plan.Held {
		r.log.Info("keeping snapshot past keep_sender: last common snapshot with slave", "snap", name, "slaves", slaves)
	}
	for _, s := range plan.Delete {
		if slaves := plan.Expired[s.Name]; slaves != nil {
			r.log.Warn("deleting last common snapshot with slave, older than max_anchor_age: the next send to it will be full",
				"snap", s.Name, "slaves", slaves, "max_anchor_age", r.cfg.MaxAnchorAge)
		}
		if err := r.btrfs.DeleteSnapshot(s); err != nil {
			r.log.Warn("delete local snapshot", "snap", s.Name, "err", err)
		} else {
			r.log.Info("pruned local snapshot", "snap", s.Name)
		}
	}
}

func (r *Replicator) maxAnchorAge() time.Duration {
	if r.cfg.MaxAnchorAge == "" {
		return 0
	}
	d, err := time.ParseDuration(r.cfg.MaxAnchorAge)
	if err != nil || d < 0 {
		r.log.Warn("invalid max_anchor_age, keeping anchors without limit", "max_anchor_age", r.cfg.MaxAnchorAge)
		return 0
	}
	return d
}

// listRemoteSubvolumes lists every subvolume of the slave's filesystem.
func listRemoteSubvolumes(ssh *sshclient.Client, cmds remote.Commands) ([]btrfs.SubvolInfo, error) {
	all, err := ssh.Run(cmds.ListSubvolumes())
	if err != nil {
		return nil, fmt.Errorf("list remote subvolumes: %w", err)
	}
	ro, err := ssh.Run(cmds.ListReadOnly())
	if err != nil {
		return nil, fmt.Errorf("list remote subvolumes: %w", err)
	}
	return btrfs.ParseSubvolumeList(all, ro)
}

// LatestReceivedSnapshot returns the path of the newest complete received
// snapshot of subvol on the slave, or "" if there is none.
func (r *Replicator) LatestReceivedSnapshot(ssh *sshclient.Client, subvol string) (string, error) {
	infos, err := listRemoteSubvolumes(ssh, r.cmds)
	if err != nil {
		return "", err
	}
	snaps := r.btrfs.Snapshots(subvol, infos)
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Received() {
			return snaps[i].AbsPath, nil
		}
	}
	return "", nil
}

func snapName(s *btrfs.Snapshot) string {
	if s == nil {
		return "none (full send)"
	}
	return s.Name
}
