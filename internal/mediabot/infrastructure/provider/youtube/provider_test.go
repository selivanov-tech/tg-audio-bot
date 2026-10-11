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

// Regression: a degraded extraction exits 0 with a correct title but no formats,
// and caching it made Download fail on format selection with no way to recover.
func TestHasAudioFormat(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "info.info.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "audio-only m4a track present",
			body: `{"formats":[{"format_id":"140","acodec":"mp4a.40.2","vcodec":"none"},
			              {"format_id":"137","acodec":"none","vcodec":"avc1"}]}`,
			want: true,
		},
		{
			// What a healthy extraction looks like when YouTube offers no m4a:
			// the /bestaudio fallback still matches, so this must pass.
			name: "audio-only opus track present",
			body: `{"formats":[{"format_id":"251","acodec":"opus","vcodec":"none"}]}`,
			want: true,
		},
		{
			// The observed cold-start failure: storyboards carry neither codec.
			name: "storyboards only",
			body: `{"formats":[{"format_id":"sb0","acodec":"none","vcodec":"none"},
			              {"format_id":"sb1","acodec":"none","vcodec":"none"}]}`,
			want: false,
		},
		{
			name: "empty format list",
			body: `{"formats":[]}`,
			want: false,
		},
		{
			name: "no formats key at all",
			body: `{"title":"something"}`,
			want: false,
		},
		{
			// The tv-downgraded client's whole menu from a datacenter IP:
			// muxed streams only. The /best fallbacks extract audio from
			// these, so they must pass the gate.
			name: "muxed formats only",
			body: `{"formats":[{"format_id":"18","acodec":"mp4a.40.2","vcodec":"avc1"}]}`,
			want: true,
		},
		{
			name: "video-only formats only",
			body: `{"formats":[{"format_id":"137","acodec":"none","vcodec":"avc1"}]}`,
			want: false,
		},
		{
			name: "malformed json",
			body: `{"formats":`,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasAudioFormat(write(t, c.body)); got != c.want {
				t.Errorf("hasAudioFormat = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		if hasAudioFormat(filepath.Join(t.TempDir(), "absent.json")) {
			t.Error("want false for a file that does not exist")
		}
	})
}

// Regression: these lines only ever appear on a run that exits 0, so keeping
// stderr for the failure path alone threw away the explanation of a degraded
// extraction at exactly the moment it mattered.
func TestExtractionWarnings(t *testing.T) {
	t.Run("keeps warning and error lines, drops the rest", func(t *testing.T) {
		got := extractionWarnings(strings.Join([]string{
			"[youtube] Extracting URL: https://youtu.be/abc",
			"WARNING: [youtube] abc: Some web client https formats have been skipped",
			"[info] abc: Downloading 1 format(s): 140",
			"ERROR: unable to download API page: HTTP Error 403: Forbidden",
		}, "\n"))

		for _, want := range []string{"Some web client https formats", "HTTP Error 403"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		if strings.Contains(got, "Extracting URL") {
			t.Errorf("informational line kept: %q", got)
		}
	})

	t.Run("empty when nothing was flagged", func(t *testing.T) {
		if got := extractionWarnings("[youtube] Extracting URL: x\n[info] done\n"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("caps the tail", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < stderrTailLines+3; i++ {
			b.WriteString("WARNING: line\n")
		}
		if got := strings.Count(extractionWarnings(b.String()), "WARNING:"); got != stderrTailLines {
			t.Errorf("kept %d lines, want %d", got, stderrTailLines)
		}
	})
}

// Regression: with 140 missing from a datacenter extraction, a container-based
// selector (`bestaudio[ext=m4a]`) shipped 139, the 48 kbps HE-AAC track. Every
// alternative that reaches 139 must come after every one that reaches a
// ~128 kbps track.
func TestBestAudioFormat_LowBitrateFallbacksComeLast(t *testing.T) {
	alts := strings.Split(bestAudioFormat, "/")
	index := func(alt string) int {
		t.Helper()
		for i, a := range alts {
			if a == alt {
				return i
			}
		}
		t.Fatalf("alternative %q missing from %q", alt, bestAudioFormat)
		return -1
	}

	if alts[0] != "bestaudio[acodec^=mp4a.40.2]" {
		t.Errorf("first alternative = %q, want the AAC-LC audio-only track", alts[0])
	}
	good := []string{
		"bestaudio[acodec^=mp4a.40.2]",
		"bestaudio[format_id^=234]",
		"bestaudio[abr>=96]",
		"best[height<=360][acodec^=mp4a.40.2][asr>=?44100]",
		"best[height<=480][acodec^=mp4a.40.2][asr>=?44100]",
	}
	poor := []string{"bestaudio[ext=m4a]", "bestaudio", "best[height<=480]", "best"}
	for _, g := range good {
		for _, p := range poor {
			if index(g) > index(p) {
				t.Errorf("%q comes after the low-bitrate fallback %q", g, p)
			}
		}
	}
}

func TestQualityArgs(t *testing.T) {
	t.Run("best caps the transcode bitrate", func(t *testing.T) {
		// Without the cap an opus source re-encodes at ~320 kbps and long
		// tracks bust Telegram's 50 MB limit.
		args, err := qualityArgs(domain.QualityBest)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(args, " ")
		for _, want := range []string{"-f " + bestAudioFormat, "--audio-format m4a", "--audio-quality 128K"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})

	t.Run("good is mp3 128k", func(t *testing.T) {
		args, err := qualityArgs(domain.QualityGood)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(args, " "); got != "--audio-format mp3 --audio-quality 128K" {
			t.Errorf("args = %q", got)
		}
	})

	t.Run("unknown quality is an error", func(t *testing.T) {
		if _, err := qualityArgs(domain.Quality(99)); err == nil {
			t.Error("want an error")
		}
	})
}

func TestParseFormatLine(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		want   string
		wantOK bool
	}{
		{"single format", "[info] _9bw_VtMUGA: Downloading 1 format(s): 140", "140", true},
		{"low-bitrate fallback", "[info] _9bw_VtMUGA: Downloading 1 format(s): 139", "139", true},
		{"merged formats", "[info] abc: Downloading 1 format(s): 137+140", "137+140", true},
		{"trailing CR", "[info] abc: Downloading 1 format(s): 251\r", "251", true},
		{"progress line", "[download]  42.0% of    5.62MiB at  1.00MiB/s ETA 00:03", "", false},
		{"other info line", "[info] Writing video metadata as JSON to: /tmp/x/info.info.json", "", false},
		{"extract audio line", "[ExtractAudio] Destination: /tmp/x/track.m4a", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseFormatLine(c.line)
			if got != c.want || ok != c.wantOK {
				t.Errorf("parseFormatLine(%q) = (%q, %v), want (%q, %v)", c.line, got, ok, c.want, c.wantOK)
			}
		})
	}
}
