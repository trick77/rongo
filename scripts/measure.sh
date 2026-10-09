#!/usr/bin/env bash
# Drives the gated evaluation arms against a real corpus and real endpoints.
#
# Not part of the product. It exists because the arms need a dozen environment
# variables in agreement, and getting one of them wrong costs an indexing run
# rather than an error message.
#
# Usage: scripts/measure.sh <corpus> <go-test-run-pattern>
#
# <corpus> is one of:
#
#   go    the Go corpus (peeq, rongo, go-sqlite3), pinned 2026-08-20.
#         scripts/run-eval.sh <pattern> is this.
#   flow  a MULTI-REPOSITORY corpus: by default the eight-repository Sock Shop
#         estate pinned in docs/measurements/2026-09-10-flow-corpus.md.
#         scripts/run-flow-eval.sh <pattern> is this.
#
#   scripts/measure.sh go TestEvalIndex                   # build the Go corpus
#   scripts/measure.sh go TestEvalMeasureRouting          # the phase 4b routing arms
#   scripts/measure.sh flow TestEvalIndex                 # build the flow corpus
#   scripts/measure.sh flow TestExpandFlowQuestions       # freeze the expansions
#   scripts/measure.sh flow TestFlowGathered              # what reaches the answer
#   scripts/measure.sh flow 'TestFlowEdgeReach$'          # the edge walk on its own
#   scripts/measure.sh flow 'TestEvalMeasureAnswers$'     # the answers, judged
#
# The two corpora are two databases and two repository roots, on purpose: one
# database holding two corpora measures neither.
#
# BACKEND_EVAL_GAP switches the directed gap pass — one short-gate call after
# the walk and the crossings, whose names are then resolved without a second
# call. It is harness-only until it is measured, so the answer arm has it OFF
# and BACKEND_EVAL_GAP=1 turns it on:
#   BACKEND_EVAL_GAP=1 scripts/measure.sh flow 'TestEvalMeasureAnswers$'
# TestFlowGathered reports the gap arms beside the others by default;
# BACKEND_EVAL_GAP=0 leaves them out and saves one call per question.
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <go|flow> <go-test-run-pattern>" >&2
  exit 2
fi
corpus=$1
pattern=$2

cd "$(dirname "$0")/.."
set -a
# shellcheck disable=SC1091
. ./.env
set +a

# The variables below override whatever .env says, deliberately. .env is the
# dev app's configuration and points BACKEND_REPO_ROOT at a relative ./repos,
# which resolves against backend/ once the test runs and silently is not
# there. The evaluation keeps its own corpus and its own database file, away
# from the app's.
export BACKEND_EVAL=1

case "$corpus" in
  go)
    # The corpus lives in eval-corpus/ under the repository root, gitignored,
    # NOT in /tmp: a reboot clears /tmp, and rebuilding costs a full re-embed
    # of ~9000 chunks against a paid endpoint — real money between a session
    # and its first number. EVAL_DB and EVAL_REPO_ROOT override; the
    # repository list is the committed one and does not.
    #
    # The path is resolved from the MAIN checkout, not $PWD: a worktree runs
    # the same script and must reach the same corpus, or every branch pays its
    # own re-embed. git rev-parse answers that for a worktree and a plain
    # checkout alike.
    EVAL_CORPUS_ROOT="$(git rev-parse --path-format=absolute --git-common-dir)/.."
    export BACKEND_EVAL_DB="${EVAL_DB:-$EVAL_CORPUS_ROOT/eval-corpus/rongo-eval-small.db}"
    export BACKEND_REPOS_FILE=../../../../repos.yaml
    export BACKEND_REPO_ROOT="${EVAL_REPO_ROOT:-$EVAL_CORPUS_ROOT/eval-corpus/checkouts}"
    ;;
  flow)
    # The clones live under /tmp/sockshop-pin, each on a branch called
    # pin20260910 (see the measurement document for the shas); the manifest
    # that lists them is committed beside the questions.
    #
    # Another corpus of the same shape — a private one, say — is a matter of
    # four variables, all read from the environment before the defaults apply:
    #   EVAL_DB, EVAL_REPOS_FILE, EVAL_REPO_ROOT, and for the answer arm
    #   BACKEND_EVAL_FLOW_QUESTIONS, BACKEND_EVAL_FLOW_EXPANSIONS, BACKEND_EVAL_RUBRICS
    # (the last three default to the flow corpus's committed files).
    export BACKEND_EVAL_DB="${EVAL_DB:-/tmp/rongo-flow.db}"
    export BACKEND_REPOS_FILE="${EVAL_REPOS_FILE:-flow-repos.yaml}"
    export BACKEND_REPO_ROOT="${EVAL_REPO_ROOT:-/tmp/rongo-flow-repos}"
    ;;
  *)
    echo "unknown corpus '$corpus': want go or flow" >&2
    exit 2
    ;;
esac

cd backend
# -count=1 defeats the test cache, and it is not optional. These arms read
# state Go does not track — the evaluation database, the clones under
# BACKEND_REPO_ROOT, a real model endpoint — so an unchanged package can
# produce a cache hit that replays the PREVIOUS run's numbers under a new
# heading. That happened once: a re-index after a repos.yaml fix reported the
# old corpus counts, down to the same duration to two decimals.
exec go test -v -count=1 -timeout 90m -run "$pattern" ./internal/retrieve/eval/
