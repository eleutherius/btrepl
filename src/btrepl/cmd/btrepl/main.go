package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	pb "github.com/eleutherius/btrepl/api"
	"github.com/eleutherius/btrepl/internal/btrfs"
	"github.com/eleutherius/btrepl/internal/config"
	"github.com/eleutherius/btrepl/internal/logbroadcast"
	"github.com/eleutherius/btrepl/internal/remote"
	"github.com/eleutherius/btrepl/internal/replication"
	"github.com/eleutherius/btrepl/internal/server"
	"github.com/eleutherius/btrepl/internal/systemd"
)

const (
	timerUnit   = "btrepl.timer"
	serviceUnit = "btrepl.service"
)

var cfgPath string

func main() {
	bc := logbroadcast.New(slog.NewTextHandler(os.Stderr, nil))
	log := slog.New(bc)
	slog.SetDefault(log)

	root := &cobra.Command{
		Use:   "btrepl",
		Short: "btrfs geo-replication manager",
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", config.DefaultPath, "config file")

	root.AddCommand(
		cmdServe(log, bc),
		cmdInitMaster(log),
		cmdAddSlave(log),
		cmdDelSlave(log),
		cmdStandalone(log),
		cmdClear(log),
		cmdStatus(log),
		cmdRun(log),
		cmdSSHFilter(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- helpers -----------------------------------------------------------------

func loadCfg() (*config.Config, error) {
	return config.Load(cfgPath)
}

func saveCfg(cfg *config.Config) error {
	return config.Save(cfg, cfgPath)
}

func newBtrfs(cfg *config.Config) *btrfs.Manager {
	return btrfs.NewManager(cfg.BtrfsRoot, cfg.SnapshotDir, cfg.SnapshotPrefix)
}

// --- commands ----------------------------------------------------------------

func cmdInitMaster(log *slog.Logger) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init-master",
		Short: "Initialize this node as replication master",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if os.IsNotExist(err) {
				cfg = config.Default()
			} else if err != nil {
				return err
			}

			bm := newBtrfs(cfg)
			if err := bm.CheckRoot(); err != nil {
				if !force {
					return fmt.Errorf("%w (use --force to skip this check)", err)
				}
				log.Warn("btrfs root check skipped", "err", err)
			}
			if err := bm.EnsureSnapshotDir(); err != nil {
				return err
			}
			for _, subvol := range cfg.Subvolumes {
				created, err := bm.EnsureSubvolume(subvol)
				if err != nil {
					log.Warn("subvolume not ready", "subvol", subvol, "err", err)
				} else if created {
					log.Info("subvolume created", "subvol", subvol, "path", bm.SubvolPath(subvol))
				}
			}
			if err := bm.EnableQuota(); err != nil {
				log.Warn("enable btrfs quota failed", "err", err)
			}
			if err := saveCfg(cfg); err != nil {
				return err
			}
			if err := systemd.WriteTimer(cfg.Interval); err != nil {
				log.Warn("write timer unit", "err", err)
			} else {
				_ = systemd.DaemonReload()
			}
			log.Info("master initialized", "config", cfgPath, "interval", cfg.Interval)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the check that btrfs_root is the top level of a btrfs mount")
	return cmd
}

func cmdAddSlave(log *slog.Logger) *cobra.Command {
	var slaveIP string
	var noRestrict bool
	cmd := &cobra.Command{
		Use:   "add-slave",
		Short: "Add a slave node to replication",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}

			ssh, err := replication.Dial(cfg, slaveIP, true)
			if err != nil {
				return fmt.Errorf("connect to slave: %w", err)
			}
			defer ssh.Close()
			if ssh.NewHostKey != "" {
				log.Info("recorded slave host key", "slave", slaveIP, "fingerprint", ssh.NewHostKey, "known_hosts", cfg.SSHKnownHosts)
			}

			if err := replication.PrepareSlave(ssh, cfg); err != nil {
				return fmt.Errorf("prepare slave: %w", err)
			}

			cfg.AddSlave(slaveIP)
			if err := saveCfg(cfg); err != nil {
				return err
			}

			if !noRestrict {
				if err := replication.RestrictKeyAndLog(ssh, cfg, slaveIP, log); err != nil {
					return err
				}
			}

			if err := systemd.Enable(timerUnit); err != nil {
				log.Warn("enable timer", "err", err)
			}
			if err := systemd.Start(timerUnit); err != nil {
				log.Warn("start timer", "err", err)
			}

			log.Info("slave added", "slave", slaveIP)
			return nil
		},
	}
	cmd.Flags().StringVarP(&slaveIP, "server", "s", "", "slave IP or hostname, optionally with :port")
	cmd.Flags().BoolVar(&noRestrict, "no-restrict", false, "keep the master's key on the slave unrestricted instead of limiting it to btrepl ssh-filter")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

func cmdDelSlave(log *slog.Logger) *cobra.Command {
	var slaveIP string
	cmd := &cobra.Command{
		Use:   "del-slave",
		Short: "Remove a slave node from replication",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			if !cfg.RemoveSlave(slaveIP) {
				return fmt.Errorf("slave %s not found in config", slaveIP)
			}
			if err := saveCfg(cfg); err != nil {
				return err
			}
			if len(cfg.Slaves) == 0 {
				if err := systemd.Stop(timerUnit); err != nil {
					log.Warn("stop timer", "err", err)
				}
				if err := systemd.Disable(timerUnit); err != nil {
					log.Warn("disable timer", "err", err)
				}
			}
			log.Info("slave removed", "slave", slaveIP)
			return nil
		},
	}
	cmd.Flags().StringVarP(&slaveIP, "server", "s", "", "slave IP or hostname")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

func cmdStandalone(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "standalone",
		Short: "Detach this node from replication and restore data from latest snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if os.IsNotExist(err) {
				cfg = config.Default()
				log.Warn("config not found, using defaults", "btrfs_root", cfg.BtrfsRoot)
			} else if err != nil {
				return err
			}

			_ = systemd.Stop(timerUnit)
			_ = systemd.Disable(timerUnit)
			_ = systemd.Stop(serviceUnit)
			_ = systemd.Disable(serviceUnit)

			bm := newBtrfs(cfg)
			subvols := cfg.Subvolumes
			if len(subvols) == 0 {
				subvols, _ = bm.ListAllSubvolumes()
				log.Info("discovered subvolumes from snapshots", "subvols", subvols)
			}
			infos, err := bm.ListSubvolumes()
			if err != nil {
				return err
			}
			for _, subvol := range subvols {
				latest := latestReceived(bm.Snapshots(subvol, infos), func(s btrfs.Snapshot) {
					log.Warn("skipping incomplete snapshot (interrupted receive?)", "snap", s.Name)
				})
				if latest == nil {
					log.Warn("no complete received snapshot to restore", "subvol", subvol)
					continue
				}
				kept, err := bm.Restore(*latest, subvol)
				if err != nil {
					log.Error("restore snapshot", "snap", latest.Name, "subvol", subvol, "err", err)
					continue
				}
				if kept != "" {
					log.Warn("previous data could not be deleted, remove it by hand", "subvol", subvol, "path", kept)
				}
				log.Info("restored", "subvol", subvol, "from", latest.Name)
			}

			if err := bm.EnableQuota(); err != nil {
				log.Warn("enable quota", "err", err)
			}
			log.Info("node is now standalone")
			return nil
		},
	}
}

