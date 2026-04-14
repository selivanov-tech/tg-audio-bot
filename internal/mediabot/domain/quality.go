package domain

// Quality is the user-selectable audio quality tier.
// Best = m4a/AAC (high quality); Good = mp3 128k (widest compatibility).
//
// User-facing labels live in the application layer — domain has no UI concerns.
type Quality int

const (
	QualityBest Quality = iota
	QualityGood
)

// String returns the machine-facing identifier (used in callback data).
func (q Quality) String() string {
	switch q {
	case QualityBest:
		return "best"
	case QualityGood:
		return "good"
	default:
		return "unknown"
	}
}
