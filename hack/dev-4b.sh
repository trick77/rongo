#!/usr/bin/env bash
# Runs the backend against a COPY of the evaluation index, for driving the
# phase 4b clarification round trip by hand.
#
# Separate from hack/dev.sh because that one sources .env last and therefore
# always wins with the app's own database path; this one deliberately points at
# an already-indexed corpus and turns the poller off, so nothing re-fetches or
# re-embeds while a human is clicking around.
set -euo pipefail

cd "$(dirname "$0")/.."
set -a
# shellcheck disable=SC1091
. ./.env
set +a

export BACKEND_DB_PATH=/tmp/rongo-dev-4b.db
export BACKEND_REPO_ROOT=/tmp/rongo-eval-repos
export BACKEND_INDEX_ENABLED=false
export BACKEND_AUTH_MODE=dev
export BACKEND_ADDR=127.0.0.1:8080
export BACKEND_SESSION_SECRET="${BACKEND_SESSION_SECRET:-dev-secret-not-for-prod}"

cd backend
exec go run ./cmd/rongo
