package application

import (
	"context"
	"sync"

	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// Queue is the per-user, globally-capped work queue for downloads.
//
// Model:
//   - One lazy-spawned goroutine per chatID processes that user's downloads
//     strictly in order.
//   - A global semaphore (buffered channel) gates how many downloads are
//     *actively running* across all users, regardless of how many users have
//     items queued. This protects CPU (transcoding), disk I/O, and
//     rate limits at the YouTube side.
//
// See CLAUDE.md "Queue is per-user with a global cap".
type Queue struct {
	processor Processor
	sem       chan struct{}

	mu      sync.Mutex
	buckets map[int64]*userBucket
}

// Processor runs one download to completion. The Queue never calls it
// concurrently for the same chatID; it may call it concurrently for
// different chatIDs up to the global cap.
type Processor interface {
	Process(ctx context.Context, d *domain.Download)
}

type userBucket struct {
	items []*domain.Download
	// running is true while a worker goroutine is draining this bucket.
	running bool
}

// NewQueue returns a Queue with the given global concurrency cap. Cap must
// be >= 1; anything smaller is treated as 1.
func NewQueue(p Processor, maxConcurrent int) *Queue {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Queue{
		processor: p,
		sem:       make(chan struct{}, maxConcurrent),
		buckets:   make(map[int64]*userBucket),
	}
}

// Enqueue appends d to the user's bucket and returns its 1-based position in
// that user's queue (1 = first, will run next). A worker is spawned if the
// bucket is not already being drained.
func (q *Queue) Enqueue(ctx context.Context, d *domain.Download) int {
	chatID := d.ChatID()

	q.mu.Lock()
	b, ok := q.buckets[chatID]
	if !ok {
		b = &userBucket{}
		q.buckets[chatID] = b
	}
	b.items = append(b.items, d)
	position := len(b.items)
	needSpawn := !b.running
	if needSpawn {
		b.running = true
	}
	q.mu.Unlock()

	if needSpawn {
		go q.drain(ctx, chatID)
	}
	return position
}

// drain runs in its own goroutine per user. It pops one item at a time,
// acquires the global semaphore, runs the processor, releases, and repeats
// until the user's bucket is empty — at which point the bucket is removed
// and the goroutine exits.
func (q *Queue) drain(ctx context.Context, chatID int64) {
	for {
		d, done := q.popNext(chatID)
		if done {
			return
		}

		// Global cap: block until a slot is free or ctx is cancelled.
		select {
		case q.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		q.processor.Process(ctx, d)
		<-q.sem
	}
}

// popNext removes and returns the next item for chatID. If the bucket is
// empty, it clears the bucket's running flag, deletes it, and returns done=true.
func (q *Queue) popNext(chatID int64) (*domain.Download, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	b, ok := q.buckets[chatID]
	if !ok || len(b.items) == 0 {
		if ok {
			b.running = false
			delete(q.buckets, chatID)
		}
		return nil, true
	}
	d := b.items[0]
	b.items = b.items[1:]
	return d, false
}
