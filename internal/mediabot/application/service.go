package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

// maxTelegramAudioBytes is the hard limit for sendAudio via the default Bot
// API (50 MiB). See CLAUDE.md: a local Bot API server would lift this.
const maxTelegramAudioBytes int64 = 50 * 1024 * 1024

// fetchInfoTimeout caps the provider's metadata call. yt-dlp makes HTTP
// requests to YouTube that can hang without this — users see the
// "Fetching video info..." message forever with nothing in the logs.
const fetchInfoTimeout = 45 * time.Second

// maxConcurrentFetchInfo caps how many yt-dlp metadata probes can run at
// once across all users. Each yt-dlp invocation spawns Python + network I/O
// (~70 MB RSS); on a 512 MB VM, 4+ in parallel OOM-kill the process. A user
// flooding URLs would otherwise spawn one yt-dlp per message with no gate,
// because the per-user download queue only serializes downloads, not fetches.
const maxConcurrentFetchInfo = 2

// progressEditInterval throttles user-facing progress updates to avoid hitting
// Telegram's per-chat edit rate limit. The final 100% tick always bypasses
// throttling so the user sees completion.
const progressEditInterval = 2 * time.Second

// Service orchestrates a single download's lifecycle: provider lookup,
// quality selection, queueing, downloading with throttled progress, size
// check, sending, and cleanup.
type Service struct {
	registry    *domain.ProviderRegistry
	messenger   port.Messenger
	queue       *Queue
	tempDir     string
	maxDuration time.Duration // 0 = no limit (constructor enforces default)
	log         *slog.Logger

	pendingMu sync.Mutex
	// pending is keyed by the keyboard message ID — when the user taps a
	// quality button, that message's ID is what Telegram echoes back, and
	// it's how we find the matching Download aggregate.
	pending map[int]*domain.Download

	progressMu sync.Mutex
	// progress tracks UI state for each in-flight Download: the message to
	// edit and the throttle timestamp. UI state is kept here (not on the
	// aggregate) so the domain stays free of transport concerns.
	progress map[*domain.Download]*progressState

	// fetchInfoSem gates concurrent yt-dlp metadata calls workspace-wide to
	// keep memory use within the VM's limit. See maxConcurrentFetchInfo.
	fetchInfoSem chan struct{}
}

// NewService wires the service. The queue is built here because the service
// itself is its Processor (the Queue calls back into svc.Process).
// maxDurationMinutes < 1 falls back to 45 min — see Config.MaxDurationMinutes.
func NewService(
	registry *domain.ProviderRegistry,
	messenger port.Messenger,
	tempDir string,
	maxConcurrent int,
	maxDurationMinutes int,
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if maxDurationMinutes < 1 {
		maxDurationMinutes = 45
	}
	s := &Service{
		registry:     registry,
		messenger:    messenger,
		tempDir:      tempDir,
		maxDuration:  time.Duration(maxDurationMinutes) * time.Minute,
		log:          logger,
		pending:      make(map[int]*domain.Download),
		progress:     make(map[*domain.Download]*progressState),
		fetchInfoSem: make(chan struct{}, maxConcurrentFetchInfo),
	}
	s.queue = NewQueue(s, maxConcurrent)
	return s
}

// Queue exposes the internal queue so wiring code (main.go) can attach
// peripherals like the keep-alive goroutine that needs to know whether any
// work is in flight. The queue itself is safe for concurrent use.
func (s *Service) Queue() *Queue {
	return s.queue
}

