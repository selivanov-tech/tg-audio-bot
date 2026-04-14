package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// TestKeepAlive_SkipsWhenIdle verifies the gate: with no active work the
// goroutine must not call the public URL. Gate protects zero-scale savings.
func TestKeepAlive_SkipsWhenIdle(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	q := NewQueue(noopProcessor{}, 1) // active starts at 0
	k := NewKeepAlive(q, srv.URL, 10*time.Millisecond, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	k.Run(ctx)

	if got := hits.Load(); got != 0 {
		t.Fatalf("expected 0 pings while idle, got %d", got)
	}
}

// TestKeepAlive_PingsWhenActive verifies the complement: with work enqueued
// but not completing, the keep-alive must fire at least one ping before ctx
// ends, else long downloads would be cut off by Fly's auto-stop.
func TestKeepAlive_PingsWhenActive(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	q := NewQueue(noopProcessor{}, 1)
	q.active.Add(1) // simulate an enqueued item without going through the full pipeline
	defer q.active.Add(-1)

	k := NewKeepAlive(q, srv.URL, 10*time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	k.Run(ctx)

	if got := hits.Load(); got < 1 {
		t.Fatalf("expected >=1 ping while active, got %d", got)
	}
}

// noopProcessor satisfies Processor for queue construction in these tests —
// we never actually enqueue through Enqueue, so Process is unreachable.
type noopProcessor struct{}

func (noopProcessor) Process(context.Context, *domain.Download) {}
