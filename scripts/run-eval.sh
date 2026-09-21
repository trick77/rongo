#!/usr/bin/env bash
# Drives the gated evaluation arms against the real corpus and real endpoints.
#
# Not part of the product. It exists because the arms need a dozen environment
# variables in agreement, and getting one of them wrong costs an indexing run
# rather than an error message.
#
# Usage: scripts/run-eval.sh <go-test-run-pattern>
#   scripts/run-eval.sh TestEvalIndex             # build the corpus
#   scripts/run-eval.sh TestEvalMeasureRouting    # the phase 4b routing arms
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
#
# The corpus lives in eval-corpus/ under the repository root, gitignored, NOT
# in /tmp: a reboot clears /tmp, and rebuilding costs a full re-embed of ~9000
# chunks against a paid endpoint — real money between a session and its first
# number. EVAL_DB and EVAL_REPO_ROOT override, the way run-flow-eval.sh takes
# its own.
export BACKEND_EVAL=1
#
# The path is resolved from the MAIN checkout, not $PWD: a worktree runs the
# same script and must reach the same corpus, or every branch pays its own
# re-embed. git rev-parse answers that for a worktree and a plain checkout
# alike.
EVAL_CORPUS_ROOT="$(git rev-parse --path-format=absolute --git-common-dir)/.."
export BACKEND_EVAL_DB="${EVAL_DB:-$EVAL_CORPUS_ROOT/eval-corpus/rongo-eval-small.db}"
export BACKEND_REPOS_FILE=../../../../repos.yaml
export BACKEND_REPO_ROOT="${EVAL_REPO_ROOT:-$EVAL_CORPUS_ROOT/eval-corpus/checkouts}"

cd backend
# -count=1 defeats the test cache, and it is not optional. These arms read
# state Go does not track — the evaluation database, the clones under
# BACKEND_REPO_ROOT, a real model endpoint — so an unchanged package can
# produce a cache hit that replays the PREVIOUS run's numbers under a new
# heading. That happened once: a re-index after a repos.yaml fix reported the
# old corpus counts, down to the same duration to two decimals.
exec go test -v -count=1 -timeout 90m -run "$1" ./internal/retrieve/eval/
