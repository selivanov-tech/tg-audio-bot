package domain

// Quality is the user-selectable audio quality tier.
// Best = opus (smaller file, better quality); Good = mp3 128k (widest compatibility).
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

// Label returns the user-facing button text.
func (q Quality) Label() string {
	switch q {
	case QualityBest:
		return "Best (Opus)"
	case QualityGood:
		return "Good (MP3)"
	default:
		return "Unknown"
	}
}
