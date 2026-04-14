package domain

// Quality is the user-selectable audio quality tier.
// Best = m4a/AAC (high quality); Good = mp3 128k (widest compatibility).
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

// Label returns the user-facing button text (Russian).
func (q Quality) Label() string {
	switch q {
	case QualityBest:
		return "Лучшее (M4A)"
	case QualityGood:
		return "Хорошее (MP3)"
	default:
		return "Неизвестно"
	}
}
