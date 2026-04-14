package config

import (
	"errors"
	"testing"
)

func TestParseAllowedChatIDs(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		got, err := parseAllowedChatIDs("123,456,789")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		for _, id := range []int64{123, 456, 789} {
			if _, ok := got[id]; !ok {
				t.Errorf("missing id %d", id)
			}
		}
		if len(got) != 3 {
			t.Errorf("len = %d, want 3", len(got))
		}
	})

	t.Run("whitespace and trailing commas tolerated", func(t *testing.T) {
		got, err := parseAllowedChatIDs("  123 , 456 ,, ,789,")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("len = %d, want 3 (got %v)", len(got), got)
		}
	})

	t.Run("negative IDs (group chats) supported", func(t *testing.T) {
		got, err := parseAllowedChatIDs("-100123,456")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if _, ok := got[-100123]; !ok {
			t.Errorf("negative id not parsed")
		}
	})

	t.Run("empty string returns ErrEmptyAllowlist", func(t *testing.T) {
		_, err := parseAllowedChatIDs("")
		if !errors.Is(err, ErrEmptyAllowlist) {
			t.Errorf("err = %v, want ErrEmptyAllowlist", err)
		}
	})

	t.Run("whitespace-only returns ErrEmptyAllowlist", func(t *testing.T) {
		_, err := parseAllowedChatIDs("  , , ")
		if !errors.Is(err, ErrEmptyAllowlist) {
			t.Errorf("err = %v, want ErrEmptyAllowlist", err)
		}
	})

	t.Run("malformed token returns wrapped error naming the token", func(t *testing.T) {
		_, err := parseAllowedChatIDs("123,abc,456")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrEmptyAllowlist) {
			t.Errorf("got ErrEmptyAllowlist, want parse error")
		}
		if !contains(err.Error(), "abc") {
			t.Errorf("err %q does not name bad token", err)
		}
	})
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
