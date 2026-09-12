# tg-audio-bot

A Telegram bot that turns a YouTube link into an audio file in the chat. Written in Go with a strict hexagonal / DDD layout, deployed on Fly.io with zero-scale (the VM stops when idle and wakes on the next message).

Send a link → the bot fetches the title and duration → downloads the best AAC track with `yt-dlp` → shows live progress → sends the audio back. If the file is over Telegram's 50 MB cap, it offers a smaller MP3 retry.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Deploy](https://github.com/selivanov-tech/tg-audio-bot/actions/workflows/deploy.yml/badge.svg)](https://github.com/selivanov-tech/tg-audio-bot/actions/workflows/deploy.yml)

## Why it exists

A small, real system I use daily. It is also a compact showcase of how I like to build Go services: a domain with zero third-party imports, ports and adapters, a state machine for the unit of work, explicit resource limits, structured logs, tests, and CI in front of deploys.

## Features

- **YouTube audio** via `yt-dlp` (`youtube.com`, `youtu.be`, `/shorts/`). Playlists are ignored on purpose.
- **Best quality by default**: asks YouTube for its native AAC (`m4a`) track, so no transcoding and no size bloat. MP3 128k is offered only when the AAC file busts the 50 MB Telegram limit.
- **Custom file name**: `/download <url> <name>`.
- **Live progress** in one editable message, throttled to one edit per 2 s; the final 100% line is always delivered.
- **Per-user queue + global caps**: each chat has its own sequential worker; at most N downloads run at once across all users (default 3); at most 2 `yt-dlp` metadata probes run at once.
- **Duration gate** (default 45 min) rejects long videos before anything is downloaded.
- **Allow-list only**: chat IDs not in `MEDIABOT_ALLOWED_CHAT_IDS` get "access denied". Empty allow-list = the bot refuses to boot (deny-all by design).
- **Webhook intake with a secret token**, not long-polling. Forged POSTs to `/webhook` are dropped.
- **Zero-scale on Fly.io**: the machine stops on HTTP idle and the Fly proxy wakes it on the next Telegram update. A keep-alive self-ping holds it up while a download is running.
- Russian user-facing texts (single edit point in `application/messages.go`).

## How it works

```
Telegram ──POST /webhook──▶ telegram adapter ──▶ application.Handler
                                                      │
                                                      ▼
                                             application.Service
                                   detect provider ─ fetch info ─ duration gate
                                                      │
                                                      ▼
                                              per-user Queue  ──(global semaphore)──▶ Provider.Download (yt-dlp + ffmpeg)
                                                      │                                        │ OnProgress (every line)
                                                      ▼                                        ▼
                                             size check ≤ 50 MB ◀──────────────── throttled EditText (1 / 2 s)
                                                      │
                                     SendAudio ──────┴────── or offer MP3 retry
```

`Download` is the aggregate for one request and a state machine: `AwaitingQuality → Queued → Downloading → Sending → Complete | Failed`. Illegal transitions return `ErrInvalidState`; nothing mutates state fields directly.

## Architecture

Hexagonal (ports & adapters) with hard layer rules:

| Layer | Path | Rules |
|---|---|---|
| Domain | `internal/mediabot/domain/` | No imports from other internal packages, no third-party libs. Value objects, the `Download` state machine, the `Provider` interface + URL-based `ProviderRegistry`, domain errors. |
| Application | `internal/mediabot/application/` | Use cases: `Service` (download lifecycle), `Queue` (per-user workers + global cap), `KeepAlive`, `Handler` (commands, allow-list), `messages.go` (all user text). Depends on `domain` and `application/port` only. |
| Ports | `internal/mediabot/application/port/` | Interfaces the application needs from outside — currently `Messenger`. |
| Infrastructure | `internal/mediabot/infrastructure/` | Adapters: `provider/youtube/` (implements `domain.Provider` over `yt-dlp`), `telegram/` (implements `port.Messenger` over `go-telegram/bot`), `config/` (env). |
| Composition root | `cmd/media-bot/main.go` | The only place adapters are wired: registry → providers, messenger, service, handler, HTTP server (`/health`, `/webhook`). |

Adding a new source (Instagram, SoundCloud, …) means implementing `domain.Provider` and registering it in `main.go`. The application layer does not change. `MediaType` already carries `Audio | Video`, so video is a provider + messenger change, not a restructure.

## Design decisions worth reading

These are the parts that were easy to get wrong and are documented in the code:

