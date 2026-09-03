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
   run `BACKEND_SESSION_SECRET=dev-secret-not-for-prod make run`. It must exit
   1 naming universal-ctags — not start with an empty symbol index.
3. `BACKEND_SESSION_SECRET=dev-secret-not-for-prod BACKEND_AUTH_MODE=dev BACKEND_ADDR=0.0.0.0:8080 make run`
   must refuse to start on the address, not on a missing session secret.
4. `make build` produces `bin/rongo`; running it serves the built SPA at
   `http://127.0.0.1:8080/`.

## Phase 4b — routing and clarification

`make dev` (or `hack/dev.sh`); dev auth logs in automatically, no login form.
UI at `http://127.0.0.1:5173/`.

Checks:

1. Ask a question that several independent modules could answer. A card
   titled "Which one do you mean?" appears with an ochre border and one row per
   candidate (title, repo · branch, summary) — the turn does not answer on
   its own.
2. Click a candidate. The card collapses to "Chosen: {title}" with a
   hairline border, the chosen row carries a "Chosen" badge, and the answer
   streams below it.
3. Open the "How does rongo know this?" details block once the answer is done.
   It lists an "N sources" count and one `repo · path:start-end (branch)` line
   per source. Click one (or its row in the Sources pane): an overlay opens
   the file out of rongo's own checkout, at the commit the answer was written
   from, with the cited lines marked and scrolled into view. Confirm the
   marked lines say what the answer claims; Escape closes it.
4. Reload the page. The thread reloads with the same clarification card
   already collapsed on "Chosen: {title}" — no re-asking, no flash of the
   open card.
5. On that answered turn, click "Explain as Developer" (visible only once
   the turn is done, unclarified, and error-free). Watch the network tab or
   backend log: no retrieval/search call fires, only the re-explain request
   on the stored sources. A new turn appears below with the Developer answer; the
   original Analyst answer stays untouched above it.
