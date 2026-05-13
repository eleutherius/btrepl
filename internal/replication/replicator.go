package replication

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/liakhov/btrepl/internal/btrfs"
	"github.com/liakhov/btrepl/internal/config"
	"github.com/liakhov/btrepl/internal/sshclient"
)

type Replicator struct {
	cfg   *config.Config
	btrfs *btrfs.Manager
	log   *slog.Logger
}

func New(cfg *config.Config, log *slog.Logger) *Replicator {
	bm := btrfs.NewManager(cfg.BtrfsRoot, cfg.SnapshotDir, cfg.SnapshotPrefix)
	return &Replicator{cfg: cfg, btrfs: bm, log: log}
}

// RunCycle runs one full replication cycle for all configured slaves.
// It continues to next slave on error, returning the last error.
func (r *Replicator) RunCycle() error {
	if len(r.cfg.Slaves) == 0 {
		r.log.Info("no slaves configured, nothing to replicate")
		return nil
	}
	var lastErr error
	for _, slave := range r.cfg.Slaves {
		r.log.Info("replicating", "slave", slave)
		if err := r.replicateSlave(slave); err != nil {
			r.log.Error("replication failed", "slave", slave, "err", err)
			lastErr = err
		} else {
			r.log.Info("replication ok", "slave", slave)
		}
	}
	return lastErr
}

func (r *Replicator) replicateSlave(slaveIP string) error {
	ssh, err := sshclient.New(slaveIP, r.cfg.SSHUser, r.cfg.SSHIdentity)
	if err != nil {
		return fmt.Errorf("connect %s: %w", slaveIP, err)
	}
	defer ssh.Close()

	remoteSnapDir := filepath.Join(r.cfg.BtrfsRoot, r.cfg.SnapshotDir)
	if _, err := ssh.Run("mkdir -p " + remoteSnapDir); err != nil {
		return fmt.Errorf("prepare slave snapshot dir: %w", err)
	}

	var lastErr error
	for _, subvol := range r.cfg.Subvolumes {
		if err := r.replicateSubvol(ssh, slaveIP, subvol); err != nil {
			r.log.Error("subvol replication failed", "subvol", subvol, "slave", slaveIP, "err", err)
			lastErr = err
		}
	}
	return lastErr
}

func (r *Replicator) replicateSubvol(ssh *sshclient.Client, slaveIP, subvol string) error {
	snap, err := r.btrfs.CreateSnapshot(subvol)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	r.log.Info("snapshot created", "snap", snap.Name, "subvol", subvol)

	localSnaps, err := r.btrfs.ListSnapshots(subvol)
	if err != nil {
		return err
	}
	remoteNames, err := r.listRemoteSnapshots(ssh, subvol)
	if err != nil {
		return err
	}

	parent := r.findParent(localSnaps, remoteNames, snap)
	r.log.Info("sending", "snap", snap.Name, "parent", snapName(parent), "slave", slaveIP)

	sendCmd := r.btrfs.SendCmd(snap, parent)
	remoteSnapDir := filepath.Join(r.cfg.BtrfsRoot, r.cfg.SnapshotDir)
	if err := ssh.PipeFrom(sendCmd, "btrfs receive "+remoteSnapDir); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	if err := r.pruneLocal(subvol, r.cfg.KeepSender); err != nil {
		r.log.Warn("prune local failed", "subvol", subvol, "err", err)
	}
	if err := r.pruneRemote(ssh, subvol, r.cfg.KeepReceiver); err != nil {
		r.log.Warn("prune remote failed", "subvol", subvol, "slave", slaveIP, "err", err)
	}
	return nil
}

func (r *Replicator) listRemoteSnapshots(ssh *sshclient.Client, subvol string) ([]string, error) {
	remoteSnapDir := filepath.Join(r.cfg.BtrfsRoot, r.cfg.SnapshotDir)
	entries, err := ssh.ListDir(remoteSnapDir)
	if err != nil {
		return nil, err
	}
	prefix := r.cfg.SnapshotPrefix + stripAt(subvol) + "_"
	var result []string
	for _, e := range entries {
		if strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	sort.Strings(result)
	return result, nil
}

// findParent returns the latest local snapshot (excluding current) that also
// exists on the remote — used as the incremental send parent.
func (r *Replicator) findParent(local []btrfs.Snapshot, remoteNames []string, current btrfs.Snapshot) *btrfs.Snapshot {
	remoteSet := make(map[string]bool, len(remoteNames))
	for _, n := range remoteNames {
		remoteSet[n] = true
	}
	var parent *btrfs.Snapshot
	for i := range local {
		s := &local[i]
		if s.Name == current.Name {
			continue
		}
		if remoteSet[s.Name] {
			parent = s
		}
	}
	return parent
}

func (r *Replicator) pruneLocal(subvol string, keep int) error {
	snaps, err := r.btrfs.ListSnapshots(subvol)
	if err != nil {
		return err
	}
	for i := 0; i < len(snaps)-keep; i++ {
		if err := r.btrfs.DeleteSnapshot(snaps[i]); err != nil {
			r.log.Warn("delete local snapshot", "snap", snaps[i].Name, "err", err)
		} else {
			r.log.Info("pruned local snapshot", "snap", snaps[i].Name)
		}
	}
	return nil
}

func (r *Replicator) pruneRemote(ssh *sshclient.Client, subvol string, keep int) error {
	remoteNames, err := r.listRemoteSnapshots(ssh, subvol)
	if err != nil {
		return err
	}
	remoteSnapDir := filepath.Join(r.cfg.BtrfsRoot, r.cfg.SnapshotDir)
	for i := 0; i < len(remoteNames)-keep; i++ {
		path := remoteSnapDir + "/" + remoteNames[i]
		if _, err := ssh.Run("btrfs subvolume delete " + path); err != nil {
			r.log.Warn("delete remote snapshot", "snap", remoteNames[i], "err", err)
		} else {
			r.log.Info("pruned remote snapshot", "snap", remoteNames[i])
		}
	}
	return nil
}

// LatestReceivedSnapshot returns the path of the latest received snapshot for
// subvol on the slave — used by standalone restore.
func (r *Replicator) LatestReceivedSnapshot(ssh *sshclient.Client, subvol string) (string, error) {
	remoteNames, err := r.listRemoteSnapshots(ssh, subvol)
	if err != nil || len(remoteNames) == 0 {
		return "", err
	}
	latest := remoteNames[len(remoteNames)-1]
	return filepath.Join(r.cfg.BtrfsRoot, r.cfg.SnapshotDir, latest), nil
}

func snapName(s *btrfs.Snapshot) string {
	if s == nil {
		return "none (full send)"
	}
	return s.Name
}

func stripAt(s string) string {
	return strings.TrimPrefix(s, "@")
}
