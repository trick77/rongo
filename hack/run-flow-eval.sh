#!/usr/bin/env bash
# Drives the flow-corpus evaluation arms: the eight-repository Sock Shop
# estate pinned in docs/measurements/2026-09-10-flow-corpus.md.
#
# Not hack/run-eval.sh, on purpose: that script hard-overrides the database
# and the repository root to the Go corpus's paths, and one database holding
# two corpora measures neither. The clones live under /tmp/sockshop-pin, each
# on a branch called pin20260910 (see the measurement document for the shas);
# the manifest that lists them is committed beside the questions.
#
# Usage: hack/run-flow-eval.sh <go-test-run-pattern>
#   hack/run-flow-eval.sh TestEvalIndex            # build the flow corpus
#   hack/run-flow-eval.sh TestExpandFlowQuestions  # freeze the expansions
#   hack/run-flow-eval.sh TestFlowGathered         # what reaches the answer
#   hack/run-flow-eval.sh TestFlowEdgeReach        # the edge walk on its own
set -euo pipefail

cd "$(dirname "$0")/.."
set -a
# shellcheck disable=SC1091
. ./.env
set +a

export BACKEND_EVAL=1
export BACKEND_EVAL_DB=/tmp/rongo-flow.db
export BACKEND_REPOS_FILE=flow-repos.yaml
export BACKEND_REPO_ROOT=/tmp/rongo-flow-repos

cd backend
# -count=1 for the reason hack/run-eval.sh gives: these arms read state Go
# does not track, and a cache hit replays the previous run's numbers.
exec go test -v -count=1 -timeout 90m -run "$1" ./internal/retrieve/eval/
