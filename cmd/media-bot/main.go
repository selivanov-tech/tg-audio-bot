// Package main is the media bot entry point. It wires adapters to the
// application layer and runs the Telegram polling loop until interrupted.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/rag-brothers/media-bot/internal/mediabot/application"
	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
	"github.com/rag-brothers/media-bot/internal/mediabot/infrastructure/config"
	"github.com/rag-brothers/media-bot/internal/mediabot/infrastructure/provider/youtube"
	"github.com/rag-brothers/media-bot/internal/mediabot/infrastructure/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		log.Fatalf("temp dir: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- adapters -----------------------------------------------------------
	registry := domain.NewProviderRegistry()
	registry.Register(youtube.New(youtube.Config{
		YtDlpPath:   cfg.YtDlpPath,
		CookiesPath: cfg.YtDlpCookiesPath,
	}))

	// Build application with a nil messenger first, then attach the real
	// one. Two-step because the Telegram adapter needs the handler, and the
	// handler needs the messenger. We resolve the cycle by giving the
	// service a forward reference to a messenger holder.
	//
	// Simpler: construct messenger first with a deferred-binding handler.
	var handler *application.Handler
	bot, err := telegram.NewBot(cfg.TelegramBotToken, &lazyHandler{get: func() *application.Handler { return handler }})
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}

	svc := application.NewService(registry, bot, cfg.TempDir, cfg.MaxConcurrentDownload)
	handler = application.NewHandler(svc, bot)

	log.Printf("media-bot starting: max_concurrent=%d temp_dir=%s", cfg.MaxConcurrentDownload, cfg.TempDir)
	bot.Start(ctx)
	log.Printf("media-bot shutdown")
}

// lazyHandler defers resolution of the real *Handler until the first update
// arrives — by which time main has finished wiring. This breaks the
// construction cycle between the Telegram adapter and the application
// handler (each needs a reference to the other).
type lazyHandler struct {
	get func() *application.Handler
}

func (l *lazyHandler) HandleMessage(ctx context.Context, chatID int64, text string, messageID int) {
	if h := l.get(); h != nil {
		h.HandleMessage(ctx, chatID, text, messageID)
	}
}

func (l *lazyHandler) HandleCallback(ctx context.Context, chatID int64, data string, messageID int, callbackQueryID string) {
	if h := l.get(); h != nil {
		h.HandleCallback(ctx, chatID, data, messageID, callbackQueryID)
	}
}
