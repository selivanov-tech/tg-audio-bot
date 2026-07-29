package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

func TestMapUserError_Nil(t *testing.T) {
	if got := MapUserError(StageFetch, nil); got != "" {
		t.Fatalf("want empty string for nil err, got %q", got)
	}
}

func TestMapUserError_DeadlineExceededPerStage(t *testing.T) {
	cases := map[Stage]string{
		StageFetch:    "YouTube",
		StageDownload: "Скачивание",
		StageSend:     "Отправка",
	}
	for stage, wantSubstr := range cases {
		got := MapUserError(stage, context.DeadlineExceeded)
		if !strings.Contains(got, wantSubstr) {
			t.Errorf("stage=%v: want substring %q, got %q", stage, wantSubstr, got)
		}
	}
}

func TestMapUserError_FileTooLarge(t *testing.T) {
	got := MapUserError(StageDownload, domain.ErrFileTooLarge)
	if !strings.Contains(got, "слишком большой") {
		t.Errorf("want too-large hint, got %q", got)
	}
}

func TestMapUserError_BotCheck(t *testing.T) {
	err := errors.New("ERROR: [youtube] abc: Sign in to confirm you're not a bot.")
	got := MapUserError(StageFetch, err)
	if !strings.Contains(got, "защита от ботов") {
		t.Errorf("want bot-check message, got %q", got)
	}
}

func TestMapUserError_CookiesHint(t *testing.T) {
	err := errors.New("Use --cookies for authentication")
	got := MapUserError(StageFetch, err)
	if !strings.Contains(got, "cookies") {
		t.Errorf("want cookies hint, got %q", got)
	}
}

func TestMapUserError_WrappedDeadline(t *testing.T) {
	// Error string contains "deadline exceeded" but the sentinel is not
	// reachable via errors.Is (e.g. third-party wrap).
	err := errors.New("yt-dlp fetch info: exit status 1: context deadline exceeded")
	got := MapUserError(StageFetch, err)
	if !strings.Contains(got, "YouTube") {
		t.Errorf("want timeout message, got %q", got)
	}
}

func TestMapUserError_VoiceForbidden(t *testing.T) {
	err := errors.New("bad request, Bad Request: VOICE_MESSAGES_FORBIDDEN")
	got := MapUserError(StageSend, err)
	if !strings.Contains(got, "голосовых") {
		t.Errorf("want voice-messages guard, got %q", got)
	}
}

func TestMapUserError_GenericPerStage(t *testing.T) {
	err := errors.New("totally unrelated failure")
	cases := map[Stage]string{
		StageFetch:    "приватным",
		StageDownload: "скачать",
		StageSend:     "отправить",
	}
	for stage, wantSubstr := range cases {
		got := MapUserError(stage, err)
		if !strings.Contains(got, wantSubstr) {
			t.Errorf("stage=%v: want substring %q, got %q", stage, wantSubstr, got)
		}
	}
}

func TestMsgQueued(t *testing.T) {
	got := MsgQueued(3)
	if !strings.Contains(got, "3") || !strings.Contains(got, "очереди") {
		t.Errorf("unexpected MsgQueued output: %q", got)
	}
}

func TestMsgTooLarge(t *testing.T) {
	got := MsgTooLarge(170)
	if !strings.Contains(got, "170") || !strings.Contains(got, "50") {
		t.Errorf("want actual size + the limit, got %q", got)
	}
}

// Sanity: the function doesn't panic on odd inputs.
func TestMapUserError_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = MapUserError(Stage(42), fmt.Errorf(""))
}
