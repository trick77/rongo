#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DB_PATH=${RONGO_DB_PATH:-/tmp/rongo-dev.db}
REPO_ROOT=${RONGO_REPO_ROOT:-/tmp/rongo-dev-repos}

cleanup() {
  if [ -n "${BACKEND_PID:-}" ]; then
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$REPO_ROOT"

(
  cd "$ROOT/backend"
  RONGO_SESSION_SECRET=${RONGO_SESSION_SECRET:-dev-secret} \
  RONGO_AUTH_MODE=dev \
  RONGO_ADDR=127.0.0.1:8080 \
  RONGO_PUBLIC_URL=http://127.0.0.1:8080 \
  RONGO_DB_PATH="$DB_PATH" \
  RONGO_REPO_ROOT="$REPO_ROOT" \
  go run ./cmd/rongo
) &
BACKEND_PID=$!

cd "$ROOT/ui"
npm run dev -- --host 127.0.0.1
