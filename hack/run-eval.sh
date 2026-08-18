#!/usr/bin/env bash
# Drives the gated evaluation arms against the real corpus and real endpoints.
#
# Not part of the product. It exists because the arms need a dozen environment
# variables in agreement, and getting one of them wrong costs an indexing run
# rather than an error message.
#
# Usage: hack/run-eval.sh <go-test-run-pattern>
#   hack/run-eval.sh TestEvalIndex             # build the corpus
#   hack/run-eval.sh TestEvalMeasureRouting    # the phase 4b routing arms
set -euo pipefail

cd "$(dirname "$0")/.."
set -a
# shellcheck disable=SC1091
. ./.env
set +a

# These four override whatever .env says, deliberately. .env is the dev app's
# configuration and points BACKEND_REPO_ROOT at a relative ./repos, which
# resolves against backend/ once the test runs and silently is not there. The
# evaluation keeps its own corpus and its own database file, away from the app's.
export BACKEND_EVAL=1
export BACKEND_EVAL_DB=/tmp/rongo-eval-small.db
export BACKEND_REPOS_FILE=../../../../repos.yaml
export BACKEND_REPO_ROOT=/tmp/rongo-eval-repos

cd backend
exec go test -v -timeout 90m -run "$1" ./internal/retrieve/eval/
