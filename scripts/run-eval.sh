#!/usr/bin/env bash
# The Go corpus arms: scripts/measure.sh go <go-test-run-pattern>. Kept under
# this name because the measurement documents cite it.
[ $# -eq 1 ] || { echo "usage: $0 <go-test-run-pattern>" >&2; exit 2; }
exec "$(dirname "$0")/measure.sh" go "$1"
