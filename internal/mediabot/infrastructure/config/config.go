// Package config loads bot configuration from environment variables.
// All non-secret defaults live here; main.go is the only caller.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	TelegramBotToken      string
	YtDlpPath             string
	YtDlpCookiesPath      string // empty = no cookies
	TempDir               string
	MaxConcurrentDownload int
}

// ErrMissingToken is returned when TELEGRAM_BOT_TOKEN is unset.
var ErrMissingToken = errors.New("TELEGRAM_BOT_TOKEN is required")

// Load reads config from the environment. It returns ErrMissingToken when
// the Telegram token is absent; other fields fall back to sensible defaults.
func Load() (Config, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return Config{}, ErrMissingToken
	}

	return Config{
		TelegramBotToken:      token,
		YtDlpPath:             envOrDefault("YTDLP_PATH", "yt-dlp"),
		YtDlpCookiesPath:      os.Getenv("YTDLP_COOKIES_PATH"),
		TempDir:               envOrDefault("MEDIABOT_TEMP_DIR", filepath.Join(os.TempDir(), "mediabot")),
		MaxConcurrentDownload: envIntOrDefault("MEDIABOT_MAX_CONCURRENT_DOWNLOADS", 3),
	}, nil
}

func envOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func envIntOrDefault(key string, defaultValue int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return defaultValue
	}
	return n
}
