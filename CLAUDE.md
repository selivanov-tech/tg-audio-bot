# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Phase 1 code is landed (plan.md Tasks 1–12) and the bot is deployed to Fly.io. `plan.md` remains the source of truth for architecture and acceptance criteria.

This repo is public, so it never names the deployment. The Fly app name and public URL live outside version control — `FLY_APP` in `.env` locally, repo secrets in CI, and the `MEDIABOT_PUBLIC_URL` Fly secret at runtime. Do not commit them back into `fly.toml`, the Makefile, or the workflow.

Module path: `github.com/selivanov-tech/tg-audio-bot`. Go version: `1.26`.

## What this is

A Telegram bot that downloads media from providers (Phase 1: YouTube audio via `yt-dlp`). Phase 1 is audio-only, but the domain model already carries `MediaType` (Audio, Video) and a `Provider` interface so new sources (Instagram, etc.) and new media types plug in without restructuring.

## Commands

```
go build ./cmd/media-bot         # build
go test ./internal/mediabot/...  # run all tests
go test ./internal/mediabot/infrastructure/provider/youtube -run TestParseProgressLine  # single test
make run                         # go run ./cmd/media-bot (reads .env)
make deploy                      # flyctl deploy --remote-only -c .infra/prod/fly.toml -a $FLY_APP
make logs                        # flyctl logs -a $FLY_APP
make ssh                         # flyctl ssh console -a $FLY_APP
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

3. **Queue is per-user with a global cap.** Each `chatID` gets its own sequential worker goroutine (lazy-spawned on enqueue, exits when the user's queue drains). A global semaphore (`MEDIABOT_MAX_CONCURRENT_DOWNLOADS`, default 3) gates *active* downloads across all users. So user A's third URL waits for A's second, AND at most 3 downloads run concurrently workspace-wide. A separate hard-coded semaphore (`maxConcurrentFetchInfo`, 2) gates `FetchInfo` calls — each yt-dlp probe is ~70 MB RSS and the download queue does not cover them, so without this gate a user flooding URLs OOM-kills the 512 MB Fly VM.

4. **`Download` is a state machine.** States: `AwaitingQuality → Queued → Downloading → Sending → Complete` (or `Failed`). Transitions go through `MarkQueued()`, `MarkDownloading()`, etc., which return `ErrInvalidState` on illegal moves. Don't mutate state fields directly.

5. **Telegram 50MB file limit is a hard constraint.** The service checks file size after download; when a Best (M4A/AAC) file exceeds the limit it shows the size error and then `offerLowerQuality` — the MP3 retry keyboard (see point 12). This is a default-Bot-API assumption; a local Bot API server would lift it to 2GB (out of scope). Note: we deliberately do NOT use opus — Telegram classifies `.opus` uploads as voice messages, which some users block via privacy settings (`VOICE_MESSAGES_FORBIDDEN`). Best therefore asks for YouTube's native AAC track by selector (`-f bestaudio[ext=m4a]/bestaudio`) and passes no `--audio-quality`: without the selector yt-dlp picks opus and re-encodes it to AAC at ~400 kbps, which triples the file (a 4:27 track went 4.3 MB → 12.7 MB) and moves the 50 MB cap from ~45 min of audio down to ~17. Don't "simplify" the selector away.

6. **Domain errors are exhaustive.** `ErrUnsupportedURL` covers both "we don't support this domain" and "no provider matched" — do not introduce a separate `ErrNoProviderFound`.

7. **All user-facing text lives in `application/messages.go`.** This is the single edit point for Russian strings — fixed constants (`MsgHelp`, `MsgFetching`, ...), formatters (`MsgQueued`, `MsgTooLarge`), renderers (`QualityLabel`, `RenderProgress`), and the error mapper (`MapUserError(stage, err)`). Domain types carry data only — `Quality.String()` returns the machine identifier for callback data, not a label; `Progress` is a pure struct with no render method. Never hardcode a user-facing string at a call site and never surface raw `err.Error()` to the user — route it through `MapUserError`.

8. **Logging is structured via `log/slog`.** A text handler is configured in `main.go` and injected into `Service` and `Handler` (both accept `*slog.Logger`; nil falls back to `slog.Default()`). Lifecycle pairs — `fetch info start`/`ok`/`failed`, `download start`/`ok`, `send start`/`ok` — let you trace one request end-to-end. When you add a new stage, emit both the start and the success log so failures are obvious by the missing `ok`.

9. **Duration limit is application-layer policy, not a domain invariant.** `MEDIABOT_MAX_DURATION_MINUTES` is enforced in `Service.HandleURL` right after `fetch info ok`, before a `Download` aggregate is created. It's a policy knob (ops decision, env-driven) — not a rule of the `Download` state machine — so it deliberately does not live in the domain. A reported duration of `0` (livestream / missing metadata) is allowed through; only a positive duration exceeding the limit is rejected.

10. **Webhook transport + Fly zero-scale.** The bot uses webhook intake, not long-polling: Fly stops the VM on HTTP idle and auto-starts it on the next Telegram POST. The Telegram adapter (`internal/mediabot/infrastructure/telegram/bot.go`) exposes `WebhookHandler()` (mounted at `/webhook` by `main.go`) and `Start(ctx)` which drains the internal updates channel via `go-telegram/bot`'s `StartWebhook`. On every boot `RegisterWebhook` re-registers the URL with Telegram (idempotent; keeps ops self-healing). Never switch back to `bot.Start` (polling) — a stopped VM can't poll, so the bot would never wake.

11. **Keep-alive self-ping.** `Queue.active` is an atomic counter bumped on `Enqueue` and decremented when `Process` returns. `application.KeepAlive` runs as a goroutine from `main.go`; every 2 min it reads `queue.Active()` and, if non-zero, GETs `${MEDIABOT_PUBLIC_URL}/health`. The ping must traverse the Fly proxy (public URL, not localhost) to reset the auto-stop idle timer. Without this, a multi-minute yt-dlp download with no concurrent webhooks would have the VM stopped from under it.

12. **Quality is never asked for — Best is the default.** `HandleURL` calls `SelectQuality(QualityBest)` itself and enqueues straight away; the "Fetching..." placeholder becomes that download's status message (`progressState.msgID`, with `progressState.header` keeping the title above every status line). The Good (MP3) tier is reachable only through `offerLowerQuality`, the one-button keyboard sent when a Best file busts the 50 MB cap — that retry is a *new* `Download` aggregate because the failed one is terminal. So the quality keyboard, the `pending` map and `HandleQualitySelection` are still live code, just off the happy path. If you ever make the default configurable, keep `SelectQuality` in the application layer — the domain must not know about defaults.

## Configuration

All config via env (see Task 5):

- `TELEGRAM_BOT_TOKEN` — required
- `TELEGRAM_WEBHOOK_SECRET` — **required**. Random string Telegram echoes back in the `X-Telegram-Bot-Api-Secret-Token` header on every webhook POST; the Telegram adapter silently drops mismatches. Generate once with `openssl rand -hex 32`. Empty/unset = boot fails with `ErrMissingWebhookSecret`.
- `MEDIABOT_PUBLIC_URL` — **required**. Externally reachable base URL, no trailing slash (prod: `https://<fly-app>.fly.dev`, set as a Fly secret — deliberately not in `fly.toml`). Used for two things: (1) webhook registration with Telegram at `${PublicURL}/webhook` on every boot; (2) keep-alive self-ping at `${PublicURL}/health` while any download is active, so the request traverses Fly's proxy and resets the auto-stop idle timer. Localhost would bypass the proxy and not count. Empty/unset = boot fails with `ErrMissingPublicURL`.
- `YTDLP_PATH` — default `yt-dlp`
- `YTDLP_COOKIES_PATH` — optional; when set, passed as `--cookies` to both FetchInfo and Download. **In prod this is effectively required, not optional**: from Fly's datacenter IP an unauthenticated YouTube session gets no usable formats at all (403 on the Innertube API → "Only images are available"), whatever the video's age rating. Set by `01-cookies.sh` at boot.
- `BUCKET_NAME`, `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` — injected by `fly storage create` (Tigris). Enable the cookie mirror; see "Cookie mirror" below. Empty `BUCKET_NAME` disables it and the bot falls back to the seed secret alone.
- `MEDIABOT_COOKIES_OBJECT` — object key for the mirrored cookie file, default `yt-cookies.txt`
- `MEDIABOT_TEMP_DIR` — default `os.TempDir()+"/mediabot"`
- `MEDIABOT_MAX_CONCURRENT_DOWNLOADS` — default 3
- `MEDIABOT_MAX_DURATION_MINUTES` — default 45. Videos longer than this are rejected right after `FetchInfo` with `MsgVideoTooLong`, before the download is queued. Rationale: 45 min @ mp3 128k ≈ 42 MB still fits Telegram's 50 MB cap; longer audio almost always fails the send stage anyway, and for opus-source videos yt-dlp must transcode to m4a/mp3, which is multi-minute on a 512 MB shared-cpu VM and looks frozen to the user. `0` / unset / invalid all fall back to the 45 default (no "unlimited" mode on purpose).
- `MEDIABOT_ALLOWED_CHAT_IDS` — **required**, comma-separated chat IDs. Empty/unset = boot fails with `ErrEmptyAllowlist` (deny-all by design). Group chat IDs are negative. See "Allowlist runbook" below.

