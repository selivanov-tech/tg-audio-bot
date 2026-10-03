# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26
# A nightly, not a stable release: from the datacenter IP YouTube serves the
# authed web_embedded client SABR-only, leaving format 18 alone (yt-dlp issue
# 17666, whose 48 kbps HE-AAC track the bot then shipped). The nightly sends
# that client a Safari UA, which brings back HLS formats 91-96 — 93-96 carry
# the 140 AAC-LC track. Switch YTDLP_REPO back to yt-dlp/yt-dlp once a stable
# release after 2026.08.19 includes commit c7fb478.
ARG YTDLP_REPO=yt-dlp/yt-dlp-nightly-builds
ARG YTDLP_VERSION=2026.09.27.232945
ARG DENO_VERSION=v2.7.12

# --- Stage 1: build the Go binary ---------------------------------------------
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /src

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
        -ldflags="-s -w" \
        -o /out/media-bot \
        ./cmd/media-bot

# --- Stage 2: runtime ---------------------------------------------------------
FROM debian:bookworm-slim

ARG YTDLP_REPO
ARG YTDLP_VERSION
ARG DENO_VERSION

ENV DEBIAN_FRONTEND=noninteractive \
    MEDIABOT_TEMP_DIR=/tmp/mediabot \
    YTDLP_PATH=/usr/local/bin/yt-dlp \
    PORT=8080

# yt-dlp needs python3 at runtime; ffmpeg is used for audio extraction / merging.
# deno is yt-dlp's default JS runtime — required to decode YouTube nsig/signatures
# (without it most formats are filtered and selection fails with "format not available").
# ca-certificates is required for HTTPS calls to Telegram and YouTube.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        ffmpeg \
        python3 \
        curl \
        unzip \
    && curl -fsSL -o /usr/local/bin/yt-dlp \
        "https://github.com/${YTDLP_REPO}/releases/download/${YTDLP_VERSION}/yt-dlp" \
    && chmod +x /usr/local/bin/yt-dlp \
    && curl -fsSL -o /tmp/deno.zip \
        "https://github.com/denoland/deno/releases/download/${DENO_VERSION}/deno-x86_64-unknown-linux-gnu.zip" \
    && unzip -q /tmp/deno.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/deno \
    && rm /tmp/deno.zip \
    && apt-get purge -y curl unzip \
    && apt-get autoremove -y \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

# Binary + fly hooks
COPY --from=builder /out/media-bot /usr/local/bin/media-bot
COPY .infra/prod/fly/entrypoint.sh /entrypoint
COPY .infra/prod/fly/scripts/ /opt/fly/scripts/
RUN chmod +x /entrypoint \
    && mkdir -p /tmp/mediabot

EXPOSE 8080

ENTRYPOINT ["/entrypoint"]
CMD ["media-bot"]
