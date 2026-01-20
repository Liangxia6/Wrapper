#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

: "${IMG_DIR:=/dev/shm/criu-inject}"
: "${A_NAME:=inj-src}"
: "${B_NAME:=inj-dst}"
: "${IMAGE:=wrapper-pingserver-criu}"
: "${SRC_PORT:=5242}"
: "${DST_PORT:=5243}"

PIDFILE=client.pid

if ! sudo -n true 2>/dev/null; then
  echo "sudo 需要可用（建议先执行一次: sudo -v）" >&2
  exit 2
fi

if [[ -f "$PIDFILE" ]]; then
  pid=$(cat "$PIDFILE" || true)
  if [[ -n "${pid:-}" ]]; then
    kill "$pid" 2>/dev/null || true
  fi
  rm -f "$PIDFILE"
fi

# In practice, the pidfile can be stale/overwritten. Ensure we stop any
# remaining client binaries from previous runs.
if command -v pgrep >/dev/null 2>&1; then
  while read -r pid; do
    [[ -n "${pid:-}" ]] || continue
    kill "$pid" 2>/dev/null || true
  done < <(pgrep -f "Client/client_bin" || true)
fi

if [[ -x ./control ]]; then
  sudo ./control down --img-dir "$IMG_DIR" --a-name "$A_NAME" --b-name "$B_NAME" --image "$IMAGE" --src-port "$SRC_PORT" --dst-port "$DST_PORT" || true
else
  echo "missing ./control; removing containers directly" >&2
  sudo podman rm -f "$A_NAME" "$B_NAME" 2>/dev/null || true
  sudo rm -rf "$IMG_DIR" 2>/dev/null || true
fi
