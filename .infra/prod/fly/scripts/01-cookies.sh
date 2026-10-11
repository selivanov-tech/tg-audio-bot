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

# Nothing to do when there is neither a seed secret nor a bucket to mirror
# from: no path is exported, and the bot runs without cookies.
if [ -z "${YTDLP_COOKIES:-}" ] && [ -z "${BUCKET_NAME:-}" ]; then
    return 0 2>/dev/null || exit 0
fi

cookies_path=/tmp/yt-cookies.txt

if [ -n "${YTDLP_COOKIES:-}" ]; then
    printf '%s' "$YTDLP_COOKIES" | base64 -d > "$cookies_path"
else
    # Bucket-only mode: the file is replaced by the mirror's copy at startup,
    # but it must already exist and be valid — yt-dlp aborts when --cookies
    # points at a missing file, and a header-only jar is legal.
    printf '# Netscape HTTP Cookie File\n' > "$cookies_path"
fi

chmod 600 "$cookies_path"
export YTDLP_COOKIES_PATH="$cookies_path"
