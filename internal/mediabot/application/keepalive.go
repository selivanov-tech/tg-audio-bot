package application

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// DefaultKeepAliveInterval is how often the self-ping runs while work is
// active. Fly's HTTP-idle auto-stop timer defaults to ~5 minutes; pinging
// well under that keeps the machine alive through long downloads.
const DefaultKeepAliveInterval = 2 * time.Minute

// keepAlivePingTimeout is the per-request timeout for a self-ping. The ping
// just resets Fly's idle timer; if it fails we log and carry on.
const keepAlivePingTimeout = 10 * time.Second

// KeepAlive periodically GETs publicURL/health via the Fly proxy while the
// queue has active work, resetting Fly's auto-stop idle timer. Without this,
// a multi-minute yt-dlp download with no concurrent webhooks could have the
// VM stopped from under it.
//
// Pings go over the public URL (not localhost) so the traffic traverses the
// Fly proxy and counts as "active request" for the idle timer.
type KeepAlive struct {
	queue    *Queue
	pingURL  string
	interval time.Duration
	client   *http.Client
	log      *slog.Logger
}

// NewKeepAlive constructs a KeepAlive. publicURL is the bot's external base
// URL (no trailing slash); interval<=0 uses DefaultKeepAliveInterval.
func NewKeepAlive(queue *Queue, publicURL string, interval time.Duration, logger *slog.Logger) *KeepAlive {
	if interval <= 0 {
		interval = DefaultKeepAliveInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &KeepAlive{
		queue:    queue,
		pingURL:  publicURL + "/health",
		interval: interval,
		client:   &http.Client{Timeout: keepAlivePingTimeout},
		log:      logger,
	}
}

// Run blocks until ctx is cancelled, pinging /health every interval while
// Queue.Active() > 0. Safe to call as a goroutine from main.
func (k *KeepAlive) Run(ctx context.Context) {
	t := time.NewTicker(k.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if k.queue.Active() == 0 {
				continue
			}
			k.ping(ctx)
		}
	}
}

// ping issues one GET /health over the public URL. The response is drained
// and discarded; only the fact that the request reached Fly's proxy matters.
func (k *KeepAlive) ping(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.pingURL, nil)
	if err != nil {
		k.log.Warn("keep-alive ping build failed", "err", err)
		return
	}
	resp, err := k.client.Do(req)
	if err != nil {
		k.log.Warn("keep-alive ping failed", "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	k.log.Debug("keep-alive ping ok", "status", resp.StatusCode, "active", k.queue.Active())
}
