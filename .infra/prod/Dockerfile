# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26
ARG YTDLP_VERSION=2026.03.17

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

ARG YTDLP_VERSION

ENV DEBIAN_FRONTEND=noninteractive \
    MEDIABOT_TEMP_DIR=/tmp/mediabot \
    YTDLP_PATH=/usr/local/bin/yt-dlp \
    PORT=8080

# yt-dlp needs python3 at runtime; ffmpeg is used for audio extraction / merging.
# ca-certificates is required for HTTPS calls to Telegram and YouTube.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        ffmpeg \
        python3 \
        curl \
    && curl -fsSL -o /usr/local/bin/yt-dlp \
        "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp" \
    && chmod +x /usr/local/bin/yt-dlp \
    && apt-get purge -y curl \
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
