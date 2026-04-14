// Package youtube implements the YouTube audio provider.
// progress_parser.go is separated from provider.go so it can be unit-tested
// without shelling out to yt-dlp.
package youtube

import (
	"regexp"
	"strconv"

	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// yt-dlp writes progress to stderr. Typical line shapes:
//
//   [download]   0.0% of   5.23MiB at  Unknown B/s ETA Unknown
//   [download]  45.2% of 5.23MiB at 1.2MiB/s ETA 00:03
//   [download]  12.3% of ~5.23MiB at 800KiB/s ETA 00:06
//   [download] 100% of 5.23MiB in 00:04
//   [download] 100% of 5.23MiB
//
// The regex accepts integer or decimal percent, the optional "~" on total size,
// and tolerates "Unknown" for speed and ETA during the first tick.
var progressRe = regexp.MustCompile(
	`^\[download\]\s+(\d+(?:\.\d+)?)%\s+of\s+~?(\S+)` +
		`(?:\s+at\s+(\S+))?` +
		`(?:\s+(?:ETA|in)\s+(\S+))?`,
)

// ParseProgressLine extracts a Progress snapshot from a single yt-dlp stderr
// line. The bool is false when the line is not a progress line (e.g. destination
// banners, "has already been downloaded", empty lines).
func ParseProgressLine(line string) (domain.Progress, bool) {
	m := progressRe.FindStringSubmatch(line)
	if m == nil {
		return domain.Progress{}, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return domain.Progress{}, false
	}
	p := domain.Progress{
		Percent:   pct,
		TotalSize: m[2],
		Speed:     m[3],
		ETA:       m[4],
	}
	if p.Speed == "Unknown" || p.Speed == "" {
		p.Speed = ""
	}
	if p.ETA == "Unknown" || p.ETA == "" {
		p.ETA = ""
	}
	return p, true
}