// HandleURL is called from the message router when a user sends a URL.
// customName is optional; empty string means "use the title from the provider".
func (s *Service) HandleURL(ctx context.Context, chatID int64, rawURL, customName string) {
	provider, err := s.registry.Detect(rawURL)
	if err != nil {
		if errors.Is(err, domain.ErrUnsupportedURL) {
			s.log.Warn("url rejected", "chat", chatID, "url", rawURL, "reason", "unsupported")
			_, _ = s.messenger.SendText(ctx, chatID, MsgUnsupportedURL)
			return
		}
		s.log.Error("url route failed", "chat", chatID, "url", rawURL, "err", err)
		_, _ = s.messenger.SendText(ctx, chatID, MsgRouteFailed)
		return
	}
	s.log.Info("url routed", "chat", chatID, "url", rawURL, "provider", provider.ID())

	progressMsgID, _ := s.messenger.SendText(ctx, chatID, MsgFetching)

	// Gate on the global fetch semaphore before logging "start" so timestamps
	// reflect when yt-dlp actually runs, not when the user sent the URL.
	select {
	case s.fetchInfoSem <- struct{}{}:
	case <-ctx.Done():
		return
	}

	s.log.Info("fetch info start", "chat", chatID, "url", rawURL)
	fetchStart := time.Now()
	info, err := func() (domain.MediaInfo, error) {
		defer func() { <-s.fetchInfoSem }()
		fetchCtx, cancel := context.WithTimeout(ctx, fetchInfoTimeout)
		defer cancel()
		return provider.FetchInfo(fetchCtx, rawURL)
	}()
	if err != nil {
		s.log.Error("fetch info failed", "chat", chatID, "url", rawURL, "err", err)
		s.replaceOrSend(ctx, chatID, progressMsgID, MapUserError(StageFetch, err))
		return
	}
	s.log.Info("fetch info ok",
		"chat", chatID,
		"url", rawURL,
		"title", info.Title,
		"duration_ms", time.Since(fetchStart).Milliseconds())

	// Duration gate: reject videos over the configured limit before creating
	// a Download or showing the quality keyboard. info.Duration == 0 means
	// the provider could not report a duration (livestream, metadata gap) —
	// we let it through and the download stage will surface whatever fails.
	if s.maxDuration > 0 && info.Duration > s.maxDuration {
		limitMin := int(s.maxDuration / time.Minute)
		actualMin := int((info.Duration + time.Minute - 1) / time.Minute) // round up
		s.log.Warn("video rejected",
			"chat", chatID,
			"url", rawURL,
			"duration_min", actualMin,
			"limit_min", limitMin)
		s.replaceOrSend(ctx, chatID, progressMsgID, MsgVideoTooLong(actualMin, limitMin))
		return
	}

	d := domain.NewDownload(chatID, rawURL, provider.ID(), info)
	if customName != "" {
		d.SetCustomName(customName)
	}

	header := info.Title
	if d.EffectiveFilename() != info.Title {
		header = d.EffectiveFilename() + " (" + info.Title + ")"
	}

	// Delete the "Fetching..." placeholder, then send the keyboard as a new
	// message so we can track it by ID for the callback.
	if progressMsgID != 0 {
		_ = s.messenger.DeleteMessage(ctx, chatID, progressMsgID)
	}
	kbMsgID, err := s.messenger.SendOptionsKeyboard(ctx, chatID, header+"\n\n"+MsgChooseQuality, []port.KeyboardOption{
		{Label: QualityLabel(domain.QualityBest), CallbackData: "quality:" + domain.QualityBest.String()},
		{Label: QualityLabel(domain.QualityGood), CallbackData: "quality:" + domain.QualityGood.String()},
	})
	if err != nil {
		return
	}

	s.pendingMu.Lock()
	s.pending[kbMsgID] = d
	s.pendingMu.Unlock()
}

// HandleQualitySelection is called from the message router when the user taps
// a quality button. keyboardMsgID is the ID of the message whose button was
// tapped (used both to find the pending Download and as the message we will
// edit with progress updates).
func (s *Service) HandleQualitySelection(ctx context.Context, chatID int64, q domain.Quality, keyboardMsgID int) {
	s.pendingMu.Lock()
	d, ok := s.pending[keyboardMsgID]
	if ok {
		delete(s.pending, keyboardMsgID)
	}
	s.pendingMu.Unlock()
	if !ok {
		s.log.Warn("callback stale", "chat", chatID, "msg_id", keyboardMsgID)
		return
	}
	s.log.Info("quality selected", "chat", chatID, "quality", q.String())

	if err := d.SelectQuality(q); err != nil {
		_ = s.messenger.EditText(ctx, chatID, keyboardMsgID, MsgStaleSelection)
		return
	}
	// Reuse the keyboard message for progress updates — we stash its ID on
	// the Download by wrapping it in a small struct via pendingProgress.
	s.progressMu.Lock()
	s.progress[d] = &progressState{msgID: keyboardMsgID}
	s.progressMu.Unlock()

	position := s.queue.Enqueue(ctx, d)
	s.log.Info("queued", "chat", chatID, "url", d.URL(), "position", position)
	if position > 1 {
		_ = s.messenger.EditText(ctx, chatID, keyboardMsgID, MsgQueued(position))
	} else {
		_ = s.messenger.EditText(ctx, chatID, keyboardMsgID, RenderProgress(domain.Progress{}))
	}
}

// progressState holds per-download UI state: the message we edit and the
// throttle timestamp for progress edits.
type progressState struct {
	mu       sync.Mutex
	msgID    int
	lastEdit time.Time
}

// The progress map is on Service rather than Download because UI state does
// not belong in the domain aggregate. It is guarded by progressMu.
//
// Declared as package-scope field additions via an init-less pattern: we
// extend Service below.

