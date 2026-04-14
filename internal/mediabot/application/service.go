package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rag-brothers/media-bot/internal/mediabot/application/port"
	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// maxTelegramAudioBytes is the hard limit for sendAudio via the default Bot
// API (50 MiB). See CLAUDE.md: a local Bot API server would lift this.
const maxTelegramAudioBytes int64 = 50 * 1024 * 1024

// progressEditInterval throttles user-facing progress updates to avoid hitting
// Telegram's per-chat edit rate limit. The final 100% tick always bypasses
// throttling so the user sees completion.
const progressEditInterval = 2 * time.Second

// Service orchestrates a single download's lifecycle: provider lookup,
// quality selection, queueing, downloading with throttled progress, size
// check, sending, and cleanup.
type Service struct {
	registry  *domain.ProviderRegistry
	messenger port.Messenger
	queue     *Queue
	tempDir   string

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
}

// NewService wires the service. The queue is built here because the service
// itself is its Processor (the Queue calls back into svc.Process).
func NewService(
	registry *domain.ProviderRegistry,
	messenger port.Messenger,
	tempDir string,
	maxConcurrent int,
) *Service {
	s := &Service{
		registry:  registry,
		messenger: messenger,
		tempDir:   tempDir,
		pending:   make(map[int]*domain.Download),
		progress:  make(map[*domain.Download]*progressState),
	}
	s.queue = NewQueue(s, maxConcurrent)
	return s
}

// HandleURL is called from the message router when a user sends a URL.
// customName is optional; empty string means "use the title from the provider".
func (s *Service) HandleURL(ctx context.Context, chatID int64, rawURL, customName string) {
	provider, err := s.registry.Detect(rawURL)
	if err != nil {
		if errors.Is(err, domain.ErrUnsupportedURL) {
			_, _ = s.messenger.SendText(ctx, chatID, "Unsupported URL. Currently supported: YouTube")
			return
		}
		_, _ = s.messenger.SendText(ctx, chatID, "Could not route URL. Try again.")
		return
	}

	progressMsgID, _ := s.messenger.SendText(ctx, chatID, "Fetching video info...")

	info, err := provider.FetchInfo(ctx, rawURL)
	if err != nil {
		s.replaceOrSend(ctx, chatID, progressMsgID, "Could not fetch video info. URL may be private or restricted.")
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
	kbMsgID, err := s.messenger.SendOptionsKeyboard(ctx, chatID, header+"\n\nChoose quality:", []port.KeyboardOption{
		{Label: domain.QualityBest.Label(), CallbackData: "quality:" + domain.QualityBest.String()},
		{Label: domain.QualityGood.Label(), CallbackData: "quality:" + domain.QualityGood.String()},
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
		return // stale callback, ignore
	}

	if err := d.SelectQuality(q); err != nil {
		_ = s.messenger.EditText(ctx, chatID, keyboardMsgID, "That selection is no longer valid. Send the URL again.")
		return
	}
	// Reuse the keyboard message for progress updates — we stash its ID on
	// the Download by wrapping it in a small struct via pendingProgress.
	s.progressMu.Lock()
	s.progress[d] = &progressState{msgID: keyboardMsgID}
	s.progressMu.Unlock()

	position := s.queue.Enqueue(ctx, d)
	if position > 1 {
		_ = s.messenger.EditText(ctx, chatID, keyboardMsgID,
			fmt.Sprintf("Queued (position %d). Will start after your current download.", position))
	} else {
		_ = s.messenger.EditText(ctx, chatID, keyboardMsgID, "Downloading... 0%")
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
		s.failDownload(ctx, d, fmt.Errorf("internal: %w", err), "Internal error.")
		return
	}
	// If we were queued, the message still says "Queued...". Reset it.
	if st != nil {
		_ = s.messenger.EditText(ctx, chatID, st.msgID, "Downloading... 0%")
		st.mu.Lock()
		st.lastEdit = time.Now()
		st.mu.Unlock()
	}

	provider, err := s.registry.Detect(d.URL())
	if err != nil {
		s.failDownload(ctx, d, err, "Unsupported URL.")
		return
	}

	// Per-download temp subdir; easy cleanup.
	if err := os.MkdirAll(s.tempDir, 0o755); err != nil {
		s.failDownload(ctx, d, err, "Could not create temp dir.")
		return
	}
	subDir, err := os.MkdirTemp(s.tempDir, "dl-*")
	if err != nil {
		s.failDownload(ctx, d, err, "Could not create temp dir.")
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
	filePath, err := provider.Download(ctx, opts)
	if err != nil {
		s.failDownload(ctx, d, err, "Download failed: "+err.Error())
		return
	}

	if err := d.MarkSending(); err != nil {
		s.failDownload(ctx, d, err, "Internal error.")
		return
	}

	// 50MB cap (default Bot API). Suggest Good quality when Best overflowed.
	stat, err := os.Stat(filePath)
	if err != nil {
		s.failDownload(ctx, d, err, "Download artifact missing.")
		return
	}
	if stat.Size() > maxTelegramAudioBytes {
		sizeMiB := stat.Size() / (1024 * 1024)
		msg := fmt.Sprintf("Audio is %d MB, over Telegram's 50MB limit.", sizeMiB)
		if d.Quality() == domain.QualityBest {
			msg += " Try Good quality."
		}
		s.failDownload(ctx, d, domain.ErrFileTooLarge, msg)
		return
	}

	if st != nil {
		_ = s.messenger.EditText(ctx, chatID, st.msgID, "Sending audio...")
	}
	if err := s.messenger.SendAudio(ctx, chatID, filePath, d.EffectiveFilename()); err != nil {
		s.failDownload(ctx, d, err, "Failed to send audio. Try again.")
		return
	}

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
		should := p.Percent >= 100 || now.Sub(st.lastEdit) >= progressEditInterval
		if should {
			st.lastEdit = now
		}
		msgID := st.msgID
		st.mu.Unlock()
		if !should {
			return
		}
		_ = s.messenger.EditText(ctx, chatID, msgID, p.DisplayString())
	}
}

// failDownload handles the error branches uniformly: log state transition,
// notify the user, and drop the progress mapping.
func (s *Service) failDownload(ctx context.Context, d *domain.Download, cause error, userMsg string) {
	_ = d.MarkFailed(cause)
	chatID := d.ChatID()
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

