package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

// fakeProvider returns a fixed MediaInfo from FetchInfo and writes a stub file
// on Download, publishing the options it was called with on a channel so tests
// can assert what the service asked for (and that it asked at all).
type fakeProvider struct {
	info      domain.MediaInfo
	matchURL  string
	fileBytes int64 // size of the stub file; 0 writes a few bytes
	downloads chan domain.DownloadOptions
}

func (p *fakeProvider) ID() domain.ProviderID    { return domain.ProviderID("fake") }
func (p *fakeProvider) MatchesURL(u string) bool { return strings.HasPrefix(u, p.matchURL) }
func (p *fakeProvider) FetchInfo(context.Context, string) (domain.MediaInfo, error) {
	return p.info, nil
}
func (p *fakeProvider) Download(_ context.Context, opts domain.DownloadOptions) (string, error) {
	path := filepath.Join(opts.OutputDir, opts.Filename+".m4a")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// Truncate keeps the oversize case cheap: the file is sparse.
	if err := f.Truncate(p.fileBytes); err != nil {
		return "", err
	}
	p.downloads <- opts
	return path, nil
}

// recordingMessenger captures every call a test needs to assert on.
type recordingMessenger struct {
	mu        sync.Mutex
	sent      []sentText
	edits     []editCall
	keyboards []keyboardCall
	audio     []string
}

type keyboardCall struct {
	text string
	opts []port.KeyboardOption
}

type editCall struct {
	chatID int64
	msgID  int
	text   string
}

func (m *recordingMessenger) SendText(_ context.Context, chatID int64, text string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentText{chatID, text})
	return len(m.sent), nil
}
func (m *recordingMessenger) EditText(_ context.Context, chatID int64, msgID int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.edits = append(m.edits, editCall{chatID, msgID, text})
	return nil
}
func (m *recordingMessenger) SendOptionsKeyboard(_ context.Context, _ int64, text string, opts []port.KeyboardOption) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyboards = append(m.keyboards, keyboardCall{text, opts})
	return 99, nil
}
func (m *recordingMessenger) SendAudio(_ context.Context, _ int64, path, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audio = append(m.audio, path)
	return nil
}
func (m *recordingMessenger) SendVideo(context.Context, int64, string, string) error { return nil }
func (m *recordingMessenger) DeleteMessage(context.Context, int64, int) error        { return nil }
func (m *recordingMessenger) AnswerCallback(context.Context, string) error           { return nil }
func (m *recordingMessenger) SetCommands(context.Context, []port.Command) error      { return nil }

// texts returns every message body the service produced, sent or edited.
func (m *recordingMessenger) texts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sent)+len(m.edits))
	for _, s := range m.sent {
		out = append(out, s.text)
	}
	for _, e := range m.edits {
		out = append(out, e.text)
	}
	return out
}

func (m *recordingMessenger) keyboardCalls() []keyboardCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]keyboardCall(nil), m.keyboards...)
}

func containsText(texts []string, want string) bool {
	for _, t := range texts {
		if strings.Contains(t, want) {
			return true
		}
	}
	return false
}

// waitFor polls cond until it holds, failing the test on timeout. Downloads run
// on the queue's worker goroutine, so assertions on them cannot be immediate.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newServiceWithProvider(t *testing.T, p domain.Provider, maxDurationMin int) (*Service, *recordingMessenger) {
	t.Helper()
	reg := domain.NewProviderRegistry()
	reg.Register(p)
	msgr := &recordingMessenger{}
	svc := NewService(reg, msgr, t.TempDir(), 1, maxDurationMin, nil)
	return svc, msgr
}

func newFakeProvider(title string, duration time.Duration, fileBytes int64) *fakeProvider {
	return &fakeProvider{
		matchURL:  "https://fake.example/",
		fileBytes: fileBytes,
		downloads: make(chan domain.DownloadOptions, 4),
		info: domain.MediaInfo{
			Provider: domain.ProviderID("fake"),
			Title:    title,
			Duration: duration,
		},
	}
}

