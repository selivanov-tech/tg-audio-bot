package domain

// Progress is a snapshot of an in-flight download, produced by a provider
// and consumed by the application layer to render user-facing status messages.
// All string fields hold pre-formatted yt-dlp output (e.g. "5.23MiB", "1.2MiB/s")
// so the domain does not need unit-conversion logic.
//
// Rendering lives in the application layer — domain has no UI concerns.
type Progress struct {
	Percent    float64 // 0..100
	TotalSize  string  // e.g. "5.23MiB" or "~5.23MiB"
	Speed      string  // e.g. "1.2MiB/s"
	ETA        string  // e.g. "00:03"
	Converting bool    // true once yt-dlp has entered post-processing (audio extract / transmux)
}