func cmdClear(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Stop replication and delete all local snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if os.IsNotExist(err) {
				cfg = config.Default()
			} else if err != nil {
				return err
			}

			_ = systemd.Stop(timerUnit)
			_ = systemd.Disable(timerUnit)

			bm := newBtrfs(cfg)
			for _, subvol := range cfg.Subvolumes {
				snaps, err := bm.ListSnapshots(subvol)
				if err != nil {
					continue
				}
				for _, s := range snaps {
					if err := bm.DeleteSnapshot(s); err != nil {
						log.Warn("delete snapshot", "snap", s.Name, "err", err)
					}
				}
			}
			if err := os.Remove(replication.CursorPath(bm.SnapshotDirPath())); err != nil && !os.IsNotExist(err) {
				log.Warn("delete replication cursors", "err", err)
			}

			cfg.Slaves = nil
			_ = saveCfg(cfg)
			log.Info("cleared")
			return nil
		},
	}
}

func cmdStatus(log *slog.Logger) *cobra.Command {
	var slaveIP string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show replication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}

			active, _ := systemd.IsActive(timerUnit)
			fmt.Printf("timer  (%s): %s\n", timerUnit, boolStatus(active))
			fmt.Printf("slaves: %v\n", cfg.Slaves)

			bm := newBtrfs(cfg)
			for _, subvol := range cfg.Subvolumes {
				snaps, _ := bm.ListSnapshots(subvol)
				latest := "-"
				if len(snaps) > 0 {
					latest = snaps[len(snaps)-1].Name
				}
				fmt.Printf("  %-20s  snapshots: %d  latest: %s\n", subvol, len(snaps), latest)
			}

			if slaveIP != "" {
				ssh, err := replication.Dial(cfg, slaveIP, false)
				if err != nil {
					log.Warn("connect to slave for status", "slave", slaveIP, "err", err)
					return nil
				}
				defer ssh.Close()

				r := replication.New(cfg, log)
				for _, subvol := range cfg.Subvolumes {
					latest, err := r.LatestReceivedSnapshot(ssh, subvol)
					if err != nil {
						fmt.Printf("  remote %-20s  error: %v\n", subvol, err)
					} else {
						fmt.Printf("  remote %-20s  latest: %s\n", subvol, latest)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&slaveIP, "server", "s", "", "slave IP for remote snapshot info")
	return cmd
}

func cmdRun(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run one replication cycle (invoked by systemd timer)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			r := replication.New(cfg, log)
			return r.RunCycle()
		},
	}
}

