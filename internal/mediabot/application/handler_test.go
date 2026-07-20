package application

import (
	"context"
	"testing"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
)

// fakeMessenger records SendText calls. Other methods are stubbed: the
// allowlist tests only exercise the deny path and the simple /start path,
// neither of which touches audio/video/keyboard/etc.
type fakeMessenger struct {
	sent              []sentText
	answeredCallbacks []string
}

type sentText struct {
	chatID int64
	text   string
}

func (f *fakeMessenger) SendText(_ context.Context, chatID int64, text string) (int, error) {
	f.sent = append(f.sent, sentText{chatID, text})
	return 1, nil
}

func (f *fakeMessenger) AnswerCallback(_ context.Context, id string) error {
	f.answeredCallbacks = append(f.answeredCallbacks, id)
	return nil
}

func (f *fakeMessenger) EditText(context.Context, int64, int, string) error { return nil }
func (f *fakeMessenger) SendOptionsKeyboard(context.Context, int64, string, []port.KeyboardOption) (int, error) {
	return 0, nil
}
func (f *fakeMessenger) SendAudio(context.Context, int64, string, string) error { return nil }
func (f *fakeMessenger) SendVideo(context.Context, int64, string, string) error { return nil }
func (f *fakeMessenger) DeleteMessage(context.Context, int64, int) error        { return nil }
func (f *fakeMessenger) SetCommands(context.Context, []port.Command) error      { return nil }

func TestHandler_AllowlistGate(t *testing.T) {
	allowed := map[int64]struct{}{42: {}, -100500: {}}

	t.Run("HandleMessage denies unknown chat with MsgAccessDenied", func(t *testing.T) {
		m := &fakeMessenger{}
		// nil svc: deny path must early-return before touching it
		h := NewHandler(nil, m, nil, allowed)

		h.HandleMessage(context.Background(), 999, "https://youtube.com/watch?v=x", 0)

		if len(m.sent) != 1 {
			t.Fatalf("sent = %d, want 1", len(m.sent))
		}
		if m.sent[0].text != MsgAccessDenied {
			t.Errorf("text = %q, want MsgAccessDenied", m.sent[0].text)
		}
	})

	t.Run("HandleMessage allows /start for allowed chat (sends MsgHelp)", func(t *testing.T) {
		m := &fakeMessenger{}
		h := NewHandler(nil, m, nil, allowed)

		h.HandleMessage(context.Background(), 42, "/start", 0)

		if len(m.sent) != 1 || m.sent[0].text != MsgHelp {
			t.Fatalf("sent = %+v, want MsgHelp", m.sent)
		}
	})

	t.Run("HandleMessage allows group chat (negative ID)", func(t *testing.T) {
		m := &fakeMessenger{}
		h := NewHandler(nil, m, nil, allowed)

		h.HandleMessage(context.Background(), -100500, "/help", 0)

		if len(m.sent) != 1 || m.sent[0].text != MsgHelp {
			t.Fatalf("sent = %+v, want MsgHelp for group chat", m.sent)
		}
	})

	t.Run("HandleCallback denies unknown chat after acking", func(t *testing.T) {
		m := &fakeMessenger{}
		h := NewHandler(nil, m, nil, allowed)

		h.HandleCallback(context.Background(), 999, "quality:best", 1, "cb-id")

		if len(m.answeredCallbacks) != 1 || m.answeredCallbacks[0] != "cb-id" {
			t.Errorf("callback not acked: %v", m.answeredCallbacks)
		}
		if len(m.sent) != 1 || m.sent[0].text != MsgAccessDenied {
			t.Fatalf("sent = %+v, want MsgAccessDenied", m.sent)
		}
	})

	t.Run("nil allowlist denies everyone", func(t *testing.T) {
		m := &fakeMessenger{}
		h := NewHandler(nil, m, nil, nil)

		h.HandleMessage(context.Background(), 42, "/start", 0)

		if len(m.sent) != 1 || m.sent[0].text != MsgAccessDenied {
			t.Fatalf("nil allowlist did not deny: %+v", m.sent)
		}
	})
}
