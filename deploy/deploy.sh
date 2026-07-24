#!/bin/sh
# Deploy/update the FabScreenTime backend on the Alpine host.
#
# Idempotent: clones on first run, fast-forwards afterwards, rebuilds the image
# and recreates only this project's container. It never touches the household
# compose project in /root/docker-compose.yml (pihole/DNS, the deliberately
# stopped ib-gateway) — this is its own `-p fabscreentime` project.
#
#   sh deploy.sh [git-ref]      default: the branch below
set -eu

REPO="${FST_REPO:-https://github.com/larsenglund/fabscreentime.git}"
DIR="${FST_DIR:-/opt/fabscreentime}"
REF="${1:-${FST_REF:-claude/screentime-app-plan-nttogj}}"
PROJECT=fabscreentime

if [ -d "$DIR/.git" ]; then
  echo "==> Updating $DIR to $REF"
  git -C "$DIR" fetch --depth 50 origin "$REF"
  git -C "$DIR" checkout -q -B "$REF" FETCH_HEAD
else
  echo "==> Cloning $REPO into $DIR"
  git clone --depth 50 --branch "$REF" "$REPO" "$DIR"
fi

VERSION="$(git -C "$DIR" describe --tags --always 2>/dev/null || echo dev)"
echo "==> Building and starting ($VERSION)"
cd "$DIR/deploy"
FST_VERSION="$VERSION" docker compose -p "$PROJECT" up -d --build

echo "==> Waiting for health"
i=0
while [ "$i" -lt 30 ]; do
  if wget -qO- --timeout=3 "http://127.0.0.1:${FST_PORT:-8090}/healthz" >/dev/null 2>&1; then
    echo "==> Healthy: http://$(hostname -i 2>/dev/null | awk '{print $1}'):${FST_PORT:-8090}/"
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done
echo "!! Backend did not become healthy in time; recent logs:" >&2
docker compose -p "$PROJECT" logs --tail 40 >&2
exit 1
