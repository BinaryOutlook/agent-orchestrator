#!/bin/bash
# Build the browser service and (re)start its container, then wait until ready.
#   NAME=rbpoc PORT=8080 NETNS=<the AO daemon's container> ./up.sh
# The daemon then gets AO_AGENT_BROWSER_URL=http://127.0.0.1:8080 and
# AO_AGENT_BROWSER_TOKEN=$(cat /tmp/rbpoc-token); agents get ./browser with RB_TOKEN.
set -euo pipefail
cd "$(dirname "$0")"
NAME=${NAME:-rbpoc}; PORT=${PORT:-8080}
[ -s /tmp/rbpoc-token ] || (umask 077; openssl rand -hex 24 > /tmp/rbpoc-token)
free_mb=$(df -k / | tail -1 | awk '{print int($4/1024)}')
if [ "$free_mb" -lt 500 ]; then echo "refusing to build: only ${free_mb} MB free on /" >&2; exit 3; fi
GOWORK=off GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/rbhost ./host
docker build -q -t rbpoc:latest . >/dev/null
docker rm -f "$NAME" >/dev/null 2>&1 || true
extra=(-p "127.0.0.1:${PORT}:8080")
# NETNS joins the AO daemon's container network (it publishes 8080), so the agent's
# Chromium and the session's dev servers share localhost, as on one machine.
[ -n "${NETNS:-}" ] && extra=(--network "container:${NETNS}")
# The browser takes this Mac's time zone, language and light/dark mode, as the
# person's own Chrome would. --init reaps Chromium's exited helper processes.
tz=$(readlink /etc/localtime | sed 's#.*/zoneinfo/##')
lang=$(defaults read -g AppleLocale 2>/dev/null | sed 's/@.*//; s/_/-/')
scheme=$(defaults read -g AppleInterfaceStyle 2>/dev/null | grep -qi dark && echo dark || echo light)
docker run -d --init --name "$NAME" --shm-size=1g ${extra[@]+"${extra[@]}"} \
  -e RB_TZ="${RB_TZ:-$tz}" -e RB_LANG="${RB_LANG:-$lang}" -e RB_SCHEME="${RB_SCHEME:-$scheme}" \
  -e RB_CHROME_LOG="${RB_CHROME_LOG:-}" -e RB_TOKEN="$(cat /tmp/rbpoc-token)" rbpoc:latest >/dev/null
for _ in $(seq 1 120); do curl -sf "http://127.0.0.1:${PORT}/stats?token=$(cat /tmp/rbpoc-token)" >/dev/null && break; sleep 0.5; done
echo "up $NAME on :$PORT"
