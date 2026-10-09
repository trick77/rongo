#!/usr/bin/env bash
# The Go corpus arms: scripts/measure.sh go <go-test-run-pattern>. Kept under
# this name because the measurement documents cite it.
exec "$(dirname "$0")/measure.sh" go "$@"
