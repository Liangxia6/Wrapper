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
: "${MODE:=foreground}"
: "${SKIP_CONTROL_BUILD:=0}"
: "${SKIP_CLIENT_BUILD:=0}"

# Client tuning knobs (passed as flags)
: "${CLIENT_INTERVAL:=200ms}"
: "${CLIENT_INTERVAL_AFTER_MIGRATE:=20ms}"
: "${CLIENT_IO_TIMEOUT:=1200ms}"
: "${CLIENT_IO_TIMEOUT_AFTER_MIGRATE:=250ms}"
: "${CLIENT_DIAL_TIMEOUT:=900ms}"
: "${CLIENT_DIAL_BACKOFF:=50ms}"
: "${CLIENT_QUIET:=0}"

# Transparent mode (UDP proxy in front of A/B): client always connects to proxy.
: "${USE_PROXY:=0}"
: "${PROXY_LISTEN_ADDR:=127.0.0.1:5342}"
: "${BACKEND_FILE:=$IMG_DIR/backend.addr}"

GO_BIN="/usr/local/go/bin/go"
if [[ ! -x "$GO_BIN" ]]; then
  GO_BIN="go"
fi

if [[ "$SKIP_CONTROL_BUILD" != "1" ]]; then
  "$GO_BIN" build -o control ./Server/Control
fi

if [[ "$SKIP_CLIENT_BUILD" != "1" ]]; then
  "$GO_BIN" build -o ./Client/client_bin ./Client/APP
fi

if [[ "$USE_PROXY" == "1" ]]; then
  "$GO_BIN" build -o ./proxy_bin ./Proxy
fi

if ! sudo -n true 2>/dev/null; then
  echo "sudo 需要可用（建议先执行一次: sudo -v）" >&2
  exit 2
fi

UP_ARGS=(up --img-dir "$IMG_DIR" --a-name "$A_NAME" --b-name "$B_NAME" --image "$IMAGE" --src-port "$SRC_PORT" --dst-port "$DST_PORT")
if [[ -n "$CRIU_HOST_BIN" ]]; then
  UP_ARGS+=(--criu-host-bin "$CRIU_HOST_BIN")
fi
sudo ./control "${UP_ARGS[@]}"

if [[ "$USE_PROXY" == "1" ]]; then
  echo "127.0.0.1:${SRC_PORT}" >"$BACKEND_FILE"
  export TARGET_ADDR="$PROXY_LISTEN_ADDR"
  export TRANSPARENT=1
else
  export TARGET_ADDR="127.0.0.1:${SRC_PORT}"
  # 透明迁移（内部 UDP 解耦）同样需要 APP 在迁移期间保持 session，
  # 避免因为短暂 read deadline 超时就主动结束连接。
  export TRANSPARENT=1
fi

CLIENT_ARGS=(
  -interval "$CLIENT_INTERVAL"
  -interval-after-migrate "$CLIENT_INTERVAL_AFTER_MIGRATE"
  -io-timeout "$CLIENT_IO_TIMEOUT"
  -io-timeout-after-migrate "$CLIENT_IO_TIMEOUT_AFTER_MIGRATE"
  -dial-timeout "$CLIENT_DIAL_TIMEOUT"
  -dial-backoff "$CLIENT_DIAL_BACKOFF"
)
if [[ -n "${TRANSPARENT:-}" ]]; then
  CLIENT_ARGS+=( -stay-connected )
fi
if [[ "$CLIENT_QUIET" == "1" ]]; then
  CLIENT_ARGS+=( -quiet )
fi

LOG=client.out
PIDFILE=client.pid

if [[ "$USE_PROXY" == "1" ]]; then
  rm -f proxy.pid
  BACKEND_FILE="$BACKEND_FILE" LISTEN_ADDR="$PROXY_LISTEN_ADDR" ./proxy_bin >proxy.log 2>&1 &
  echo $! >proxy.pid
fi

if [[ "$MODE" == "foreground" ]]; then
  DOWN_ARGS=(down --img-dir "$IMG_DIR" --a-name "$A_NAME" --b-name "$B_NAME" --image "$IMAGE" --src-port "$SRC_PORT" --dst-port "$DST_PORT")
  cleanup() {
    sudo ./control "${DOWN_ARGS[@]}" >/dev/null 2>&1 || true
  }
  trap cleanup EXIT

  ./Client/client_bin "${CLIENT_ARGS[@]}"
  exit 0
fi

rm -f "$PIDFILE"
./Client/client_bin "${CLIENT_ARGS[@]}" >"$LOG" 2>&1 &
echo $! >"$PIDFILE"
