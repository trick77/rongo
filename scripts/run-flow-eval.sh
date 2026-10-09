#!/usr/bin/env bash
# The flow corpus arms: scripts/measure.sh flow <go-test-run-pattern>. Kept
# under this name because the measurement documents cite it.
exec "$(dirname "$0")/measure.sh" flow "$@"
