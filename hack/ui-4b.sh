#!/usr/bin/env bash
# Runs the Vite dev server for the phase 4b hand-check, alongside hack/dev-4b.sh.
set -euo pipefail
cd "$(dirname "$0")/../ui"
exec npm run dev -- --host 127.0.0.1
