#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
: "${IMG_DIR:=/dev/shm/criu-inject}"
: "${A_NAME:=inj-src}"
: "${B_NAME:=inj-dst}"
: "${IMAGE:=wrapper-pingserver-criu}"
: "${SRC_PORT:=5242}"
: "${DST_PORT:=5243}"
: "${CRIU_HOST_BIN:=}"

if ! sudo -n true 2>/dev/null; then
  echo "sudo 需要可用（建议先执行一次: sudo -v）" >&2
  exit 2
fi

if [[ ! -x ./control ]]; then
  echo "missing ./control; run ./run.sh first" >&2
  exit 2
fi

MIG_ARGS=(migrate --img-dir "$IMG_DIR" --a-name "$A_NAME" --b-name "$B_NAME" --image "$IMAGE" --src-port "$SRC_PORT" --dst-port "$DST_PORT")
if [[ -n "$CRIU_HOST_BIN" ]]; then
  MIG_ARGS+=(--criu-host-bin "$CRIU_HOST_BIN")
fi
sudo ./control "${MIG_ARGS[@]}"

LOG=client.out
if [[ -f "$LOG" ]]; then
  deadline=$((SECONDS+30))
  while (( SECONDS < deadline )); do
    if grep -q "服务中断" "$LOG"; then
      grep "服务中断" "$LOG" | tail -n 1
      exit 0
    fi
    sleep 0.2
  done
  echo "no downtime line yet; check: tail -f $LOG" >&2
fi