- **Progress throttling lives in the service, not the provider.** The YouTube adapter reports every `yt-dlp` progress line; the service decides how often the user sees an edit. Providers stay dumb and testable.
- **Two separate semaphores.** The download queue caps active downloads, but a `yt-dlp` metadata probe is ~70 MB RSS on its own. Without a second cap, a user pasting ten links at once OOM-kills the 512 MB VM before any download starts.
- **AAC by selector, not by re-encode.** `-f bestaudio[ext=m4a]/bestaudio` makes `yt-dlp` remux YouTube's own AAC track. The naive `--audio-format m4a --audio-quality 0` re-encodes opus → AAC at ~400 kbps: three times the size for zero quality gain and a multi-minute ffmpeg pass on a shared CPU.
- **No `.opus`.** Telegram treats `.opus` uploads as voice messages, which some users block (`VOICE_MESSAGES_FORBIDDEN`).
- **Webhook, never polling.** A stopped Fly machine cannot poll; the proxy can only wake it on an inbound request. The webhook URL is re-registered on every boot (idempotent, self-healing).
- **Keep-alive goes through the public URL.** Pinging `localhost` would not reset Fly's idle timer; the request must cross the proxy.
- **Duration limit is application policy, not a domain invariant.** It is an ops knob from env, so it does not live in the domain.
- **Fail loud at boot.** Missing token, webhook secret, public URL, or an empty allow-list are boot errors, not degraded modes.
- **The repo never names the deployment.** `fly.toml` has no `app` key; the app name and public URL are secrets / env, so a public repo does not expose the running instance.

## Run locally

Prerequisites: Go 1.26, `yt-dlp` and `ffmpeg` on `PATH`, a bot token from @BotFather, and a public HTTPS tunnel (ngrok, cloudflared) because Telegram must reach your webhook.

```sh
cp .env.example .env     # fill TELEGRAM_BOT_TOKEN, TELEGRAM_WEBHOOK_SECRET, MEDIABOT_PUBLIC_URL, MEDIABOT_ALLOWED_CHAT_IDS
make run                 # go run ./cmd/media-bot, sources .env
```

Try the pipeline without Telegram at all — real `yt-dlp`, real routing, messages printed to the console:

```sh
make test-live URL="https://www.youtube.com/watch?v=..."
```

### Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `TELEGRAM_BOT_TOKEN` | yes | — | Bot token. |
| `TELEGRAM_WEBHOOK_SECRET` | yes | — | Echoed by Telegram in `X-Telegram-Bot-Api-Secret-Token`; mismatches are dropped. `openssl rand -hex 32`. |
| `MEDIABOT_PUBLIC_URL` | yes | — | Base URL for webhook registration and the keep-alive self-ping. |
| `MEDIABOT_ALLOWED_CHAT_IDS` | yes | — | Comma-separated chat IDs (groups are negative). Empty = boot fails. |
| `YTDLP_PATH` | no | `yt-dlp` | Binary path. |
| `YTDLP_COOKIES_PATH` | no | — | Netscape `cookies.txt` for restricted videos. In practice required from datacenter IPs. |
| `MEDIABOT_TEMP_DIR` | no | `$TMPDIR/mediabot` | Working directory for downloads. |
| `MEDIABOT_MAX_CONCURRENT_DOWNLOADS` | no | `3` | Global cap on active downloads. |
| `MEDIABOT_MAX_DURATION_MINUTES` | no | `45` | Reject longer videos before download. No unlimited mode. |
| `FLY_APP` | deploy only | — | Fly app name for the Makefile targets. |

## Tests and CI

```sh
make test      # go test ./internal/mediabot/...
make vet
```

Unit tests cover the domain state machine, the service (queueing, throttling, size fallback), the keep-alive loop, message rendering, config parsing and the `yt-dlp` progress parser. The Telegram adapter is a thin wrapper and is exercised by the live harness instead.

The deploy workflow (`.github/workflows/deploy.yml`, manual dispatch) runs `go vet`, `golangci-lint` and the tests before `flyctl deploy`, then smoke-checks `/health`.

## Deployment

Production is a two-stage Docker image (`golang:1.26-bookworm` → `debian:bookworm-slim` with `ffmpeg`, `python3`, a pinned `yt-dlp` and `deno` for YouTube signature decoding) on a 512 MB shared-CPU Fly machine in `fra`.

```sh
export FLY_APP=<your-app>          # or put it in .env
flyctl secrets set TELEGRAM_BOT_TOKEN=... TELEGRAM_WEBHOOK_SECRET="$(openssl rand -hex 32)" \
  MEDIABOT_PUBLIC_URL="https://${FLY_APP}.fly.dev" -c .infra/prod/fly.toml -a "$FLY_APP"
make sync-allowlist                # pushes allowed_chat_ids.txt as MEDIABOT_ALLOWED_CHAT_IDS
make deploy
```

Optional cookies for restricted videos: set the `YTDLP_COOKIES` secret to base64 of a `cookies.txt`; a boot hook materialises it to tmpfs. Runbooks for cookies, the allow-list, and webhook + zero-scale live in `CLAUDE.md`.

## Known limits

- First message after idle takes ~5–10 s while the VM cold-starts.
- No persistent volume: an in-flight download is lost on redeploy (auto-stop itself never cuts a download thanks to the keep-alive).
- Telegram's default Bot API caps uploads at 50 MB; a self-hosted Bot API server would lift it to 2 GB and is out of scope.

## Roadmap

- More providers behind the same `Provider` interface (Instagram, SoundCloud).
- Video (`MediaType` and `SendVideo` are already declared).
