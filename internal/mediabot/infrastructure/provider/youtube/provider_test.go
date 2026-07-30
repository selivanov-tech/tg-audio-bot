package youtube

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

func TestMatchesURL(t *testing.T) {
	p := New(Config{})
	cases := []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/watch?v=abc", true},
		{"https://youtube.com/watch?v=abc", true},
		{"https://m.youtube.com/watch?v=abc", true},
		{"https://youtu.be/abc", true},
		{"https://www.youtube.com/shorts/abc", true},
		{"http://youtu.be/abc", true},
		{"youtube.com/watch?v=abc", true},
		{"https://example.com/watch?v=abc", false},
		{"https://vimeo.com/123", false},
		{"not a url", false},
		{"", false},
	}
	for _, c := range cases {
		if got := p.MatchesURL(c.url); got != c.want {
			t.Errorf("MatchesURL(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"  hello  ", "hello"},
		{"a/b\\c", "a_b_c"},
		{"a\x00b", "a_b"},
		{"a\tb\nc", "abc"},
		{"...hidden...", "hidden"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeFilename(c.in); got != c.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Regression: a title with brackets made filepath.Glob treat the stem as a
// character class, so a fully successful download reported
// "output file not found".
func TestFindOutputFile(t *testing.T) {
	const bracketed = "HUGEL, French Montana - Shine [Make The Girls Dance Records]"

	write := func(t *testing.T, dir string, names ...string) {
		t.Helper()
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("finds a stem containing glob metacharacters", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, bracketed+".m4a")

		got, err := findOutputFile(dir, bracketed, domain.QualityBest)
		if err != nil {
			t.Fatalf("findOutputFile: %v", err)
		}
		if filepath.Base(got) != bracketed+".m4a" {
			t.Errorf("got %q, want the bracketed .m4a", filepath.Base(got))
		}
	})

	t.Run("prefers the requested format over leftovers", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, bracketed+".webm", bracketed+".m4a")

		got, err := findOutputFile(dir, bracketed, domain.QualityBest)
		if err != nil {
			t.Fatalf("findOutputFile: %v", err)
		}
		if filepath.Ext(got) != ".m4a" {
			t.Errorf("got %q, want the .m4a", filepath.Base(got))
		}

		got, err = findOutputFile(dir, bracketed, domain.QualityGood)
		if err != nil {
			t.Fatalf("findOutputFile (good): %v", err)
		}
		// No .mp3 present, so any candidate is acceptable — it must not error.
		if got == "" {
			t.Error("empty result for Good quality")
		}
	})

	t.Run("falls back to any file when yt-dlp truncated the name", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "truncated-by-ytdlp.m4a")

		got, err := findOutputFile(dir, strings.Repeat("very long title ", 30), domain.QualityBest)
		if err != nil {
			t.Fatalf("findOutputFile: %v", err)
		}
		if filepath.Base(got) != "truncated-by-ytdlp.m4a" {
			t.Errorf("got %q, want the only file in the dir", filepath.Base(got))
		}
	})

	t.Run("errors on an empty directory", func(t *testing.T) {
		if _, err := findOutputFile(t.TempDir(), "anything", domain.QualityBest); !errors.Is(err, domain.ErrDownloadFailed) {
			t.Errorf("err = %v, want ErrDownloadFailed", err)
		}
	})
}

// Regression: --print implies --simulate, and simulate mode writes no files, so
// --write-info-json without --no-simulate is silently ignored. That made the
// info-reuse path a no-op that only production could reveal.
func TestFetchInfoArgs_CaptureRequiresNoSimulate(t *testing.T) {
	args := strings.Join(fetchInfoArgs("/tmp/ytinfo-x/info"), " ")
	for _, want := range []string{"--write-info-json", "--no-simulate", "--no-download"} {
		if !strings.Contains(args, want) {
			t.Errorf("capture args missing %s: %s", want, args)
		}
	}

	// Without a template there is nothing to write, so neither flag belongs.
	plain := strings.Join(fetchInfoArgs(""), " ")
	for _, unwanted := range []string{"--write-info-json", "--no-simulate"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("non-capture args should not contain %s: %s", unwanted, plain)
		}
	}
	if !strings.Contains(plain, "%(title)s") {
		t.Errorf("title print missing: %s", plain)
	}
}