func TestHandleURL_DurationGate(t *testing.T) {
	const url = "https://fake.example/video"

	t.Run("rejects video over the limit without creating a Download", func(t *testing.T) {
		p := newFakeProvider("Very Long Video", 3*time.Hour, 128)
		svc, msgr := newServiceWithProvider(t, p, 45)

		svc.HandleURL(context.Background(), 42, url, "")

		if n := len(msgr.keyboardCalls()); n != 0 {
			t.Errorf("keyboard sent %d times, want 0", n)
		}
		// The "Fetching..." placeholder is edited to the rejection message.
		if !containsText(msgr.texts(), MsgVideoTooLong(180, 45)) {
			t.Errorf("rejection message not emitted; texts=%q", msgr.texts())
		}
		select {
		case opts := <-p.downloads:
			t.Errorf("download started for a rejected video: %+v", opts)
		case <-time.After(50 * time.Millisecond):
		}
		// No pending Download should remain.
		svc.pendingMu.Lock()
		n := len(svc.pending)
		svc.pendingMu.Unlock()
		if n != 0 {
			t.Errorf("pending size = %d, want 0", n)
		}
	})

	t.Run("allows video at or under the limit", func(t *testing.T) {
		p := newFakeProvider("Short", 30*time.Minute, 128)
		svc, _ := newServiceWithProvider(t, p, 45)

		svc.HandleURL(context.Background(), 42, url, "")

		waitFor(t, "download of an allowed video", func() bool { return len(p.downloads) == 1 })
	})

	t.Run("allows video with unknown duration (0) through", func(t *testing.T) {
		p := newFakeProvider("Livestream Or Unknown", 0, 128)
		svc, _ := newServiceWithProvider(t, p, 45)

		svc.HandleURL(context.Background(), 42, url, "")

		waitFor(t, "download of a video with unknown duration", func() bool { return len(p.downloads) == 1 })
	})
}

// The bot must not ask for a quality tier: Best starts on its own.
func TestHandleURL_StartsBestQualityWithoutAsking(t *testing.T) {
	p := newFakeProvider("Some Track", 3*time.Minute, 128)
	svc, msgr := newServiceWithProvider(t, p, 45)

	svc.HandleURL(context.Background(), 42, "https://fake.example/video", "My Name")

	var opts domain.DownloadOptions
	select {
	case opts = <-p.downloads:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the download to start")
	}
	if opts.Quality != domain.QualityBest {
		t.Errorf("quality = %v, want Best", opts.Quality)
	}
	if opts.Filename != "My Name" {
		t.Errorf("filename = %q, want the custom name", opts.Filename)
	}
	if n := len(msgr.keyboardCalls()); n != 0 {
		t.Errorf("keyboard sent %d times, want 0 (quality must not be asked)", n)
	}

	waitFor(t, "the audio to be sent", func() bool {
		msgr.mu.Lock()
		defer msgr.mu.Unlock()
		return len(msgr.audio) == 1
	})
	// Status messages carry the title so parallel downloads stay distinguishable.
	if !containsText(msgr.texts(), "My Name (Some Track)") {
		t.Errorf("status messages lack the title header; texts=%q", msgr.texts())
	}
}

// A Best-quality file over Telegram's cap is the one case where the tier
// matters, so the user gets a one-tap MP3 retry instead of a dead end.
func TestProcess_OversizeFileOffersMp3Retry(t *testing.T) {
	const url = "https://fake.example/video"
	p := newFakeProvider("Huge Track", 40*time.Minute, 51*1024*1024)
	svc, msgr := newServiceWithProvider(t, p, 45)

	svc.HandleURL(context.Background(), 42, url, "")

	waitFor(t, "the MP3 retry keyboard", func() bool { return len(msgr.keyboardCalls()) == 1 })

	kb := msgr.keyboardCalls()[0]
	if kb.text != MsgRetryLowerQuality {
		t.Errorf("keyboard text = %q, want MsgRetryLowerQuality", kb.text)
	}
	if len(kb.opts) != 1 || kb.opts[0].CallbackData != "quality:"+domain.QualityGood.String() {
		t.Errorf("keyboard options = %+v, want a single Good option", kb.opts)
	}
	if !containsText(msgr.texts(), MsgTooLarge(51)) {
		t.Errorf("size error not shown; texts=%q", msgr.texts())
	}

	// The retry is pending under the keyboard's message ID, so tapping the
	// button runs the same URL at Good quality.
	svc.pendingMu.Lock()
	retry, ok := svc.pending[99]
	svc.pendingMu.Unlock()
	if !ok {
		t.Fatal("no pending retry registered for the keyboard message")
	}
	if retry.URL() != url || retry.State() != domain.StateAwaitingQuality {
		t.Errorf("retry download = %q/%v, want the same URL awaiting quality", retry.URL(), retry.State())
	}

	svc.HandleQualitySelection(context.Background(), 42, domain.QualityGood, 99)
	waitFor(t, "the MP3 retry download", func() bool { return len(p.downloads) == 2 })
	<-p.downloads
	second := <-p.downloads
	if second.Quality != domain.QualityGood {
		t.Errorf("retry quality = %v, want Good", second.Quality)
	}
}

func TestNewService_MaxDurationDefault(t *testing.T) {
	reg := domain.NewProviderRegistry()
	msgr := &recordingMessenger{}
	for _, in := range []int{-1, 0} {
		svc := NewService(reg, msgr, t.TempDir(), 1, in, nil)
		if svc.maxDuration != 45*time.Minute {
			t.Errorf("maxDuration for input %d = %v, want 45m", in, svc.maxDuration)
		}
	}
	svc := NewService(reg, msgr, t.TempDir(), 1, 90, nil)
	if svc.maxDuration != 90*time.Minute {
		t.Errorf("maxDuration for 90 = %v, want 90m", svc.maxDuration)
	}
}
