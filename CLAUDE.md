# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Phase 1 code is landed (plan.md Tasks 1–12) and the bot is deployed to Fly.io at `example.fly.dev`. `plan.md` remains the source of truth for architecture and acceptance criteria.

Module path: `github.com/rag-brothers/media-bot`. Go version: `1.26`.

## What this is

A Telegram bot that downloads media from providers (Phase 1: YouTube audio via `yt-dlp`). Phase 1 is audio-only, but the domain model already carries `MediaType` (Audio, Video) and a `Provider` interface so new sources (Instagram, etc.) and new media types plug in without restructuring.

## Commands

```
go build ./cmd/media-bot         # build
go test ./internal/mediabot/...  # run all tests
go test ./internal/mediabot/infrastructure/provider/youtube -run TestParseProgressLine  # single test
make run                         # go run ./cmd/media-bot (reads .env)
make deploy                      # flyctl deploy --remote-only -c .infra/prod/fly.toml
make logs                        # flyctl logs -a <fly-app>
make ssh                         # flyctl ssh console -a <fly-app>
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

## Deployment

Production runs on Fly.io. Layout mirrors `<redacted-local-path>`:

- `.infra/prod/Dockerfile` — two-stage build. `golang:1.26-bookworm` → `debian:bookworm-slim` with `ffmpeg`, `python3`, and a pinned `yt-dlp` binary (ARG `YTDLP_VERSION`). Bump the version arg when yt-dlp breaks on YouTube changes.
- `.infra/prod/fly.toml` — app `<fly-app>`, region `fra`, 512MB shared-cpu-1x. `auto_stop_machines = false` and `min_machines_running = 1` because the per-user queue is in-memory and cannot survive suspension. Health check hits `/health` every 30s.
- `.infra/prod/fly/entrypoint.sh` — runs every `/opt/fly/scripts/*.sh` hook then execs `media-bot`. Drop shell scripts into `.infra/prod/fly/scripts/` to extend (e.g. write cookies from a secret).
- `.github/workflows/deploy.yml` — manual `workflow_dispatch` only. Runs `go vet` + `golangci-lint` + `go test ./internal/mediabot/...` before `flyctl deploy`, then smoke-checks `/health`.
- `cmd/media-bot/main.go` serves `GET /health` on `$PORT` (default 8080) alongside the polling loop so Fly's HTTP check can pass.

Paths in `[build]` (`dockerfile`, `ignorefile`) are relative to `fly.toml`'s directory, NOT the repo root. Build context is the directory where `flyctl deploy` runs (repo root).

Fly config is always outside the default location, so every `flyctl` command for this app needs `-c .infra/prod/fly.toml` (except `-a <fly-app>` commands like `logs`/`ssh`).

### Open deployment items

1. **First-boot grace period is 15s.** The bot currently boots in ~4s so this is fine, but if startup grows (eager Telegram API call, DB, etc.) bump `grace_period` in `fly.toml`.
2. **Cookies plumbing is not wired.** `YTDLP_COOKIES_PATH` config exists in code but no secret is set on Fly. When an age-restricted video fails, add a Fly secret `YTDLP_COOKIES` and a `fly/scripts/01-cookies.sh` hook that writes it to a path referenced by `YTDLP_COOKIES_PATH`. A volume is needed if cookies should persist across restarts.
3. **No persistent volume.** In-flight downloads are lost on every redeploy. Acceptable per `plan.md`; users can re-send URLs.
4. **`GO_VERSION` build arg vs `go.mod`.** Both say `1.26`. If `go.mod` bumps, bump the arg in `.infra/prod/fly.toml` at the same time.
5. **CI token rotation.** `FLY_API_TOKEN` in GitHub secrets is scoped to the current Fly app. If the app is ever destroyed/recreated, re-issue with `flyctl tokens create deploy -c .infra/prod/fly.toml` and update the secret.

## Reference patterns from the sibling `rag` workspace

The plan points to these files for patterns to follow:

- `<redacted-local-path>/cmd/migrate/main.go` — entry point, config loading, signal context, graceful shutdown
- `<redacted-local-path>/internal/migration/domain/errors.go` — domain error style
- `<redacted-local-path>/internal/migration/application/port/repository.go` — port interface style

Match these patterns unless you have a concrete reason not to.