## Third-party libs

- Telegram: `github.com/go-telegram/bot` (actively maintained). Do **not** use `go-telegram-bot-api/telegram-bot-api/v5` — stale since 2021.
- `yt-dlp` is invoked via `os/exec`, not as a library.

## Deployment

Production runs on Fly.io:

- `.infra/prod/Dockerfile` — two-stage build. `golang:1.26-bookworm` → `debian:bookworm-slim` with `ffmpeg`, `python3`, and a pinned `yt-dlp` binary (ARG `YTDLP_VERSION`). Bump the version arg when yt-dlp breaks on YouTube changes.
- `.infra/prod/fly.toml` — region `fra`, 512MB shared-cpu-1x. Carries **no `app` key** on purpose (see "Project status"); flyctl takes the name from `-a` / `$FLY_APP` and prefers it over anything in the file. **Zero-scale**: `auto_stop_machines = 'stop'`, `auto_start_machines = true`, `min_machines_running = 0`. The Fly proxy buffers the first Telegram webhook POST after idle, cold-starts the VM (~5–10s), and forwards the request. Between active sessions the machine is stopped and billing pauses.
- `.infra/prod/fly/entrypoint.sh` — **sources** every `/opt/fly/scripts/*.sh` hook (so hooks can export env vars that reach `media-bot`), then execs the binary. Drop shell scripts into `.infra/prod/fly/scripts/` to extend; see the cookies runbook below for an example.
- `.github/workflows/deploy.yml` — manual `workflow_dispatch` only. Runs `go vet` + `golangci-lint` + `go test ./internal/mediabot/...` before `flyctl deploy`, then smoke-checks `/health`.
- `cmd/media-bot/main.go` serves two routes on `$PORT` (default 8080): `GET /health` (Fly health check + self-ping target) and `POST /webhook` (Telegram update intake, delegated to `go-telegram/bot`'s `WebhookHandler` which validates the secret token header).

Paths in `[build]` (`dockerfile`, `ignorefile`) are relative to `fly.toml`'s directory, NOT the repo root. Build context is the directory where `flyctl deploy` runs (repo root).

Fly config is always outside the default location, so every `flyctl` command needs `-c .infra/prod/fly.toml`. And because `fly.toml` has no `app` key, every command also needs `-a <app>` — flyctl cannot infer the name. Either pass it explicitly or `export FLY_APP=<app>` for the shell session. The Makefile targets do this for you and fail fast when `FLY_APP` is unset.

### Cookie mirror (Tigris object storage)

**The problem it solves.** YouTube rotates account cookies, and yt-dlp writes the refreshed jar back to the file it was given (verified: a 2-line file came back with 12 lines and yt-dlp's own `# This file is generated by yt-dlp` header). The file lives on tmpfs and `01-cookies.sh` re-creates it from the `YTDLP_COOKIES` secret at every boot — and with zero-scale, boots are frequent. So every refresh was discarded and the seed snapshot aged until YouTube answered `The provided YouTube account cookies are no longer valid`, which surfaces to users only as `Requested format is not available`.

`internal/mediabot/infrastructure/cookiestore` closes the loop: load the jar from the bucket at boot, upload it whenever the local file changes, and once more on SIGINT (which Fly sends before stopping the machine). The secret becomes a one-time seed.

Provision once — Fly stages all five `AWS_*`/`BUCKET_NAME` secrets itself:
```sh
flyctl storage create -a "$FLY_APP"     # private Tigris bucket, credentials scoped to it
make deploy
```

Boot logs tell you which path was taken: `cookie mirror loaded` (bucket copy in use), `cookie mirror empty, seeding from secret` (first boot with a fresh bucket), `cookie mirror disabled` (no `BUCKET_NAME`), or a `cookie mirror … failed` error. Every failure is non-fatal by design — the bot keeps running on the seeded file, it just stops persisting rotations.

Notes and limits:
- The bucket must stay **private** — these are live account session cookies.
- Uploads are mtime+size triggered, polled once a minute, and never re-upload a jar that was just downloaded.
- Single machine today, so no write races. If concurrency ever goes above one machine, two of them will clobber each other's jar.
- Path-style addressing is used (`endpoint/bucket/key`); Tigris accepts both styles.

### Cookies runbook (yt-dlp)

Cookies are needed for age- or region-restricted YouTube videos — and in prod for *every* video (see `YTDLP_COOKIES_PATH` above). Plumbing:

- `.infra/prod/fly/scripts/01-cookies.sh` — sourced by `entrypoint.sh` at boot. With `YTDLP_COOKIES` set it base64-decodes the value to `/tmp/yt-cookies.txt` (mode 600) and exports `YTDLP_COOKIES_PATH`. With only `BUCKET_NAME` set it writes a header-only jar to that same path instead — yt-dlp aborts when `--cookies` points at a missing file, and the mirror replaces the placeholder moments later. It is a no-op only when both are unset.
- Path is tmpfs. The durable copy lives in the Tigris bucket (see "Cookie mirror"), so no volume is needed.

Enable:
```sh
# Export cookies.txt from a browser (Netscape format), then:
flyctl secrets set YTDLP_COOKIES="$(base64 -i cookies.txt | tr -d '\n')" -c .infra/prod/fly.toml -a "$FLY_APP"
# Linux alternative: base64 -w0 cookies.txt
```
Setting the secret triggers an auto-redeploy.

Verify on the VM:
```sh
make ssh
  env | grep YTDLP_COOKIES_PATH     # → /tmp/yt-cookies.txt
  head -n 3 /tmp/yt-cookies.txt     # → Netscape header line
```

Disable / rotate:
```sh
flyctl secrets unset YTDLP_COOKIES -c .infra/prod/fly.toml -a "$FLY_APP"
# to rotate, just re-run `secrets set` with a fresh cookies.txt
```

`cookies.txt` is gitignored — safe to keep in the repo root while setting the secret.

### Allowlist runbook

The bot rejects any chat ID not in `MEDIABOT_ALLOWED_CHAT_IDS` with `MsgAccessDenied` ("Доступ к боту ограничен."). Empty/unset = boot fails — there is no allow-all mode.

Source of truth is `allowed_chat_ids.txt` in the repo root (gitignored). Format: one chat ID per line, blanks and `# comments` ignored, group chats are negative IDs:

```
# Owner
12345678
# Some group
-1234567890
```

Sync to Fly:
```sh
make sync-allowlist     # parses the txt, sets MEDIABOT_ALLOWED_CHAT_IDS as a Fly secret
```
The secret update auto-redeploys the app. Adding/removing a user means: edit the txt, run `make sync-allowlist`, wait ~1 min for the new machine.

Local dev: put the same comma-separated list in `.env` as `MEDIABOT_ALLOWED_CHAT_IDS=12345678,87654321`. `make run` sources `.env`. Without it the bot won't start.

To get your own chat ID: message the bot once and grep `make logs` for `msg received chat=...` (or use `@userinfobot` in Telegram).

### Webhook + zero-scale runbook

First-time setup for a new environment:

```sh
# 0. The app name is not in the repo — put FLY_APP=<app> in .env, and export
#    it for the raw flyctl calls below (the make targets read .env themselves):
export FLY_APP=<app>

# 1. Generate and register the webhook secret (only if not already set):
flyctl secrets set TELEGRAM_WEBHOOK_SECRET="$(openssl rand -hex 32)" -c .infra/prod/fly.toml -a "$FLY_APP"

# 2. Set the public base URL. It is a secret, not a fly.toml [env] value, so
#    the repo does not name the deployment. Boot fails without it.
flyctl secrets set MEDIABOT_PUBLIC_URL="https://${FLY_APP}.fly.dev" -c .infra/prod/fly.toml -a "$FLY_APP"

# 3. Deploy — the bot calls Telegram's setWebhook at boot using MEDIABOT_PUBLIC_URL:
make deploy

# 4. Verify Telegram accepted the registration:
TOKEN=$(flyctl secrets list -c .infra/prod/fly.toml -a "$FLY_APP" | awk '/TELEGRAM_BOT_TOKEN/ {print $2}')  # or use the value you set
curl -s "https://api.telegram.org/bot${TOKEN}/getWebhookInfo" | jq .
# → should show url = https://<app>.fly.dev/webhook, has_custom_certificate=false, pending_update_count≈0
```

CI needs the same two values as repo secrets: `FLY_APP` (passed to `flyctl deploy -a`) and `MEDIABOT_PUBLIC_URL` (used by the post-deploy health check), alongside `FLY_API_TOKEN`.

Sanity checks after change to zero-scale:
- `flyctl status -c .infra/prod/fly.toml -a "$FLY_APP"` shows `min_machines_running = 0` and the machine may be `stopped` between sessions.
- Send a message → watch `make logs` for `webhook registered` then `msg received` within ~10s (cold start) or instantly (warm).
- Kick off a >5-min download → logs show periodic inbound GETs to `/health` from the bot itself (keep-alive). Machine stays `started` until ~2-5 min after the last active work.

Rollback to always-on (if auto-stop ever causes grief): set `min_machines_running = 1` and `auto_stop_machines = false` in `fly.toml`, redeploy. No code changes required.

### Open deployment items

1. **Cold-start latency.** First Telegram message after idle takes ~5–10s while Fly boots the VM. Acceptable for this bot; if it grows, profile boot or bump the VM to a paid CPU tier.
2. **No persistent volume.** In-flight downloads are lost on every redeploy. Auto-stop does NOT cut mid-download thanks to the keep-alive self-ping; users can re-send URLs after any unexpected restart.
3. **Stale MP3-retry callbacks.** If the VM auto-stops while a user is staring at the MP3 retry keyboard and they tap the button after the machine starts, the in-memory `pending` map is empty and the tap is silently dropped (logged as `callback stale`). Acceptable; user re-sends URL. Rare now that the keyboard only appears for oversize files.
4. **`GO_VERSION` build arg vs `go.mod`.** Both say `1.26`. If `go.mod` bumps, bump the arg in `.infra/prod/fly.toml` at the same time.
5. **CI token rotation.** `FLY_API_TOKEN` in GitHub secrets is scoped to the current Fly app. If the app is ever destroyed/recreated, re-issue with `flyctl tokens create deploy -c .infra/prod/fly.toml -a "$FLY_APP"`, and update the `FLY_APP` / `MEDIABOT_PUBLIC_URL` repo secrets to match the new name.
6. **App name is guessable from the repo name.** Keeping the name out of the repo only helps while the Fly app is not simply `tg-audio-bot`. If it still matches the repo slug, anyone can derive the `.fly.dev` hostname and wake the zero-scale VM by hitting `/health`. Rename the app to something unrelated if that matters.
