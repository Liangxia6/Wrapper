#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

: "${IMG_DIR:=/dev/shm/criu-inject}"
: "${A_NAME:=inj-src}"
: "${B_NAME:=inj-dst}"

PIDFILE=client.pid

if [[ -f "$PIDFILE" ]]; then
	pid=$(cat "$PIDFILE" || true)
	if [[ -n "${pid:-}" ]]; then
		kill "$pid" 2>/dev/null || true
	fi
	rm -f "$PIDFILE"
fi

echo "[cleanup.sh] remove A/B containers (if any)"
sudo podman rm -f "$A_NAME" "$B_NAME" 2>/dev/null || true

echo "[cleanup.sh] remove checkpoint dir: $IMG_DIR"
sudo rm -rf "$IMG_DIR" 2>/dev/null || true

echo "[cleanup.sh] prune dangling images (safe, reclaims untagged images)"
sudo podman image prune -f
