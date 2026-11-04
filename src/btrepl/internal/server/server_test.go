package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, path, interval string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("interval: "+interval+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunLoopPicksUpIntervalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, "1h")

	s := New(path, slog.New(slog.DiscardHandler), nil)
	s.reloadEvery = 10 * time.Millisecond
	runs := make(chan struct{}, 100)
	s.runCycle = func() { runs <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunLoop(ctx)

	select {
	case <-runs:
		t.Fatal("cycle ran before the 1h interval elapsed")
	case <-time.After(100 * time.Millisecond):
	}

	writeConfig(t, path, "20ms")
	for i := 0; i < 3; i++ {
		select {
		case <-runs:
		case <-time.After(2 * time.Second):
			t.Fatalf("got %d cycles after changing interval to 20ms, want 3", i)
		}
	}
}

func TestIntervalFallsBackToOneHour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	s := New(path, slog.New(slog.DiscardHandler), nil)
	if got := s.interval(); got != time.Hour {
		t.Errorf("missing config: interval = %s", got)
	}
	for _, v := range []string{"0s", "-5m", "soon"} {
		writeConfig(t, path, v)
		if got := s.interval(); got != time.Hour {
			t.Errorf("interval %q: got %s, want 1h", v, got)
		}
	}
	writeConfig(t, path, "15m")
	if got := s.interval(); got != 15*time.Minute {
		t.Errorf("interval 15m: got %s", got)
	}
}