// Process implements Processor. The queue calls this for one download at a
// time per user, and for up to maxConcurrent downloads across all users.
func (s *Service) Process(ctx context.Context, d *domain.Download) {
	chatID := d.ChatID()
	st := s.progressStateFor(d)

	if err := d.MarkDownloading(); err != nil {
		s.failDownload(ctx, d, fmt.Errorf("internal: %w", err), MsgInternalError)
		return
	}
	// If we were queued, the message still says "Queued...". Reset it.
	if st != nil {
		_ = s.messenger.EditText(ctx, chatID, st.msgID, RenderProgress(domain.Progress{}))
		st.mu.Lock()
		st.lastEdit = time.Now()
		st.mu.Unlock()
	}

	provider, err := s.registry.Detect(d.URL())
	if err != nil {
		s.failDownload(ctx, d, err, MsgUnsupportedURL)
		return
	}

	// Per-download temp subdir; easy cleanup.
	if err := os.MkdirAll(s.tempDir, 0o755); err != nil {
		s.failDownload(ctx, d, err, MsgInternalError)
		return
	}
	subDir, err := os.MkdirTemp(s.tempDir, "dl-*")
	if err != nil {
		s.failDownload(ctx, d, err, MsgInternalError)
		return
	}
	defer func() { _ = os.RemoveAll(subDir) }()

	opts := domain.DownloadOptions{
		URL:        d.URL(),
		Quality:    d.Quality(),
		MediaType:  domain.MediaTypeAudio,
		OutputDir:  subDir,
		Filename:   d.EffectiveFilename(),
		OnProgress: s.progressCallback(ctx, chatID, st),
	}
	s.log.Info("download start", "chat", chatID, "url", d.URL(), "quality", d.Quality().String())
	filePath, err := provider.Download(ctx, opts)
	if err != nil {
		s.failDownload(ctx, d, err, MapUserError(StageDownload, err))
		return
	}

	if err := d.MarkSending(); err != nil {
		s.failDownload(ctx, d, err, MsgInternalError)
		return
	}

	// 50MB cap (default Bot API). Suggest Good quality when Best overflowed.
	stat, err := os.Stat(filePath)
	if err != nil {
		s.failDownload(ctx, d, err, MsgInternalError)
		return
	}
	sizeMB := stat.Size() / (1024 * 1024)
	if stat.Size() > maxTelegramAudioBytes {
		msg := MsgTooLarge(sizeMB, d.Quality() == domain.QualityBest)
		s.failDownload(ctx, d, domain.ErrFileTooLarge, msg)
		return
	}
	s.log.Info("download ok", "chat", chatID, "url", d.URL(), "size_mb", sizeMB)

	if st != nil {
		_ = s.messenger.EditText(ctx, chatID, st.msgID, MsgSending)
	}
	s.log.Info("send start", "chat", chatID, "size_mb", sizeMB)
	if err := s.messenger.SendAudio(ctx, chatID, filePath, d.EffectiveFilename()); err != nil {
		s.failDownload(ctx, d, err, MapUserError(StageSend, err))
		return
	}
	s.log.Info("send ok", "chat", chatID)

	_ = d.MarkComplete()
	if st != nil {
		_ = s.messenger.DeleteMessage(ctx, chatID, st.msgID)
	}
	s.forgetProgress(d)
}

// progressCallback returns a throttled callback. It always forwards a 100%
// update regardless of the throttle interval, so the user sees completion.
func (s *Service) progressCallback(ctx context.Context, chatID int64, st *progressState) func(domain.Progress) {
	if st == nil {
		return func(domain.Progress) {}
	}
	return func(p domain.Progress) {
		st.mu.Lock()
		now := time.Now()
		should := p.Converting || p.Percent >= 100 || now.Sub(st.lastEdit) >= progressEditInterval
		if should {
			st.lastEdit = now
		}
		msgID := st.msgID
		st.mu.Unlock()
		if !should {
			return
		}
		_ = s.messenger.EditText(ctx, chatID, msgID, RenderProgress(p))
	}
}

// failDownload handles the error branches uniformly: log state transition,
// notify the user, and drop the progress mapping.
func (s *Service) failDownload(ctx context.Context, d *domain.Download, cause error, userMsg string) {
	_ = d.MarkFailed(cause)
	chatID := d.ChatID()
	s.log.Error("download failed",
		"chat", chatID,
		"url", d.URL(),
		"state", d.State(),
		"err", cause)
	st := s.progressStateFor(d)
	if st != nil {
		_ = s.messenger.EditText(ctx, chatID, st.msgID, userMsg)
	} else {
		_, _ = s.messenger.SendText(ctx, chatID, userMsg)
	}
	s.forgetProgress(d)
}

// --- progress map helpers (kept below to not clutter the main flow) -------

// progressState lookup/store: see note above; these methods gate access to
// the Service's map field declared via receiver extension below.
func (s *Service) progressStateFor(d *domain.Download) *progressState {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progress[d]
}
func (s *Service) forgetProgress(d *domain.Download) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	delete(s.progress, d)
}

// replaceOrSend edits the existing msg if msgID != 0, otherwise sends fresh.
func (s *Service) replaceOrSend(ctx context.Context, chatID int64, msgID int, text string) {
	if msgID != 0 {
		if err := s.messenger.EditText(ctx, chatID, msgID, text); err == nil {
			return
		}
	}
	_, _ = s.messenger.SendText(ctx, chatID, text)
}

