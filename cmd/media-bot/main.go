// Package main is the media bot entry point. It wires adapters to the
// application layer and runs the Telegram polling loop until interrupted.
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

	"github.com/rag-brothers/media-bot/internal/mediabot/application"
	"github.com/rag-brothers/media-bot/internal/mediabot/application/port"
	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
	"github.com/rag-brothers/media-bot/internal/mediabot/infrastructure/config"
	"github.com/rag-brothers/media-bot/internal/mediabot/infrastructure/provider/youtube"
	"github.com/rag-brothers/media-bot/internal/mediabot/infrastructure/telegram"
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

	// --- health server ------------------------------------------------------
	// Fly.io hits GET /health every 30s. Without a 200 reply the machine is
	// marked unhealthy and restarted.
	healthSrv := startHealthServer(ctx)

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

	svc := application.NewService(registry, bot, cfg.TempDir, cfg.MaxConcurrentDownload, logger)
	handler = application.NewHandler(svc, bot, logger)

	// Register the Russian "/" command menu. Best-effort: log the error but
	// don't fail startup — the bot still works without the menu hints.
	if err := bot.SetCommands(ctx, []port.Command{
		{Command: "start", Description: "Помощь и примеры"},
		{Command: "help", Description: "Как пользоваться ботом"},
		{Command: "download", Description: "Скачать с именем файла"},
	}); err != nil {
		slog.Warn("set commands failed", "err", err)
	}

	slog.Info("media-bot starting",
		"max_concurrent", cfg.MaxConcurrentDownload,
		"temp_dir", cfg.TempDir)
	bot.Start(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = healthSrv.Shutdown(shutdownCtx)
	slog.Info("media-bot shutdown")
}

// startHealthServer launches a minimal HTTP server on $PORT (default 8080)
// that answers GET /health with 200 OK. It shuts down gracefully when ctx
// is cancelled.
func startHealthServer(ctx context.Context) *http.Server {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("health server listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health server", "err", err)
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
