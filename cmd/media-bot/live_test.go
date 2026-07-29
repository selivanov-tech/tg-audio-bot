//go:build live

// Live end-to-end harness: real yt-dlp, real message routing, no Telegram.
//
// It wires the same adapters main() does, except the Messenger port is a
// console stub — so every text, edit, keyboard and audio upload is printed
// instead of sent. Run it to check the download pipeline locally without a
// bot token, a webhook, or a public URL:
//
//	make test-live URL="https://www.youtube.com/watch?v=..."
//
// The build tag keeps it out of `go test ./...` and CI: it needs the network
// and takes as long as the download does.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/infrastructure/provider/youtube"
)

// liveChatID stands in for a real chat. It is also the only allowlisted ID,
// so the allowlist gate is exercised too.
const liveChatID int64 = 1

// consoleMessenger implements port.Messenger by printing to the test log and
// copying any "uploaded" audio somewhere durable — the service deletes its
// temp dir as soon as Process returns.
type consoleMessenger struct {
	t      *testing.T
	outDir string

	mu       sync.Mutex
	nextID   int
	lastText string   // most recent message body, for the failure report
	saved    []string // where each "uploaded" file landed
}

func (m *consoleMessenger) SendText(_ context.Context, chatID int64, text string) (int, error) {
	id := m.record(text)
	m.t.Logf("[send #%d chat=%d] %s", id, chatID, text)
	return id, nil
}

func (m *consoleMessenger) EditText(_ context.Context, _ int64, msgID int, text string) error {
	m.record(text)
	m.t.Logf("[edit #%d] %s", msgID, text)
	return nil
}

func (m *consoleMessenger) SendOptionsKeyboard(_ context.Context, chatID int64, text string, opts []port.KeyboardOption) (int, error) {
	id := m.record(text)
	m.t.Logf("[keyboard #%d chat=%d] %s", id, chatID, text)
	for _, o := range opts {
		m.t.Logf("    button %q -> %s", o.Label, o.CallbackData)
	}
	return id, nil
}

func (m *consoleMessenger) SendAudio(_ context.Context, _ int64, filePath, filename string) error {
	saved, size, err := copyOut(filePath, m.outDir)
	if err != nil {
		return err
	}
	m.t.Logf("[audio] %q — %.1f MB -> %s", filename, float64(size)/(1024*1024), saved)
	m.mu.Lock()
	m.saved = append(m.saved, saved)
	m.mu.Unlock()
	return nil
}

// record assigns the next message ID and remembers the body.
func (m *consoleMessenger) record(text string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.lastText = text
	return m.nextID
}

func (m *consoleMessenger) result() (saved []string, lastText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.saved...), m.lastText
}

func (m *consoleMessenger) SendVideo(ctx context.Context, chatID int64, filePath, filename string) error {
	return m.SendAudio(ctx, chatID, filePath, filename)
}

func (m *consoleMessenger) DeleteMessage(_ context.Context, _ int64, msgID int) error {
	m.t.Logf("[delete #%d]", msgID)
	return nil
}

func (m *consoleMessenger) AnswerCallback(context.Context, string) error      { return nil }
func (m *consoleMessenger) SetCommands(context.Context, []port.Command) error { return nil }

func copyOut(src, outDir string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = in.Close() }()

	dst := filepath.Join(outDir, filepath.Base(src))
	out, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = out.Close() }()

	size, err := io.Copy(out, in)
	return dst, size, err
}

// TestLiveDownload drives one message through Handler -> Service -> yt-dlp.
//
// Env:
//
//	URL          the YouTube link (required; the test skips without it)
//	LIVE_TEXT    full message text to route instead of a bare URL, e.g.
//	             "/download <url> My Name" (optional)
//	LIVE_TIMEOUT how long to wait for the upload, Go duration (default 5m)
//	YTDLP_PATH, YTDLP_COOKIES_PATH, MEDIABOT_MAX_DURATION_MINUTES honour the
//	             same meaning they have in production.
func TestLiveDownload(t *testing.T) {
	url := os.Getenv("URL")
	if url == "" {
		t.Skip("set URL=https://www.youtube.com/watch?v=... to run the live harness")
	}
	text := os.Getenv("LIVE_TEXT")
	if text == "" {
		text = url
	}
	timeout := 5 * time.Minute
	if raw := os.Getenv("LIVE_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("LIVE_TIMEOUT=%q: %v", raw, err)
		}
		timeout = d
	}
	maxDuration := 45
	if raw := os.Getenv("MEDIABOT_MAX_DURATION_MINUTES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("MEDIABOT_MAX_DURATION_MINUTES=%q: %v", raw, err)
		}
		maxDuration = n
	}

	ytdlp := os.Getenv("YTDLP_PATH")
	if ytdlp == "" {
		ytdlp = "yt-dlp"
	}

	// Deliberately not t.TempDir(): the result should survive the test so it
	// can be played back.
	outDir, err := os.MkdirTemp("", "mediabot-live-")
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	registry := domain.NewProviderRegistry()
	registry.Register(youtube.New(youtube.Config{
		YtDlpPath:   ytdlp,
		CookiesPath: os.Getenv("YTDLP_COOKIES_PATH"),
	}))

	msgr := &consoleMessenger{t: t, outDir: outDir}
	svc := application.NewService(registry, msgr, t.TempDir(), 1, maxDuration, logger)
	handler := application.NewHandler(svc, msgr, logger, map[int64]struct{}{liveChatID: {}})

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	t.Logf("routing %q (max duration %d min, timeout %s, output dir %s)", text, maxDuration, timeout, outDir)
	handler.HandleMessage(ctx, liveChatID, text, 1)

	// HandleMessage returns as soon as the download is queued — or right away
	// if the URL never got that far. Queue.Active() counts queued-or-running
	// work, so it hitting 0 without a file means the run is over (rejected,
	// failed, or waiting on a button we cannot tap). Check the file first:
	// SendAudio records it before Process returns, so a zero count with no
	// file recorded is genuinely terminal.
	for {
		saved, lastText := msgr.result()
		if len(saved) > 0 {
			fmt.Printf("\nsaved: %s\n", saved[len(saved)-1])
			return
		}
		if svc.Queue().Active() == 0 {
			t.Fatalf("pipeline stopped with no audio.\nlast message to the user: %q", lastText)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no audio after %s — see the log above for the stage that stalled", timeout)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
