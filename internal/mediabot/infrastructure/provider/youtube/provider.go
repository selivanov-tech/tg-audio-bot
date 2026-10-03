package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

// extraction is one yt-dlp metadata pass: what it printed, plus the info JSON it
// left on disk for Download to replay. An empty path means nothing was captured.
type extraction struct {
	stdout string
	dir    string
	path   string
}

// discardInfo drops the captured JSON but keeps the printed metadata. Downgrades
// the run to "title and duration only", which is the behaviour from before the
// info cache existed.
func (e *extraction) discardInfo() {
	if e.dir != "" {
		_ = os.RemoveAll(e.dir)
	}
	e.dir, e.path = "", ""
}

// FetchInfo runs yt-dlp in no-download mode to get title and duration.
// Output: two lines on stdout, one per --print flag.
func (p *Provider) FetchInfo(ctx context.Context, url string) (domain.MediaInfo, error) {
	ex, err := p.extract(ctx, url)
	if err != nil {
		return domain.MediaInfo{}, err
	}
	ex = p.reextractIfNoAudio(ctx, url, ex)

	lines := strings.Split(strings.TrimRight(ex.stdout, "\n"), "\n")
	info := domain.MediaInfo{Provider: ProviderID}
	if len(lines) > 0 {
		info.Title = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		info.Duration = parseDurationString(strings.TrimSpace(lines[1]))
	}
	if info.Title == "" {
		ex.discardInfo()
		return domain.MediaInfo{}, fmt.Errorf("yt-dlp fetch info: empty title")
	}

	// Handing the entry over last means a rejected run never leaves a directory
	// sitting in the cache until its TTL.
	if ex.path != "" {
		p.info.put(url, ex.dir, ex.path)
		p.log.Info("ytdlp info captured", "url", url)
	}
	return info, nil
}

// extract runs one yt-dlp metadata pass, keeping the extraction on disk for
// Download to replay — see infocache.go. Failing to capture it is not fatal:
// without the JSON, Download simply extracts again.
func (p *Provider) extract(ctx context.Context, url string) (extraction, error) {
	var ex extraction
	var outTmpl string
	if dir, err := os.MkdirTemp("", "ytinfo-*"); err == nil {
		ex.dir = dir
		outTmpl = filepath.Join(dir, "info")
	}

	args := p.withCookies(fetchInfoArgs(outTmpl))
	args = append(args, url)

	cmd := exec.CommandContext(ctx, p.cfg.YtDlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		ex.discardInfo()
		return extraction{}, fmt.Errorf("yt-dlp fetch info: %w: %s", err, lastLine(&stderr))
	}
	if warnings := extractionWarnings(stderr.String()); warnings != "" {
		p.log.Warn("ytdlp extraction warnings", "url", url, "detail", warnings)
	}

	ex.stdout = stdout.String()
	if ex.dir != "" {
		infoPath := filepath.Join(ex.dir, "info.info.json")
		if _, statErr := os.Stat(infoPath); statErr == nil {
			ex.path = infoPath
		} else {
			// Logged rather than ignored: a silent miss here is invisible until
			// downloads start failing in an environment where the second
			// extraction is refused.
			p.log.Warn("ytdlp info not written", "url", url, "err", statErr)
			ex.discardInfo()
		}
	}
	return ex, nil
}

// reextractIfNoAudio guards the cache against a degraded extraction.
//
// yt-dlp exits 0 and prints a correct title and duration even when the player
// API handed it nothing downloadable — the metadata comes from the watch page,
// the format list does not. Caching such a run is worse than not caching it at
// all: --load-info-json skips extraction entirely, so Download fails on format
// selection in about a second with no chance to recover.
//
// One retry is what fixes the first request after a zero-scale cold start. That
// request runs against a cookie jar the machine loaded from the mirror hours
// earlier; YouTube answers it with metadata only and rotates the session as a
// side effect, so the immediate second extraction comes back complete. When the
// retry is no better the JSON is dropped rather than cached, which leaves
// Download to extract for itself — the pre-cache behaviour, and still the better
// bet than replaying something known to be unusable.
func (p *Provider) reextractIfNoAudio(ctx context.Context, url string, ex extraction) extraction {
	if ex.path == "" || hasAudioFormat(ex.path) {
		return ex
	}
	p.log.Warn("ytdlp info has no audio format, re-extracting", "url", url)
	ex.discardInfo()

	retry, err := p.extract(ctx, url)
	if err != nil {
		// Keep the first run: its title and duration are still good.
		p.log.Warn("ytdlp re-extract failed", "url", url, "err", err)
		return ex
	}
	if retry.path != "" && !hasAudioFormat(retry.path) {
		p.log.Warn("ytdlp re-extract still has no audio format", "url", url)
		retry.discardInfo()
	}
	return retry
}

