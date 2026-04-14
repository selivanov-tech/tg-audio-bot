package config

import (
	"errors"
	"testing"
)

func TestEnvIntOrDefault(t *testing.T) {
	// os.Getenv returns "" for both truly-unset and empty-set vars, so the
	// two cases are indistinguishable here — one empty case is enough.
	const key = "MEDIABOT_TEST_INT"
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"empty returns default", "", 45},
		{"valid positive", "90", 90},
		{"invalid returns default", "abc", 45},
		{"zero returns default (footgun guard)", "0", 45},
		{"negative returns default", "-5", 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.env)
			got := envIntOrDefault(key, 45)
			if got != tc.want {
				t.Errorf("envIntOrDefault(%q=%q) = %d, want %d", key, tc.env, got, tc.want)
			}
		})
	}
}

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
