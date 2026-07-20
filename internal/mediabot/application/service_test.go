package application

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

// fakeProvider returns a fixed MediaInfo from FetchInfo. Download is not
// expected to be called by the duration-gate tests; it fails the test if it is.
type fakeProvider struct {
	t        *testing.T
	info     domain.MediaInfo
	matchURL string
}

func (p *fakeProvider) ID() domain.ProviderID         { return domain.ProviderID("fake") }
func (p *fakeProvider) MatchesURL(u string) bool      { return strings.HasPrefix(u, p.matchURL) }
func (p *fakeProvider) FetchInfo(context.Context, string) (domain.MediaInfo, error) {
	return p.info, nil
}
func (p *fakeProvider) Download(context.Context, domain.DownloadOptions) (string, error) {
	p.t.Fatal("Download should not be called when the video is rejected")
	return "", nil
}

// recordingMessenger captures every call a test needs to assert on.
type recordingMessenger struct {
	mu        sync.Mutex
	sent      []sentText
	edits     []editCall
	keyboards int
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
func (m *recordingMessenger) SendOptionsKeyboard(context.Context, int64, string, []port.KeyboardOption) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyboards++
	return 99, nil
}
func (m *recordingMessenger) SendAudio(context.Context, int64, string, string) error { return nil }
func (m *recordingMessenger) SendVideo(context.Context, int64, string, string) error { return nil }
func (m *recordingMessenger) DeleteMessage(context.Context, int64, int) error        { return nil }
func (m *recordingMessenger) AnswerCallback(context.Context, string) error           { return nil }
func (m *recordingMessenger) SetCommands(context.Context, []port.Command) error      { return nil }

func newServiceWithProvider(t *testing.T, p domain.Provider, maxDurationMin int) (*Service, *recordingMessenger) {
	t.Helper()
	reg := domain.NewProviderRegistry()
	reg.Register(p)
	msgr := &recordingMessenger{}
	svc := NewService(reg, msgr, t.TempDir(), 1, maxDurationMin, nil)
	return svc, msgr
}

func TestHandleURL_DurationGate(t *testing.T) {
	const url = "https://fake.example/video"

	t.Run("rejects video over the limit without creating a Download", func(t *testing.T) {
		p := &fakeProvider{
			t:        t,
			matchURL: "https://fake.example/",
			info: domain.MediaInfo{
				Provider: domain.ProviderID("fake"),
				Title:    "Very Long Video",
				Duration: 3 * time.Hour,
			},
		}
		svc, msgr := newServiceWithProvider(t, p, 45)

		svc.HandleURL(context.Background(), 42, url, "")

		if msgr.keyboards != 0 {
			t.Errorf("keyboard sent %d times, want 0", msgr.keyboards)
		}
		// The "Fetching..." placeholder is edited to the rejection message.
		wantText := MsgVideoTooLong(180, 45)
		found := false
		for _, e := range msgr.edits {
			if e.text == wantText {
				found = true
				break
			}
		}
		for _, s := range msgr.sent {
			if s.text == wantText {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("rejection message not emitted; edits=%v sent=%v", msgr.edits, msgr.sent)
		}
		// No pending Download should remain.
		svc.pendingMu.Lock()
		n := len(svc.pending)
		svc.pendingMu.Unlock()
		if n != 0 {
			t.Errorf("pending size = %d, want 0", n)
		}
	})

	t.Run("allows video at or under the limit (keyboard shown)", func(t *testing.T) {
		p := &fakeProvider{
			t:        t,
			matchURL: "https://fake.example/",
			info: domain.MediaInfo{
				Provider: domain.ProviderID("fake"),
				Title:    "Short",
				Duration: 30 * time.Minute,
			},
		}
		svc, msgr := newServiceWithProvider(t, p, 45)

		svc.HandleURL(context.Background(), 42, url, "")

		if msgr.keyboards != 1 {
			t.Errorf("keyboard sent %d times, want 1", msgr.keyboards)
		}
	})

	t.Run("allows video with unknown duration (0) through", func(t *testing.T) {
		p := &fakeProvider{
			t:        t,
			matchURL: "https://fake.example/",
			info: domain.MediaInfo{
				Provider: domain.ProviderID("fake"),
				Title:    "Livestream Or Unknown",
				Duration: 0,
			},
		}
		svc, msgr := newServiceWithProvider(t, p, 45)

		svc.HandleURL(context.Background(), 42, url, "")

		if msgr.keyboards != 1 {
			t.Errorf("keyboard sent %d times, want 1 (unknown duration must not be rejected)", msgr.keyboards)
		}
	})
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
