package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	pb "github.com/eleutherius/btrepl/api"
	"github.com/eleutherius/btrepl/internal/btrfs"
	"github.com/eleutherius/btrepl/internal/config"
	"github.com/eleutherius/btrepl/internal/logbroadcast"
	"github.com/eleutherius/btrepl/internal/replication"
	"github.com/eleutherius/btrepl/internal/server"
	"github.com/eleutherius/btrepl/internal/sshclient"
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
	return &cobra.Command{
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
			if err := bm.EnsureSnapshotDir(); err != nil {
				return err
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
}

func cmdAddSlave(log *slog.Logger) *cobra.Command {
	var slaveIP string
	cmd := &cobra.Command{
		Use:   "add-slave",
		Short: "Add a slave node to replication",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}

			ssh, err := sshclient.New(slaveIP, cfg.SSHUser, cfg.SSHIdentity)
			if err != nil {
				return fmt.Errorf("connect to slave: %w", err)
			}
			defer ssh.Close()

			// Ensure snapshot dir exists on slave.
			snapDir := cfg.BtrfsRoot + "/" + cfg.SnapshotDir
			if _, err := ssh.Run("mkdir -p " + snapDir); err != nil {
				return fmt.Errorf("prepare slave: %w", err)
			}

			cfg.AddSlave(slaveIP)
			if err := saveCfg(cfg); err != nil {
				return err
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
	cmd.Flags().StringVarP(&slaveIP, "server", "s", "", "slave IP or hostname")
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
			for _, subvol := range subvols {
				snaps, err := bm.ListSnapshots(subvol)
				if err != nil || len(snaps) == 0 {
					log.Warn("no snapshots to restore", "subvol", subvol)
					continue
				}
				latest := snaps[len(snaps)-1]
				dst := bm.SubvolPath(subvol)

				exists, _ := bm.SubvolumeExists(dst)
				if exists {
					if err := bm.DeleteSubvolume(dst); err != nil {
						log.Warn("delete existing subvol", "subvol", subvol, "err", err)
					}
				}
				if err := bm.WritableSnapshot(latest.AbsPath, dst); err != nil {
					log.Error("restore snapshot", "snap", latest.Name, "dst", dst, "err", err)
				} else {
					log.Info("restored", "subvol", subvol, "from", latest.Name)
				}
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
				ssh, err := sshclient.New(slaveIP, cfg.SSHUser, cfg.SSHIdentity)
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
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start gRPC daemon with internal replication loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			lis, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}

			srv := server.New(cfgPath, log, bc)
			grpcSrv := grpc.NewServer()
			pb.RegisterBtreplServer(grpcSrv, srv)

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			go srv.RunLoop(ctx)

			go func() {
				<-ctx.Done()
				grpcSrv.GracefulStop()
			}()

			log.Info("gRPC server listening", "addr", addr)
			return grpcSrv.Serve(lis)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":50051", "gRPC listen address")
	return cmd
}

func boolStatus(b bool) string {
	if b {
		return "active"
	}
	return "inactive"
}
