#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

# Documented in the README as the way to set BACKEND_SESSION_SECRET (and
# anything else) for `make dev`. Values below still win where this script
# forces dev-specific settings (addr, auth mode); only unset ones fall
# through from .env.
if [ -f "$ROOT/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$ROOT/.env"
  set +a
fi

DB_PATH=${BACKEND_DB_PATH:-/tmp/rongo-dev.db}
REPO_ROOT=${BACKEND_REPO_ROOT:-/tmp/rongo-dev-repos}

# A relative path (e.g. from .env, copied from .env.example) is meant to
# resolve against the repo root. The backend subshell below cd's into
# backend/ before running go, so a relative path exported as-is would
# resolve to backend/data or backend/repos instead — while compose mounts
# ./data and ./repos at the repo root and .gitignore anchors them there too.
# Resolve here, once, so the path means the same thing regardless of where
# the process actually runs.
case "$DB_PATH" in
  /*) ;;
  *) DB_PATH="$ROOT/${DB_PATH#./}" ;;
esac
case "$REPO_ROOT" in
  /*) ;;
  *) REPO_ROOT="$ROOT/${REPO_ROOT#./}" ;;
esac

# Enable job control so the backend subshell gets its own process group;
# without it, $! is the subshell's PID but `kill` on that PID alone never
# reaches `go run`'s child (the compiled binary), which keeps holding the
# port on any exit that isn't Ctrl-C.
set -m

cleanup() {
  if [ -n "${BACKEND_PID:-}" ]; then
    kill -- "-$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$REPO_ROOT"

(
  cd "$ROOT/backend"
  BACKEND_SESSION_SECRET=${BACKEND_SESSION_SECRET:-dev-secret-not-for-prod} \
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
