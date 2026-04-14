#!/usr/bin/env sh
# Entrypoint for the production container on Fly.io.
# Runs any hook scripts dropped into /opt/fly/scripts/, then execs the main
# command (defaults to the media-bot binary).

set -e

for f in /opt/fly/scripts/*.sh; do
    [ -f "$f" ] && sh "$f"
done

if [ $# -gt 0 ]; then
    exec "$@"
else
    exec media-bot
fi