func cmdServe(log *slog.Logger, bc *logbroadcast.Broadcaster) *cobra.Command {
	var addr string
	var files server.TLSFiles
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start gRPC daemon with internal replication loop",
		Long: `Start the gRPC daemon with the internal replication loop.

By default the API listens on a Unix socket that only root can open. To
listen on TCP, give a host:port and the files for mutual TLS: clients must
present a certificate signed by --tls-client-ca.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			lis, opts, err := server.Listen(addr, files)
			if err != nil {
				return err
			}

			srv := server.New(cfgPath, log, bc)
			grpcSrv := grpc.NewServer(opts...)
			pb.RegisterBtreplServer(grpcSrv, srv)

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			go srv.RunLoop(ctx)

			go func() {
				<-ctx.Done()
				grpcSrv.GracefulStop()
			}()

			log.Info("gRPC server listening", "addr", addr, "mutual_tls", len(opts) > 0)
			return grpcSrv.Serve(lis)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", server.DefaultAddr, "gRPC listen address: unix:<path>, or host:port with mutual TLS")
	cmd.Flags().StringVar(&files.Cert, "tls-cert", "", "server certificate (PEM) for a TCP address")
	cmd.Flags().StringVar(&files.Key, "tls-key", "", "server private key (PEM) for a TCP address")
	cmd.Flags().StringVar(&files.ClientCA, "tls-client-ca", "", "CA certificate (PEM) that signs allowed client certificates")
	return cmd
}

// latestReceived returns the newest complete received snapshot, the only kind
// standalone may promote, and reports newer incomplete ones through skip.
func latestReceived(snaps []btrfs.Snapshot, skip func(btrfs.Snapshot)) *btrfs.Snapshot {
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Received() {
			return &snaps[i]
		}
		skip(snaps[i])
	}
	return nil
}

func cmdSSHFilter() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-filter",
		Short: "Forced SSH command for a slave: allow only the commands btrepl runs there",
		Long: `Restricts the master's key on a slave to the commands btrepl runs there.
Put it in front of the key in the slave's authorized_keys:

  restrict,command="/usr/local/bin/btrepl ssh-filter" ssh-ed25519 AAAA... root@master

btrfs_root and snapshot_dir are read from the slave's config (-c), or the
defaults if there is none. Other commands are refused.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if os.IsNotExist(err) {
				cfg = config.Default()
			} else if err != nil {
				return err
			}
			argv, err := remote.New(cfg.BtrfsRoot, cfg.SnapshotDir).Allow(os.Getenv("SSH_ORIGINAL_COMMAND"))
			if err != nil {
				return err
			}
			path, err := exec.LookPath(argv[0])
			if err != nil {
				return err
			}
			return syscall.Exec(path, argv, os.Environ())
		},
	}
}

func boolStatus(b bool) string {
	if b {
		return "active"
	}
	return "inactive"
}
