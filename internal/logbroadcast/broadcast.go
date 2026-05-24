package logbroadcast

import (
	"context"
	"log/slog"
	"sync"

	pb "github.com/eleutherius/btrepl/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Broadcaster is a slog.Handler that writes to a base handler and fans out
// every log record to all registered gRPC WatchLogs streams.
type Broadcaster struct {
	base slog.Handler

	mu          sync.RWMutex
	subscribers map[uint64]chan<- *pb.LogEntry
	nextID      uint64
}

func New(base slog.Handler) *Broadcaster {
	return &Broadcaster{
		base:        base,
		subscribers: make(map[uint64]chan<- *pb.LogEntry),
	}
}

func (b *Broadcaster) Enabled(ctx context.Context, level slog.Level) bool {
	return b.base.Enabled(ctx, level)
}

func (b *Broadcaster) Handle(ctx context.Context, r slog.Record) error {
	attrs := make(map[string]string, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})

	entry := &pb.LogEntry{
		Level:   r.Level.String(),
		Message: r.Message,
		Time:    timestamppb.New(r.Time).AsTime().Format("2006-01-02T15:04:05Z07:00"),
		Attrs:   attrs,
	}

	b.mu.RLock()
	for _, ch := range b.subscribers {
		select {
		case ch <- entry:
		default: // drop if subscriber is slow
		}
	}
	b.mu.RUnlock()

	return b.base.Handle(ctx, r)
}

func (b *Broadcaster) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Broadcaster{base: b.base.WithAttrs(attrs), subscribers: b.subscribers}
}

func (b *Broadcaster) WithGroup(name string) slog.Handler {
	return &Broadcaster{base: b.base.WithGroup(name), subscribers: b.subscribers}
}

// Subscribe registers a channel to receive log entries. Returns an unsubscribe func.
func (b *Broadcaster) Subscribe(ch chan<- *pb.LogEntry) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subscribers[id] = ch
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
}
