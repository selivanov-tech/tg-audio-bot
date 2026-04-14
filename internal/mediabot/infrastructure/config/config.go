// Package config loads bot configuration from environment variables.
// All non-secret defaults live here; main.go is the only caller.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	TelegramBotToken      string
	YtDlpPath             string
	YtDlpCookiesPath      string // empty = no cookies
	TempDir               string
	MaxConcurrentDownload int
	AllowedChatIDs        map[int64]struct{}
}

// ErrMissingToken is returned when TELEGRAM_BOT_TOKEN is unset.
var ErrMissingToken = errors.New("TELEGRAM_BOT_TOKEN is required")

// ErrEmptyAllowlist is returned when MEDIABOT_ALLOWED_CHAT_IDS is unset
// or parses to zero IDs. Empty = deny-all by design: a misconfigured prod
// deploy should fail loudly at boot, not silently let everyone in.
var ErrEmptyAllowlist = errors.New("MEDIABOT_ALLOWED_CHAT_IDS is required and must contain at least one chat ID")

// Load reads config from the environment. It returns ErrMissingToken when
// the Telegram token is absent; other fields fall back to sensible defaults.
func Load() (Config, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return Config{}, ErrMissingToken
	}

	allowed, err := parseAllowedChatIDs(os.Getenv("MEDIABOT_ALLOWED_CHAT_IDS"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		TelegramBotToken:      token,
		YtDlpPath:             envOrDefault("YTDLP_PATH", "yt-dlp"),
		YtDlpCookiesPath:      os.Getenv("YTDLP_COOKIES_PATH"),
		TempDir:               envOrDefault("MEDIABOT_TEMP_DIR", filepath.Join(os.TempDir(), "mediabot")),
		MaxConcurrentDownload: envIntOrDefault("MEDIABOT_MAX_CONCURRENT_DOWNLOADS", 3),
		AllowedChatIDs:        allowed,
	}, nil
}

// parseAllowedChatIDs parses a comma-separated list of int64 chat IDs.
// Whitespace around tokens and trailing commas are tolerated. Returns
// ErrEmptyAllowlist when the result would have zero entries — empty means
// deny-all, so a misconfig must fail loud at boot.
func parseAllowedChatIDs(raw string) (map[int64]struct{}, error) {
	out := make(map[int64]struct{})
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		id, err := strconv.ParseInt(tok, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MEDIABOT_ALLOWED_CHAT_IDS: invalid chat id %q: %w", tok, err)
		}
		out[id] = struct{}{}
	}
	if len(out) == 0 {
		return nil, ErrEmptyAllowlist
	}
	return out, nil
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
