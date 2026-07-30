package youtube

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

// ProviderID is the registry key and ProviderID value for YouTube.
const ProviderID = domain.ProviderID("youtube")

// Config is the provider's dependency surface. Wired in main.go from the
// global Config (see infrastructure/config).
type Config struct {
	YtDlpPath   string // e.g. "yt-dlp"; required
	CookiesPath string // optional; passed as --cookies when non-empty
	// Logger is optional; nil falls back to slog.Default(). The provider logs
	// whether an extraction was captured and reused, which is the only way to
	// tell from production logs that the reuse path is actually engaging.
	Logger *slog.Logger
}

// Provider implements domain.Provider for YouTube audio downloads.
type Provider struct {
	cfg  Config
	info *infoCache
	log  *slog.Logger
}

func New(cfg Config) *Provider {
	if cfg.YtDlpPath == "" {
		cfg.YtDlpPath = "yt-dlp"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{cfg: cfg, info: newInfoCache(), log: logger}
}

// fetchInfoArgs builds FetchInfo's arguments. An empty infoOutTmpl means "do
// not capture the extraction".
//
// --no-simulate is load-bearing and easy to lose: --print implies --simulate,
// and simulate mode writes no files at all, so --write-info-json is accepted and
// silently ignored. That made the whole reuse path a no-op in production while
// every local run still looked fine, because a download works either way when
// the second extraction is not being refused.
func fetchInfoArgs(infoOutTmpl string) []string {
	args := []string{
		"--no-download",
		"--print", "%(title)s",
		"--print", "%(duration_string)s",
	}
	if infoOutTmpl != "" {
		args = append(args, "--no-simulate", "--write-info-json", "-o", infoOutTmpl)
	}
	return args
}

func (p *Provider) ID() domain.ProviderID { return ProviderID }

// Do NOT pin `--extractor-args youtube:player_client=...` here. Pinning
// android_vr,visionos,default looked right locally and broke production: from a
// datacenter IP `android_vr` answers HTTP 403 without cookies, `tv` returns only
// storyboards, and an unsupported client name (`visionos` in the pinned yt-dlp)
// is skipped with a warning, not an error — so the effective list silently
// shrinks. Probed on the VM: cookies plus yt-dlp's own client defaults is the
// combination that serves plain HTTPS DASH formats. Cookies are therefore not
// optional in prod, whatever the video's age rating.

// hostRe matches the host portion of a YouTube URL. Kept deliberately simple:
// we accept youtu.be, (www.|m.)?youtube.com, and youtube.com/shorts/...
var hostRe = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.|m\.)?(youtube\.com|youtu\.be)(?:/|$)`)

func (p *Provider) MatchesURL(rawURL string) bool {
	return hostRe.MatchString(strings.TrimSpace(rawURL))
}

// FetchInfo runs yt-dlp in no-download mode to get title and duration.
// Output: two lines on stdout, one per --print flag.
func (p *Provider) FetchInfo(ctx context.Context, url string) (domain.MediaInfo, error) {
	// Keep this extraction on disk for Download to replay — see infocache.go.
	// A failure here is not fatal: without the JSON, Download simply extracts
	// again, which is the old behaviour.
	infoDir, err := os.MkdirTemp("", "ytinfo-*")
	var infoOutTmpl string
	if err == nil {
		infoOutTmpl = filepath.Join(infoDir, "info")
	} else {
		infoDir = ""
	}

	args := fetchInfoArgs(infoOutTmpl)
	args = p.withCookies(args)
	args = append(args, url)

	cmd := exec.CommandContext(ctx, p.cfg.YtDlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if infoDir != "" {
			_ = os.RemoveAll(infoDir)
		}
		return domain.MediaInfo{}, fmt.Errorf("yt-dlp fetch info: %w: %s", err, lastLine(&stderr))
	}
	if infoDir != "" {
		infoPath := filepath.Join(infoDir, "info.info.json")
		if _, statErr := os.Stat(infoPath); statErr == nil {
			p.info.put(url, infoDir, infoPath)
			p.log.Info("ytdlp info captured", "url", url)
		} else {
			// Logged rather than ignored: a silent miss here is invisible until
			// downloads start failing in an environment where the second
			// extraction is refused.
			p.log.Warn("ytdlp info not written", "url", url, "err", statErr)
			_ = os.RemoveAll(infoDir)
		}
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	info := domain.MediaInfo{Provider: ProviderID}
	if len(lines) > 0 {
		info.Title = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		info.Duration = parseDurationString(strings.TrimSpace(lines[1]))
	}
	if info.Title == "" {
		return domain.MediaInfo{}, fmt.Errorf("yt-dlp fetch info: empty title")
	}
	return info, nil
}

// Download invokes yt-dlp with quality-specific args, streams stderr progress
// lines to opts.OnProgress on every match, and returns the resulting file path.
func (p *Provider) Download(ctx context.Context, opts domain.DownloadOptions) (string, error) {
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	stem := sanitizeFilename(opts.Filename)
	if stem == "" {
		stem = "audio"
	}
	outTmpl := filepath.Join(opts.OutputDir, stem+".%(ext)s")

	args := []string{
		"--no-playlist",
		"--newline",
		"-x", // extract audio
		"-o", outTmpl,
	}
	switch opts.Quality {
	case domain.QualityBest:
		// m4a (AAC), not opus: Telegram classifies .opus as a voice message
		// and rejects with VOICE_MESSAGES_FORBIDDEN when the recipient has
		// voice messages disabled.
		//
		// The format selector is what makes this extract-only. yt-dlp's
		// default `bestaudio` prefers opus (equal bitrate, higher score), so
		// `--audio-format m4a` alone always re-encodes opus → AAC — and with
		// `--audio-quality 0` that re-encode lands around 400 kbps, ~3x the
		// size of YouTube's own 129 kbps AAC track for no quality gain (a
		// re-encode cannot add any). Naming the m4a track instead makes
		// yt-dlp print "Not converting audio" and just remux, which also
		// skips a multi-minute ffmpeg pass on the 512 MB VM. The
		// `/bestaudio` fallback keeps the rare videos with no m4a track
		// working; those still transcode.
		args = append(args, "-f", "bestaudio[ext=m4a]/bestaudio", "--audio-format", "m4a")
	case domain.QualityGood:
		args = append(args, "--audio-format", "mp3", "--audio-quality", "128K")
	default:
		return "", fmt.Errorf("unsupported quality: %v", opts.Quality)
	}
	args = p.withCookies(args)

	// Replay FetchInfo's extraction when we still have it: that removes the
	// second Innertube API call per request, which is the one YouTube answers
	// with 403 from a datacenter IP. Falls back to extracting from the URL.
	if infoPath, infoDir := p.info.take(opts.URL); infoPath != "" {
		defer func() { _ = os.RemoveAll(infoDir) }()
		args = append(args, "--load-info-json", infoPath)
		p.log.Info("ytdlp info reuse", "url", opts.URL, "hit", true)
	} else {
		args = append(args, opts.URL)
		p.log.Info("ytdlp info reuse", "url", opts.URL, "hit", false)
	}

	cmd := exec.CommandContext(ctx, p.cfg.YtDlpPath, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	// yt-dlp prints progress to stdout with --newline; merge stderr so we
	// still catch errors, but read from stdout for progress.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start yt-dlp: %w", err)
	}

	// Tail stderr for diagnostics on failure.
	var stderrTail strings.Builder
	go copyToBuilder(stderr, &stderrTail)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var convertingEmitted bool
	for scanner.Scan() {
		line := scanner.Text()
		if prog, ok := ParseProgressLine(line); ok {
			if opts.OnProgress != nil {
				opts.OnProgress(prog)
			}
			continue
		}
		// After the download finishes, yt-dlp emits lines like
		// "[ExtractAudio] Destination: ..." / "[ffmpeg] ..." while it
		// transmuxes or transcodes. Surface that to the user once so the
		// "Downloading... 100%" message does not look stuck for long videos.
		if !convertingEmitted && isPostProcessingLine(line) {
			convertingEmitted = true
			if opts.OnProgress != nil {
				opts.OnProgress(domain.Progress{Converting: true})
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("%w: %s", domain.ErrDownloadFailed, lastLineStr(stderrTail.String()))
	}

	// yt-dlp's final extension depends on --audio-format, so the file has to be
	// discovered rather than computed.
	return findOutputFile(opts.OutputDir, stem, opts.Quality)
}

// findOutputFile locates yt-dlp's output inside the per-download directory.
//
// It matches by prefix instead of globbing: titles routinely contain glob
// metacharacters — "Artist - Track [Label]" is enough — and filepath.Glob reads
// `[...]` as a character class, so the pattern silently matches nothing while
// the file sits right there. That produced "output file not found" for a
// download that had completely succeeded.
//
// When no name matches the prefix (yt-dlp truncates filenames that would exceed
// the filesystem limit), every file in the directory is a candidate: the caller
// creates a fresh temp dir per download, so anything in it belongs to this one.
func findOutputFile(dir, stem string, q domain.Quality) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("%w: read output dir: %v", domain.ErrDownloadFailed, err)
	}

	var matches, all []string
	prefix := stem + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		all = append(all, full)
		if strings.HasPrefix(e.Name(), prefix) {
			matches = append(matches, full)
		}
	}
	if len(matches) == 0 {
		matches = all
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w: output file not found", domain.ErrDownloadFailed)
	}
	// Prefer the audio output over any intermediate files yt-dlp left behind.
	return preferAudioFile(matches, q), nil
}

// --- helpers ---------------------------------------------------------------

// isPostProcessingLine reports whether a yt-dlp stdout line signals that the
// download is done and ffmpeg-based post-processing has started.
func isPostProcessingLine(line string) bool {
	return strings.Contains(line, "[ExtractAudio]") ||
		strings.Contains(line, "[ffmpeg]") ||
		strings.Contains(line, "[Fixup")
}

func (p *Provider) withCookies(args []string) []string {
	if p.cfg.CookiesPath != "" {
		return append(args, "--cookies", p.cfg.CookiesPath)
	}
	return args
}

// sanitizeFilename strips path separators, control chars, and trims spaces/dots
// from both ends. Unicode letters are preserved.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0:
			b.WriteRune('_')
		case r < 0x20:
			// skip control chars
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), " .")
}

// parseDurationString accepts yt-dlp's duration_string formats: "SS", "MM:SS",
// or "HH:MM:SS". Returns zero on any parse error.
func parseDurationString(s string) time.Duration {
	if s == "" || s == "NA" {
		return 0
	}
	parts := strings.Split(s, ":")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		nums = append(nums, n)
	}
	switch len(nums) {
	case 1:
		return time.Duration(nums[0]) * time.Second
	case 2:
		return time.Duration(nums[0])*time.Minute + time.Duration(nums[1])*time.Second
	case 3:
		return time.Duration(nums[0])*time.Hour + time.Duration(nums[1])*time.Minute + time.Duration(nums[2])*time.Second
	default:
		return 0
	}
}

// preferAudioFile picks the file whose extension matches the requested format.
// Falls back to the first match if no preferred extension is found.
func preferAudioFile(matches []string, q domain.Quality) string {
	var want string
	switch q {
	case domain.QualityBest:
		want = ".m4a"
	case domain.QualityGood:
		want = ".mp3"
	}
	if want != "" {
		for _, m := range matches {
			if strings.EqualFold(filepath.Ext(m), want) {
				return m
			}
		}
	}
	return matches[0]
}

func copyToBuilder(r io.Reader, b *strings.Builder) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func lastLine(buf *bytes.Buffer) string {
	return lastLineStr(buf.String())
}

// lastLineStr returns the tail of yt-dlp's stderr. It keeps several lines, not
// just the final one: yt-dlp reports the actionable cause (skipped clients,
// SABR-only formats, missing PO token) as WARNING lines *above* the terminal
// ERROR, so keeping only the last line hides why extraction failed.
func lastLineStr(s string) string {
	lines := make([]string, 0, stderrTailLines)
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > stderrTailLines {
		lines = lines[len(lines)-stderrTailLines:]
	}
	return strings.Join(lines, " | ")
}

const stderrTailLines = 4
