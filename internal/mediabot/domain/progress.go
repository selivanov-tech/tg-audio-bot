package domain

import "fmt"

// Progress is a snapshot of an in-flight download, produced by a provider
// and consumed by the application layer to render user-facing status messages.
// All string fields hold pre-formatted yt-dlp output (e.g. "5.23MiB", "1.2MiB/s")
// so the domain does not need unit-conversion logic.
type Progress struct {
	Percent   float64 // 0..100
	TotalSize string  // e.g. "5.23MiB" or "~5.23MiB"
	Speed     string  // e.g. "1.2MiB/s"
	ETA       string  // e.g. "00:03"
}

// DisplayString renders the progress for a Telegram message body.
func (p Progress) DisplayString() string {
	if p.Percent >= 100 {
		return "Downloading... 100%"
	}
	if p.TotalSize == "" {
		return fmt.Sprintf("Downloading... %.1f%%", p.Percent)
	}
	return fmt.Sprintf("Downloading... %.1f%% of %s (%s, ETA %s)",
		p.Percent, p.TotalSize, p.Speed, p.ETA)
}
