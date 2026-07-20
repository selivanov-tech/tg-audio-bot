// Package main is the media bot entry point. It wires adapters to the
// application layer and runs the Telegram webhook dispatcher until
// interrupted.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/infrastructure/config"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/infrastructure/provider/youtube"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/infrastructure/telegram"
)

func main() {
	// Structured text logs — human-readable in Fly's log UI, grep-friendly.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

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
	bot, err := telegram.NewBot(cfg.TelegramBotToken, cfg.WebhookSecret, &lazyHandler{get: func() *application.Handler { return handler }})
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}

	svc := application.NewService(registry, bot, cfg.TempDir, cfg.MaxConcurrentDownload, cfg.MaxDurationMinutes, logger)
	handler = application.NewHandler(svc, bot, logger, cfg.AllowedChatIDs)

	// --- HTTP server --------------------------------------------------------
	// Serves both /health (Fly health check) and /webhook (Telegram updates).
	// Must start before RegisterWebhook so Telegram's first POST has somewhere
	// to land.
	httpSrv := startHTTPServer(bot.WebhookHandler())

	// Register the "/" command menu. Best-effort: log the error but don't fail
	// startup — the bot still works without the menu hints.
	if err := bot.SetCommands(ctx, []port.Command{
		{Command: "start", Description: "Помощь и примеры"},
		{Command: "help", Description: "Как пользоваться ботом"},
		{Command: "download", Description: "Скачать с именем файла"},
	}); err != nil {
		slog.Warn("set commands failed", "err", err)
	}

	// Tell Telegram where to POST updates. Idempotent — calling on every boot
	// is safe and self-heals after any manual re-registration drift.
	webhookURL := cfg.PublicURL + "/webhook"
	if err := bot.RegisterWebhook(ctx, webhookURL, cfg.WebhookSecret); err != nil {
		log.Fatalf("telegram: register webhook: %v", err)
	}
	slog.Info("webhook registered", "url", webhookURL)

	// Keep-alive: while the queue has active downloads, ping /health over the
	// Fly proxy every 2 min so auto-stop's idle timer doesn't fire mid-work.
	keepAlive := application.NewKeepAlive(svc.Queue(), cfg.PublicURL, 0, logger)
	go keepAlive.Run(ctx)

	slog.Info("media-bot starting",
		"max_concurrent", cfg.MaxConcurrentDownload,
		"max_duration_min", cfg.MaxDurationMinutes,
		"temp_dir", cfg.TempDir,
		"allowed_chats", len(cfg.AllowedChatIDs),
		"public_url", cfg.PublicURL)
	// Blocks on the webhook dispatcher goroutine, draining the internal
	// updates channel populated by WebhookHandler until ctx is cancelled.
	bot.Start(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	slog.Info("media-bot shutdown")
}

// startHTTPServer launches the HTTP server on $PORT (default 8080). It
// exposes:
//
//   - GET /health for Fly's health check (also hit by the keep-alive self-ping).
//   - POST /webhook for Telegram updates, delegated to the library's handler
//     which validates the X-Telegram-Bot-Api-Secret-Token header and enqueues
//     the decoded update onto the internal dispatcher channel.
//
// The caller is responsible for calling Shutdown on ctx cancel.
func startHTTPServer(webhookHandler http.Handler) *http.Server {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/webhook", webhookHandler)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("http server listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
		}
	}()

	return srv
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
