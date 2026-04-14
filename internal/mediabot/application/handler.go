package application

import (
	"context"
	"log/slog"
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
	log       *slog.Logger
	allowed   map[int64]struct{}
}

// NewHandler builds a Handler. allowed gates every entry point: chat IDs
// not in the map receive MsgAccessDenied and never reach the service. The
// map is required (empty/nil means deny-all); config.Load enforces this.
func NewHandler(svc *Service, messenger port.Messenger, logger *slog.Logger, allowed map[int64]struct{}) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, messenger: messenger, log: logger, allowed: allowed}
}

// isAllowed is the single gate point. Group chats (negative IDs) work the
// same as private chats — membership is exact.
func (h *Handler) isAllowed(chatID int64) bool {
	_, ok := h.allowed[chatID]
	return ok
}

// urlRe finds the first URL in a free-form message. Intentionally permissive:
// provider.MatchesURL does the real filtering.
var urlRe = regexp.MustCompile(`https?://\S+`)

func (h *Handler) HandleMessage(ctx context.Context, chatID int64, text string, _ int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	h.log.Info("msg received", "chat", chatID, "text_len", len(text))

	if !h.isAllowed(chatID) {
		h.log.Warn("access denied", "chat", chatID)
		_, _ = h.messenger.SendText(ctx, chatID, MsgAccessDenied)
		return
	}

	switch {
	case text == "/start", text == "/help":
		_, _ = h.messenger.SendText(ctx, chatID, MsgHelp)
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
		_, _ = h.messenger.SendText(ctx, chatID, MsgDownloadUsage)
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

	if !h.isAllowed(chatID) {
		h.log.Warn("access denied", "chat", chatID, "callback", data)
		_, _ = h.messenger.SendText(ctx, chatID, MsgAccessDenied)
		return
	}

	if !strings.HasPrefix(data, "quality:") {
		h.log.Warn("callback unknown", "chat", chatID, "data", data)
		return
	}
	q, ok := parseQuality(strings.TrimPrefix(data, "quality:"))
	if !ok {
		h.log.Warn("callback unknown", "chat", chatID, "data", data)
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
