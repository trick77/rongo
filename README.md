![rongo](rongo-wide.png)

Understanding code without going through a person first. rongo indexes an organisation's
repositories, asks back which mechanism is meant when a question is ambiguous, and explains it in
the chosen role — **BA** in domain terms, **DEV** in technical ones. Every statement is sourced.

Technical decisions: [`AGENTS.md`](AGENTS.md).

## State

The question-answer path is in place: indexing, retrieval, the follow-up question on ambiguous
questions and sourced answers, measured in [`docs/measurements/`](docs/measurements). Sign-in runs
over OIDC (`BACKEND_AUTH_MODE=oidc`), and the stack is operated with `compose.yaml` behind a
reverse proxy that terminates TLS.

## Requirements

- Go, Node.js
- `git`
- `rg` (ripgrep)
- `ctags` — **universal-ctags**, not the BSD ctags macOS ships at
  `/usr/bin/ctags`. Install with `brew install universal-ctags`, and make sure
  it comes before `/usr/bin` on `PATH`.

## Development

```
cp .env.example .env   # fill in the five active lines: the model and
                        # embedding endpoints with their keys, and
                        # BACKEND_SESSION_SECRET (openssl rand -base64 32)
make dev                # backend + Vite dev server with hot reload
```

Everything else in `.env.example` is commented out and shows its default. The
five active ones have none, and the process refuses to start without them.

`make dev` starts the backend on `127.0.0.1:8080` and Vite on
`127.0.0.1:5173`; `/api/*` is proxied through to the backend.

Further targets: `make build` (binary), `make test` (Go tests), `make fe-test`
(typecheck + frontend tests). Details on manual checks:
[`docs/manual-verification.md`](docs/manual-verification.md).
