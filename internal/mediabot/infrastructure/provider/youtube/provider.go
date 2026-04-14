package youtube

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// ProviderID is the registry key and ProviderID value for YouTube.
const ProviderID = domain.ProviderID("youtube")

// Config is the provider's dependency surface. Wired in main.go from the
// global Config (see infrastructure/config).
type Config struct {
	YtDlpPath    string // e.g. "yt-dlp"; required
	CookiesPath  string // optional; passed as --cookies when non-empty
}

// Provider implements domain.Provider for YouTube audio downloads.
type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	if cfg.YtDlpPath == "" {
		cfg.YtDlpPath = "yt-dlp"
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) ID() domain.ProviderID { return ProviderID }

// hostRe matches the host portion of a YouTube URL. Kept deliberately simple:
// we accept youtu.be, (www.|m.)?youtube.com, and youtube.com/shorts/...
var hostRe = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.|m\.)?(youtube\.com|youtu\.be)(?:/|$)`)

func (p *Provider) MatchesURL(rawURL string) bool {
	return hostRe.MatchString(strings.TrimSpace(rawURL))
}

// FetchInfo runs yt-dlp in no-download mode to get title and duration.
// Output: two lines on stdout, one per --print flag.
func (p *Provider) FetchInfo(ctx context.Context, url string) (domain.MediaInfo, error) {
	args := []string{
		"--no-warnings",
		"--no-download",
		"--print", "%(title)s",
		"--print", "%(duration_string)s",
	}
	args = p.withCookies(args)
	args = append(args, url)

	cmd := exec.CommandContext(ctx, p.cfg.YtDlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return domain.MediaInfo{}, fmt.Errorf("yt-dlp fetch info: %w: %s", err, lastLine(&stderr))
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
		"--no-warnings",
		"--no-playlist",
		"--newline",
		"-x", // extract audio
		"-o", outTmpl,
	}
	switch opts.Quality {
	case domain.QualityBest:
		// m4a (AAC), not opus: Telegram classifies .opus as a voice message
		// and rejects with VOICE_MESSAGES_FORBIDDEN when the recipient has
		// voice messages disabled. AAC is served natively by YouTube so this
		// is usually extract-only, no transcode.
		args = append(args, "--audio-format", "m4a", "--audio-quality", "0")
	case domain.QualityGood:
		args = append(args, "--audio-format", "mp3", "--audio-quality", "128K")
	default:
		return "", fmt.Errorf("unsupported quality: %v", opts.Quality)
	}
	args = p.withCookies(args)
	args = append(args, opts.URL)

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

	// yt-dlp's final extension depends on --audio-format; glob to find it.
	matches, err := filepath.Glob(filepath.Join(opts.OutputDir, stem+".*"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("%w: output file not found", domain.ErrDownloadFailed)
	}
	// Prefer the audio output (opus/mp3) over any intermediate files yt-dlp
	// may have left behind.
	preferred := preferAudioFile(matches, opts.Quality)
	return preferred, nil
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

func lastLineStr(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
