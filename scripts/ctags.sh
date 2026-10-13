#!/bin/sh
# The backend tests index a real fixture repository, and rongo shells out to
# universal-ctags for it. macOS ships BSD ctags, which yields an empty symbol
# index rather than an error, so the build is checked, not just the name.
# ship's CI runner has none: installed there, never on a developer's machine.
set -eu

universal() { ctags --version 2>/dev/null | grep -q 'Universal Ctags'; }

universal && exit 0
if [ "${GITHUB_ACTIONS:-}" = true ]; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq --no-install-recommends universal-ctags
  universal && exit 0
fi
echo "ctags: universal-ctags is not on PATH (macOS: brew install universal-ctags)" >&2
exit 1
