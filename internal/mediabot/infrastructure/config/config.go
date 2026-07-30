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
	// MaxDurationMinutes rejects videos longer than this at FetchInfo time.
	// Default 45 min keeps the extracted audio close to Telegram's 50 MB cap
	// and avoids multi-hour ffmpeg transcodes that look frozen to the user
	// and almost always fail to send anyway. 0 or unset = default (not
	// unlimited — that would be a footgun).
	MaxDurationMinutes int
	AllowedChatIDs     map[int64]struct{}

	// PublicURL is the externally reachable base URL of this bot (no trailing
	// slash), e.g. "https://your-app.fly.dev". Used for two things:
	// (1) webhook registration with Telegram — "${PublicURL}/webhook"
	// (2) keep-alive self-ping while downloads are active — "${PublicURL}/health"
	// via the Fly proxy so the auto-stop idle timer resets.
	PublicURL string

	// WebhookSecret is a random token Telegram echoes back in the
	// X-Telegram-Bot-Api-Secret-Token header on each webhook POST. The
	// Telegram adapter validates it and silently drops forged requests.
	WebhookSecret string

	// Cookie mirror (object storage). Names follow what `fly storage create`
	// injects, so a provisioned Tigris bucket needs no extra wiring. An empty
	// CookieBucket disables the mirror and the bot falls back to the
	// YTDLP_COOKIES seed alone. Credentials are deliberately absent: the AWS
	// SDK reads AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY itself.
	CookieBucket  string // BUCKET_NAME
	CookieObject  string // MEDIABOT_COOKIES_OBJECT, default "yt-cookies.txt"
	S3EndpointURL string // AWS_ENDPOINT_URL_S3
	S3Region      string // AWS_REGION, default "auto"
}

// ErrMissingToken is returned when TELEGRAM_BOT_TOKEN is unset.
var ErrMissingToken = errors.New("TELEGRAM_BOT_TOKEN is required")

// ErrEmptyAllowlist is returned when MEDIABOT_ALLOWED_CHAT_IDS is unset
// or parses to zero IDs. Empty = deny-all by design: a misconfigured prod
// deploy should fail loudly at boot, not silently let everyone in.
var ErrEmptyAllowlist = errors.New("MEDIABOT_ALLOWED_CHAT_IDS is required and must contain at least one chat ID")

// ErrMissingPublicURL is returned when MEDIABOT_PUBLIC_URL is unset. The
// webhook transport has no polling fallback, so we fail loud at boot.
var ErrMissingPublicURL = errors.New("MEDIABOT_PUBLIC_URL is required (e.g. https://your-app.fly.dev)")

// ErrMissingWebhookSecret is returned when TELEGRAM_WEBHOOK_SECRET is unset.
// Without it the /webhook endpoint is publicly POST-able by anyone.
var ErrMissingWebhookSecret = errors.New("TELEGRAM_WEBHOOK_SECRET is required; generate with `openssl rand -hex 32`")

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

	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MEDIABOT_PUBLIC_URL")), "/")
	if publicURL == "" {
		return Config{}, ErrMissingPublicURL
	}

	webhookSecret := strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_SECRET"))
	if webhookSecret == "" {
		return Config{}, ErrMissingWebhookSecret
	}

	return Config{
		TelegramBotToken:      token,
		YtDlpPath:             envOrDefault("YTDLP_PATH", "yt-dlp"),
		YtDlpCookiesPath:      os.Getenv("YTDLP_COOKIES_PATH"),
		TempDir:               envOrDefault("MEDIABOT_TEMP_DIR", filepath.Join(os.TempDir(), "mediabot")),
		MaxConcurrentDownload: envIntOrDefault("MEDIABOT_MAX_CONCURRENT_DOWNLOADS", 3),
		MaxDurationMinutes:    envIntOrDefault("MEDIABOT_MAX_DURATION_MINUTES", 45),
		AllowedChatIDs:        allowed,
		PublicURL:             publicURL,
		WebhookSecret:         webhookSecret,
		CookieBucket:          strings.TrimSpace(os.Getenv("BUCKET_NAME")),
		CookieObject:          envOrDefault("MEDIABOT_COOKIES_OBJECT", "yt-cookies.txt"),
		S3EndpointURL:         strings.TrimSpace(os.Getenv("AWS_ENDPOINT_URL_S3")),
		S3Region:              envOrDefault("AWS_REGION", "auto"),
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
