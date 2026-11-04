package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	pb "github.com/eleutherius/btrepl/api"
	"github.com/eleutherius/btrepl/internal/btrfs"
	"github.com/eleutherius/btrepl/internal/config"
	"github.com/eleutherius/btrepl/internal/logbroadcast"
	"github.com/eleutherius/btrepl/internal/replication"
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

	// reloadEvery is how often RunLoop re-reads interval from the config.
	reloadEvery time.Duration
	// runCycle runs one replication cycle; tests replace it.
	runCycle func()
	// badInterval is the last invalid interval value that was logged.
	badInterval string
}

func New(cfgPath string, log *slog.Logger, bc *logbroadcast.Broadcaster) *Server {
	s := &Server{
		cfgPath:     cfgPath,
		log:         log,
		broadcast:   bc,
		runCh:       make(chan struct{}, 1),
		reloadEvery: time.Minute,
	}
	s.runCycle = s.doRun
	return s
}

// RunLoop runs a replication cycle every interval until ctx is cancelled.
// The interval is re-read from the config after every cycle and every
// reloadEvery, so a change takes effect without restarting the daemon.
func (s *Server) RunLoop(ctx context.Context) {
	interval := s.interval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	reload := time.NewTicker(s.reloadEvery)
	defer reload.Stop()

	s.log.Info("daemon started", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCycle()
		case <-s.runCh:
			s.runCycle()
			ticker.Reset(interval)
		case <-reload.C:
		}
		if d := s.interval(); d != interval {
			s.log.Info("interval changed", "from", interval, "to", d)
			interval = d
			ticker.Reset(interval)
		}
	}
}

// interval returns the configured cycle interval, or one hour if the config
// cannot be read or interval is not a positive duration.
func (s *Server) interval() time.Duration {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return time.Hour
	}
	d, err := time.ParseDuration(cfg.Interval)
	if err != nil || d <= 0 {
		if cfg.Interval != s.badInterval {
			s.log.Warn("invalid interval in config, using 1h", "interval", cfg.Interval)
			s.badInterval = cfg.Interval
		}
		return time.Hour
	}
	s.badInterval = ""
	return d
}

func (s *Server) doRun() {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		s.log.Error("load config", "err", err)
		return
	}
	r := replication.New(cfg, s.log)
	if err := r.RunCycle(); errors.Is(err, replication.ErrCycleRunning) {
		s.log.Warn("replication cycle skipped", "err", err)
	} else if err != nil {
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

	ssh, err := replication.Dial(cfg, req.Ip, true)
	if err != nil {
		return &pb.SlaveResponse{Ok: false, Error: err.Error()}, nil
	}
	defer ssh.Close()
	if ssh.NewHostKey != "" {
		s.log.Info("recorded slave host key", "ip", req.Ip, "fingerprint", ssh.NewHostKey, "known_hosts", cfg.SSHKnownHosts)
	}

	if err := replication.PrepareSlave(ssh, cfg); err != nil {
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
