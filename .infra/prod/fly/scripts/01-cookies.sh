#!/usr/bin/env sh
# Materialize yt-dlp cookies from the YTDLP_COOKIES Fly secret.
#
# The secret holds base64-encoded Netscape-format cookies.txt content.
# When set, we decode it to /tmp/yt-cookies.txt and export YTDLP_COOKIES_PATH
# so the bot's config picks it up. When unset, this script is a no-op and the
# bot runs without cookies.
#
# Sourced (not executed) by entrypoint.sh, so `export` propagates to media-bot.

set -e

if [ -z "${YTDLP_COOKIES:-}" ]; then
    return 0 2>/dev/null || exit 0
fi

cookies_path=/tmp/yt-cookies.txt
printf '%s' "$YTDLP_COOKIES" | base64 -d > "$cookies_path"
chmod 600 "$cookies_path"
export YTDLP_COOKIES_PATH="$cookies_path"
