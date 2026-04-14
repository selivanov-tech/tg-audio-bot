# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

The repository currently contains only `plan.md`. The Go code has not been written yet. `plan.md` is the source of truth for architecture, task breakdown, and acceptance criteria — read it before making decisions.

Module path (once `go mod init` runs): `github.com/rag-brothers/media-bot`. Go version: `1.26`.

## What this is

A Telegram bot that downloads media from providers (Phase 1: YouTube audio via `yt-dlp`). Phase 1 is audio-only, but the domain model already carries `MediaType` (Audio, Video) and a `Provider` interface so new sources (Instagram, etc.) and new media types plug in without restructuring.

## Commands

Once the project is initialized per Task 12, the expected commands are:

```
go build ./cmd/media-bot         # build
go test ./internal/mediabot/...  # run all tests
go test ./internal/mediabot/infrastructure/provider/youtube -run TestParseProgressLine  # single test
make run                         # go run ./cmd/media-bot (reads .env)
```

Runtime prerequisite: `yt-dlp` must be on `PATH` (or set `YTDLP_PATH`).

## Architecture

Full DDD / hexagonal (ports & adapters). The layer rules are strict:

- `internal/mediabot/domain/` — no imports from other internal packages, no third-party libs. Enums, value objects, the `Download` aggregate (state machine), the `Provider` interface, and domain errors live here.
- `internal/mediabot/application/` — orchestrates use cases. Depends on `domain` and on `application/port` interfaces only. Never imports adapters directly.
- `internal/mediabot/application/port/` — interfaces the application needs from the outside world (currently `Messenger`).
- `internal/mediabot/infrastructure/` — concrete adapters: `provider/youtube/` implements `domain.Provider`, `telegram/` implements `port.Messenger`, `config/` loads env.
- `cmd/media-bot/main.go` — the only place adapters get wired into the application. Creates the provider registry, registers each provider, constructs the messenger, injects both into the service.

### Key cross-cutting design points

These are easy to get wrong without reading multiple files together:

1. **Provider discovery is URL-based.** `ProviderRegistry.Detect(url)` iterates providers calling `MatchesURL`. Adding a provider means implementing `domain.Provider` and registering it in `main.go` — no changes to the application layer.

2. **Progress throttling lives in the service, not the provider.** The YouTube provider calls the `OnProgress` callback on *every* matched stderr line. The application service throttles edits to the Telegram message to at most one per 2 seconds, and always forwards the final 100% line. If you add throttling inside a provider, you break this contract.

3. **Queue is per-user with a global cap.** Each `chatID` gets its own sequential worker goroutine (lazy-spawned on enqueue, exits when the user's queue drains). A global semaphore (`MEDIABOT_MAX_CONCURRENT_DOWNLOADS`, default 3) gates *active* downloads across all users. So user A's third URL waits for A's second, AND at most 3 downloads run concurrently workspace-wide.

4. **`Download` is a state machine.** States: `AwaitingQuality → Queued → Downloading → Sending → Complete` (or `Failed`). Transitions go through `MarkQueued()`, `MarkDownloading()`, etc., which return `ErrInvalidState` on illegal moves. Don't mutate state fields directly.

5. **Telegram 50MB file limit is a hard constraint.** The service checks file size after download and surfaces a user-facing error suggesting Good (MP3) quality when Best (opus) exceeds the limit. This is a default-Bot-API assumption; a local Bot API server would lift it to 2GB (out of scope).

6. **Domain errors are exhaustive.** `ErrUnsupportedURL` covers both "we don't support this domain" and "no provider matched" — do not introduce a separate `ErrNoProviderFound`.

## Configuration

All config via env (see Task 5):

- `TELEGRAM_BOT_TOKEN` — required
- `YTDLP_PATH` — default `yt-dlp`
- `YTDLP_COOKIES_PATH` — optional; when set, passed as `--cookies` to both FetchInfo and Download (needed for some age-/region-restricted YouTube videos)
- `MEDIABOT_TEMP_DIR` — default `os.TempDir()+"/mediabot"`
- `MEDIABOT_MAX_CONCURRENT_DOWNLOADS` — default 3

## Third-party libs

- Telegram: `github.com/go-telegram/bot` (actively maintained). Do **not** use `go-telegram-bot-api/telegram-bot-api/v5` — stale since 2021.
- `yt-dlp` is invoked via `os/exec`, not as a library.

## Reference patterns from the sibling `rag` workspace

The plan points to these files for patterns to follow:

- `<redacted-local-path>/cmd/migrate/main.go` — entry point, config loading, signal context, graceful shutdown
- `<redacted-local-path>/internal/migration/domain/errors.go` — domain error style
- `<redacted-local-path>/internal/migration/application/port/repository.go` — port interface style

Match these patterns unless you have a concrete reason not to.
