package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "github.com/eleutherius/btrepl/api"
	"github.com/eleutherius/btrepl/internal/btrfs"
	"github.com/eleutherius/btrepl/internal/config"
	"github.com/eleutherius/btrepl/internal/logbroadcast"
	"github.com/eleutherius/btrepl/internal/replication"
	"github.com/eleutherius/btrepl/internal/sshclient"
	"github.com/eleutherius/btrepl/internal/systemd"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	timerUnit   = "btrepl.timer"
	logChanSize = 64
)

type Server struct {
	pb.UnimplementedBtreplServer

	cfgPath   string
	log       *slog.Logger
	broadcast *logbroadcast.Broadcaster

	mu    sync.Mutex
	runCh chan struct{}
}

func New(cfgPath string, log *slog.Logger, bc *logbroadcast.Broadcaster) *Server {
	return &Server{
		cfgPath:   cfgPath,
		log:       log,
		broadcast: bc,
		runCh:     make(chan struct{}, 1),
	}
}

// RunLoop starts the daemon replication loop. Blocks until ctx is cancelled.
func (s *Server) RunLoop(ctx context.Context) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		s.log.Error("load config", "err", err)
		return
	}

	interval := parseDuration(cfg.Interval, time.Hour)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.log.Info("daemon started", "interval", cfg.Interval, "addr", ":50051")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.doRun()
		case <-s.runCh:
			s.doRun()
			ticker.Reset(interval)
		}
	}
}

func (s *Server) doRun() {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		s.log.Error("load config", "err", err)
		return
	}
	r := replication.New(cfg, s.log)
	if err := r.RunCycle(); err != nil {
		s.log.Error("replication cycle failed", "err", err)
	}
}

// --- gRPC handlers ---

func (s *Server) Run(_ context.Context, _ *pb.RunRequest) (*pb.RunResponse, error) {
	select {
	case s.runCh <- struct{}{}:
		s.log.Info("manual run triggered")
		return &pb.RunResponse{Ok: true}, nil
	default:
		return &pb.RunResponse{Ok: false, Error: "replication already running"}, nil
	}
}

func (s *Server) Status(_ context.Context, _ *pb.StatusRequest) (*pb.StatusResponse, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}

	active, _ := systemd.IsActive(timerUnit)
	bm := btrfs.NewManager(cfg.BtrfsRoot, cfg.SnapshotDir, cfg.SnapshotPrefix)

	var subvols []*pb.SubvolStatus
	for _, sv := range cfg.Subvolumes {
		snaps, _ := bm.ListSnapshots(sv)
		latest := ""
		if len(snaps) > 0 {
			latest = snaps[len(snaps)-1].Name
		}
		subvols = append(subvols, &pb.SubvolStatus{
			Name:           sv,
			SnapshotCount:  int32(len(snaps)),
			LatestSnapshot: latest,
		})
	}

	return &pb.StatusResponse{
		TimerActive: active,
		Interval:    cfg.Interval,
		Slaves:      cfg.Slaves,
		Subvolumes:  subvols,
	}, nil
}

func (s *Server) AddSlave(_ context.Context, req *pb.SlaveRequest) (*pb.SlaveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}

	ssh, err := sshclient.New(req.Ip, cfg.SSHUser, cfg.SSHIdentity)
	if err != nil {
		return &pb.SlaveResponse{Ok: false, Error: err.Error()}, nil
	}
	defer ssh.Close()

	snapDir := cfg.BtrfsRoot + "/" + cfg.SnapshotDir
	if _, err := ssh.Run("mkdir -p " + snapDir); err != nil {
		return &pb.SlaveResponse{Ok: false, Error: err.Error()}, nil
	}

	cfg.AddSlave(req.Ip)
	if err := config.Save(cfg, s.cfgPath); err != nil {
		return nil, status.Errorf(codes.Internal, "save config: %v", err)
	}

	s.log.Info("slave added", "ip", req.Ip)
	return &pb.SlaveResponse{Ok: true}, nil
}

func (s *Server) DelSlave(_ context.Context, req *pb.SlaveRequest) (*pb.SlaveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}

	if !cfg.RemoveSlave(req.Ip) {
		return &pb.SlaveResponse{Ok: false, Error: "slave not found"}, nil
	}
	if err := config.Save(cfg, s.cfgPath); err != nil {
		return nil, status.Errorf(codes.Internal, "save config: %v", err)
	}

	s.log.Info("slave removed", "ip", req.Ip)
	return &pb.SlaveResponse{Ok: true}, nil
}

func (s *Server) WatchLogs(_ *pb.WatchLogsRequest, stream pb.Btrepl_WatchLogsServer) error {
	ch := make(chan *pb.LogEntry, logChanSize)
	unsub := s.broadcast.Subscribe(ch)
	defer unsub()

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case entry := <-ch:
			if err := stream.Send(entry); err != nil {
				return err
			}
		}
	}
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
