package domain

// MediaType identifies what kind of media a provider returns.
// Phase 1 uses only Audio; Video is declared so providers and messenger
// contracts can already accept it without later breaking changes.
type MediaType int

const (
	MediaTypeAudio MediaType = iota
	MediaTypeVideo
)

func (m MediaType) String() string {
	switch m {
	case MediaTypeAudio:
		return "audio"
	case MediaTypeVideo:
		return "video"
	default:
		return "unknown"
	}
}
