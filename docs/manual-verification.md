# Manual verification

Flows that no automated test covers, because they need real binaries or a real
browser. Run these by hand before calling a phase done.

## Phase 1 — skeleton

Prerequisites (the dev environment runs without Docker, so these come from your
machine):

- `git`
- `rg` (ripgrep)
- `ctags` — **universal-ctags**, not the BSD ctags macOS ships at
  `/usr/bin/ctags`. Install with `brew install universal-ctags` and make sure it
  precedes `/usr/bin` on `PATH`.

Checks:

1. `make dev` starts the backend and Vite; `http://127.0.0.1:5173/` shows the
   shell and `/api/me` returns the dev user through the proxy.
2. Temporarily put a directory containing only BSD `ctags` first on `PATH` and
   run `make run`. It must exit 1 naming universal-ctags — not start with an
   empty symbol index.
3. `RONGO_AUTH_MODE=dev RONGO_ADDR=0.0.0.0:8080 make run` must refuse to start.
4. `make build` produces `bin/rongo`; running it serves the built SPA at
   `http://127.0.0.1:8080/`.
