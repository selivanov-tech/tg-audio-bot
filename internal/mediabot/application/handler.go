package application

import (
	"context"
	"regexp"
	"strings"

	"github.com/rag-brothers/media-bot/internal/mediabot/application/port"
	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// Handler routes Telegram updates to the Service. It satisfies
// infrastructure/telegram.UpdateHandler without importing that package:
// the method set is declared here and shared by interface satisfaction.
type Handler struct {
	svc       *Service
	messenger port.Messenger
}

func NewHandler(svc *Service, messenger port.Messenger) *Handler {
	return &Handler{svc: svc, messenger: messenger}
}

// urlRe finds the first URL in a free-form message. Intentionally permissive:
// provider.MatchesURL does the real filtering.
var urlRe = regexp.MustCompile(`https?://\S+`)

const helpText = "Send me a YouTube URL and I'll download the audio.\n\n" +
	"Optional: use /download <url> <name> to save it with a custom filename."

func (h *Handler) HandleMessage(ctx context.Context, chatID int64, text string, _ int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	switch {
	case text == "/start", text == "/help":
		_, _ = h.messenger.SendText(ctx, chatID, helpText)
		return
	case strings.HasPrefix(text, "/download"):
		h.handleDownloadCommand(ctx, chatID, text)
		return
	}

	if url := urlRe.FindString(text); url != "" {
		h.svc.HandleURL(ctx, chatID, url, "")
	}
	// Silent on anything else.
}

// handleDownloadCommand parses `/download <url> [custom name...]`.
// Anything after the URL is taken verbatim as the filename.
func (h *Handler) handleDownloadCommand(ctx context.Context, chatID int64, text string) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/download"))
	if rest == "" {
		_, _ = h.messenger.SendText(ctx, chatID, "Usage: /download <url> [name]")
		return
	}
	fields := strings.Fields(rest)
	url := fields[0]
	customName := strings.TrimSpace(strings.TrimPrefix(rest, url))
	h.svc.HandleURL(ctx, chatID, url, customName)
}

// HandleCallback routes inline-button taps. The only callback shape we
// produce is "quality:best" / "quality:good".
func (h *Handler) HandleCallback(ctx context.Context, chatID int64, data string, messageID int, callbackQueryID string) {
	// Ack immediately so the user's spinner dismisses.
	_ = h.messenger.AnswerCallback(ctx, callbackQueryID)

	if !strings.HasPrefix(data, "quality:") {
		return
	}
	q, ok := parseQuality(strings.TrimPrefix(data, "quality:"))
	if !ok {
		return
	}
	h.svc.HandleQualitySelection(ctx, chatID, q, messageID)
}

func parseQuality(s string) (domain.Quality, bool) {
	switch s {
	case domain.QualityBest.String():
		return domain.QualityBest, true
	case domain.QualityGood.String():
		return domain.QualityGood, true
	default:
		return 0, false
	}
}
