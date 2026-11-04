package replication

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/eleutherius/btrepl/internal/config"
)

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "btrepl.lock")
	unlock, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(path); !errors.Is(err, ErrCycleRunning) {
		t.Fatalf("second lock: err = %v, want ErrCycleRunning", err)
	}
	unlock()
	unlock2, err := lock(path)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func TestRunCycleTakesLock(t *testing.T) {
	r := &Replicator{lockPath: filepath.Join(t.TempDir(), "btrepl.lock")}
	r.cfg = config.Default()
	r.cfg.Slaves = []string{"10.0.0.2"}
	unlock, err := lock(r.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := r.RunCycle(); !errors.Is(err, ErrCycleRunning) {
		t.Fatalf("RunCycle while locked: err = %v, want ErrCycleRunning", err)
	}
}
