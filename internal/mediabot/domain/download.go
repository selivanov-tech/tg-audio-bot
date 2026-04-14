package domain

import (
	"time"
)

// DownloadState is a point in the lifecycle of a single user request.
// Transitions are enforced by Mark* methods; direct field access is not allowed
// outside this package (see CLAUDE.md: "Download is a state machine").
type DownloadState int

const (
	StateAwaitingQuality DownloadState = iota
	StateQueued
	StateDownloading
	StateSending
	StateComplete
	StateFailed
)

func (s DownloadState) String() string {
	switch s {
	case StateAwaitingQuality:
		return "awaiting_quality"
	case StateQueued:
		return "queued"
	case StateDownloading:
		return "downloading"
	case StateSending:
		return "sending"
	case StateComplete:
		return "complete"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Download is the aggregate tracking a single URL through the pipeline.
// One Download per user request; the queue holds these between states.
type Download struct {
	chatID     int64
	url        string
	provider   ProviderID
	info       MediaInfo
	quality    Quality
	hasQuality bool
	customName string
	state      DownloadState
	failure    error
	createdAt  time.Time
}

func NewDownload(chatID int64, url string, provider ProviderID, info MediaInfo) *Download {
	return &Download{
		chatID:    chatID,
		url:       url,
		provider:  provider,
		info:      info,
		state:     StateAwaitingQuality,
		createdAt: time.Now(),
	}
}

// Accessors ---------------------------------------------------------------

func (d *Download) ChatID() int64         { return d.chatID }
func (d *Download) URL() string           { return d.url }
func (d *Download) Provider() ProviderID  { return d.provider }
func (d *Download) Info() MediaInfo       { return d.info }
func (d *Download) Quality() Quality      { return d.quality }
func (d *Download) State() DownloadState  { return d.state }
func (d *Download) Failure() error        { return d.failure }
func (d *Download) CreatedAt() time.Time  { return d.createdAt }

// EffectiveFilename returns the custom name if one was set, otherwise the
// title from the provider's MediaInfo. The extension is added by the provider.
func (d *Download) EffectiveFilename() string {
	if d.customName != "" {
		return d.customName
	}
	return d.info.Title
}

// Mutators ----------------------------------------------------------------

func (d *Download) SetCustomName(name string) {
	d.customName = name
}

// SelectQuality records the user's choice and moves AwaitingQuality -> Queued.
func (d *Download) SelectQuality(q Quality) error {
	if d.state != StateAwaitingQuality {
		return ErrInvalidState
	}
	d.quality = q
	d.hasQuality = true
	d.state = StateQueued
	return nil
}

// MarkQueued is reserved for the case where a Download is enqueued without
// going through SelectQuality first (currently unused; kept for symmetry).
func (d *Download) MarkQueued() error {
	if d.state != StateAwaitingQuality {
		return ErrInvalidState
	}
	d.state = StateQueued
	return nil
}

func (d *Download) MarkDownloading() error {
	if d.state != StateQueued {
		return ErrInvalidState
	}
	d.state = StateDownloading
	return nil
}

func (d *Download) MarkSending() error {
	if d.state != StateDownloading {
		return ErrInvalidState
	}
	d.state = StateSending
	return nil
}

func (d *Download) MarkComplete() error {
	if d.state != StateSending {
		return ErrInvalidState
	}
	d.state = StateComplete
	return nil
}

// MarkFailed is legal from any non-terminal state; terminal states reject it.
func (d *Download) MarkFailed(err error) error {
	switch d.state {
	case StateComplete, StateFailed:
		return ErrInvalidState
	}
	d.state = StateFailed
	d.failure = err
	return nil
}
