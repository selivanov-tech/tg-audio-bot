#!/usr/bin/env sh
# Entrypoint for the production container on Fly.io.
# Sources any hook scripts dropped into /opt/fly/scripts/, then execs the main
# command (defaults to the media-bot binary). Hooks are sourced (not executed)
# so they can export env vars that reach the main process.

set -e

for f in /opt/fly/scripts/*.sh; do
    [ -f "$f" ] && . "$f"
done

if [ $# -gt 0 ]; then
    exec "$@"
else
    exec media-bot
fi
