package domain

import (
	"errors"
	"testing"
)

func newTestDownload() *Download {
	return NewDownload(42, "https://example.com/v", ProviderID("test"), MediaInfo{Title: "hello"})
}

func TestNewDownload_StartsAwaitingQuality(t *testing.T) {
	d := newTestDownload()
	if d.State() != StateAwaitingQuality {
		t.Fatalf("state = %v, want awaiting_quality", d.State())
	}
}

func TestEffectiveFilename_PrefersCustomName(t *testing.T) {
	d := newTestDownload()
	if got := d.EffectiveFilename(); got != "hello" {
		t.Fatalf("title fallback = %q, want hello", got)
	}
	d.SetCustomName("my-song")
	if got := d.EffectiveFilename(); got != "my-song" {
		t.Fatalf("custom name = %q, want my-song", got)
	}
}

func TestHappyPathTransitions(t *testing.T) {
	d := newTestDownload()
	if err := d.SelectQuality(QualityBest); err != nil {
		t.Fatalf("SelectQuality: %v", err)
	}
	if d.State() != StateQueued || d.Quality() != QualityBest {
		t.Fatalf("after SelectQuality: state=%v quality=%v", d.State(), d.Quality())
	}
	if err := d.MarkDownloading(); err != nil {
		t.Fatalf("MarkDownloading: %v", err)
	}
	if err := d.MarkSending(); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}
	if err := d.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if d.State() != StateComplete {
		t.Fatalf("final state = %v, want complete", d.State())
	}
}

func TestIllegalTransitions(t *testing.T) {
	// Can't MarkDownloading from AwaitingQuality.
	d := newTestDownload()
	if err := d.MarkDownloading(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("MarkDownloading from awaiting: err=%v, want ErrInvalidState", err)
	}

	// Can't MarkComplete from Queued.
	d = newTestDownload()
	_ = d.SelectQuality(QualityGood)
	if err := d.MarkComplete(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("MarkComplete from queued: err=%v, want ErrInvalidState", err)
	}

	// SelectQuality twice is illegal.
	d = newTestDownload()
	_ = d.SelectQuality(QualityGood)
	if err := d.SelectQuality(QualityBest); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("double SelectQuality: err=%v, want ErrInvalidState", err)
	}
}

func TestMarkFailed(t *testing.T) {
	boom := errors.New("boom")

	// From Downloading: legal.
	d := newTestDownload()
	_ = d.SelectQuality(QualityBest)
	_ = d.MarkDownloading()
	if err := d.MarkFailed(boom); err != nil {
		t.Fatalf("MarkFailed from downloading: %v", err)
	}
	if d.State() != StateFailed || !errors.Is(d.Failure(), boom) {
		t.Fatalf("after MarkFailed: state=%v failure=%v", d.State(), d.Failure())
	}

	// From Complete: illegal.
	d = newTestDownload()
	_ = d.SelectQuality(QualityBest)
	_ = d.MarkDownloading()
	_ = d.MarkSending()
	_ = d.MarkComplete()
	if err := d.MarkFailed(boom); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("MarkFailed from complete: err=%v, want ErrInvalidState", err)
	}

	// From Failed: illegal (no double-fail).
	d = newTestDownload()
	_ = d.SelectQuality(QualityBest)
	_ = d.MarkDownloading()
	_ = d.MarkFailed(boom)
	if err := d.MarkFailed(boom); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("double MarkFailed: err=%v, want ErrInvalidState", err)
	}
}
