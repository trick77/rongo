#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DB_PATH=${BACKEND_DB_PATH:-/tmp/rongo-dev.db}
REPO_ROOT=${BACKEND_REPO_ROOT:-/tmp/rongo-dev-repos}

cleanup() {
  if [ -n "${BACKEND_PID:-}" ]; then
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$REPO_ROOT"

(
  cd "$ROOT/backend"
  BACKEND_SESSION_SECRET=${BACKEND_SESSION_SECRET:-dev-secret} \
  BACKEND_AUTH_MODE=dev \
  BACKEND_ADDR=127.0.0.1:8080 \
  BACKEND_PUBLIC_URL=http://127.0.0.1:8080 \
  BACKEND_DB_PATH="$DB_PATH" \
  BACKEND_REPO_ROOT="$REPO_ROOT" \
  go run ./cmd/rongo
) &
BACKEND_PID=$!

cd "$ROOT/ui"
npm run dev -- --host 127.0.0.1