// hasAudioFormat reports whether a captured extraction holds at least one
// format carrying an audio track — audio-only for `bestaudio`, or muxed for the
// `/best...` fallbacks; -x turns either into an audio file.
//
// A refused extraction leaves `formats` either empty or holding only storyboard
// images, whose acodec is "none". Neither is distinguishable from a healthy
// extraction by exit code, and both make Download fail with "Requested format
// is not available".
func hasAudioFormat(infoPath string) bool {
	raw, err := os.ReadFile(infoPath)
	if err != nil {
		return false
	}
	var doc struct {
		Formats []struct {
			ACodec string `json:"acodec"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	for _, f := range doc.Formats {
		// Storyboards and video-only tracks write "none" explicitly; an
		// absent acodec is unknown, and the safe reading of unknown is "no
		// audio" — a false negative only costs one extra extraction.
		if f.ACodec != "" && f.ACodec != "none" {
			return true
		}
	}
	return false
}

// extractionWarnings pulls yt-dlp's WARNING and ERROR lines out of a run that
// still exited 0. They are the only record of *why* an extraction came back
// degraded — skipped player clients, a missing PO token, SABR-only formats, a
// 403 on the API page — and keeping stderr for the failure path alone is what
// made the last such incident undiagnosable from production logs.
func extractionWarnings(stderr string) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "WARNING:") || strings.HasPrefix(l, "ERROR:") {
			lines = append(lines, l)
		}
	}
	if len(lines) > stderrTailLines {
		lines = lines[len(lines)-stderrTailLines:]
	}
	return strings.Join(lines, " | ")
}

// bestAudioFormat is the Best tier's format selector: a ranked list, of which
// yt-dlp takes the first alternative that matches anything.
//
// The output is m4a (AAC), not opus: Telegram classifies .opus as a voice
// message and rejects it with VOICE_MESSAGES_FORBIDDEN when the recipient has
// voice messages disabled.
//
//  1. AAC-LC audio-only (140, and its lower-ranked -drc twin). It remuxes
//     into m4a without re-encoding — yt-dlp prints "Not converting audio" —
//     which skips a multi-minute ffmpeg pass on the 512 MB VM. It is matched
//     by codec, not by container: `[ext=m4a]` also matches 139, YouTube's
//     48 kbps HE-AAC track (mp4a.40.5). From a datacenter IP 140 is sometimes
//     missing (the SABR-only experiment makes yt-dlp skip the web clients'
//     https formats), and a container-based selector then shipped 139 without
//     a word: a 6-minute track arrived as 2 MiB of dull, narrow-sounding
//     audio instead of 5.6 MiB.
//  2. 234, the HLS audio-only AAC-LC stream (~128 kbps). yt-dlp reports its
//     codec and bitrate as unknown and its ext as mp4, so only its id can
//     select it; the prefix keeps language-suffixed variants.
//  3. Any audio-only format of at least 96 kbps — in practice opus 251, which
//     --audio-format m4a transcodes (see qualityArgs for the bitrate).
//  4. A muxed AAC-LC stream, smallest video first: 360p, then 480p. Some
//     datacenter sessions get no audio-only formats at all — the authed
//     clients come back SABR-only — and the menu shrinks to muxed video+audio:
//     HLS 91-96 and format 18. -x strips the video and the AAC track is copied.
//     The height cap bounds the throwaway video bytes. The codec filter keeps
//     out 91/92, whose audio is HE-AAC, and asr>=?44100 keeps out a format 18
//     that is labelled mp4a.40.2 but carries the 22 kHz HE-AAC core — seen in
//     production, byte for byte the size of 139. HLS formats report no asr,
//     hence the `?`; 93-96 carry the 140 track.
//  5. Only then the low-bitrate leftovers: 139, any audio-only format, any
//     muxed stream. A poor file still beats a failed download.
const bestAudioFormat = "bestaudio[acodec^=mp4a.40.2]" +
	"/bestaudio[format_id^=234]" +
	"/bestaudio[abr>=96]" +
	"/best[height<=360][acodec^=mp4a.40.2][asr>=?44100]" +
	"/best[height<=480][acodec^=mp4a.40.2][asr>=?44100]" +
	"/bestaudio[ext=m4a]/bestaudio/best[height<=480]/best"

// qualityArgs returns the format and post-processing args for a quality tier.
//
// Best passes --audio-quality 128K for the transcode path only: yt-dlp drops
// quality args when it merely copies an AAC track. Without it, yt-dlp's
// default VBR quality makes ffmpeg's AAC encoder land around 320 kbps — a
// 6-minute opus source came out at 14.5 MB instead of 6 — and a 45-minute
// track would no longer fit Telegram's 50 MB cap.
func qualityArgs(q domain.Quality) ([]string, error) {
	switch q {
	case domain.QualityBest:
		return []string{"-f", bestAudioFormat, "--audio-format", "m4a", "--audio-quality", "128K"}, nil
	case domain.QualityGood:
		return []string{"--audio-format", "mp3", "--audio-quality", "128K"}, nil
	default:
		return nil, fmt.Errorf("unsupported quality: %v", q)
	}
}

// formatLineRe matches the line yt-dlp prints once format selection is done,
// e.g. "[info] _9bw_VtMUGA: Downloading 1 format(s): 140". Merged selections
// print ids joined with "+".
var formatLineRe = regexp.MustCompile(`^\[info\] .+: Downloading \d+ format\(s\): (\S+)$`)

// parseFormatLine extracts the chosen format id(s). Logging them is what makes
// a quality drop visible in production: the id is the only place the choice
// shows before the file size does.
func parseFormatLine(line string) (string, bool) {
	m := formatLineRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", false
	}
	return m[1], true
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
	qArgs, err := qualityArgs(opts.Quality)
	if err != nil {
		return "", err
	}
	args = append(args, qArgs...)
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
		if format, ok := parseFormatLine(line); ok {
			p.log.Info("ytdlp format chosen", "url", opts.URL, "format", format)
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
