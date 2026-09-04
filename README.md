![rongo](rongo-wide.png)

Ask questions about your codebases and get answers with sources.

rongo clones the repositories you list, indexes them, and answers in one of
two voices: Analyst, which explains what the code does in the terms of the
domain, or Developer, which explains how it does it and shows the code. When
a question could mean several things it asks which one before answering.
Every claim names repository, branch, file, lines and the commit it was read
at, and the file opens in rongo's own viewer at that commit. The name comes
from Rongorongo, the Easter Island script that nobody has deciphered.

The index holds the code and docs as written, never summaries of them. The
model reads the files that matched. If an answer would lead into code that
isn't indexed, it says so instead of guessing.

## How it works

One Go binary, one SQLite file (FTS5 for text, sqlite-vec for embeddings),
React UI embedded in the binary. Symbols come from universal-ctags, search
from ripgrep, checkouts from git. Chat goes to an OpenAI-compatible endpoint
serving the MiMo deployments named in `backend/internal/llm/client.go`,
embeddings to any OpenAI-compatible `/embeddings` endpoint.

## Running it

`make dev` runs it locally with hot reload. Needs Go, Node.js, `git`, `rg`
and universal-ctags (the BSD ctags macOS ships doesn't work and rongo says so
at startup).

`compose.yaml` runs it in production behind a TLS-terminating reverse proxy
with OIDC login. The comment at the top of that file covers the first run.

## Development

```
make test        # Go tests
make fe-test     # typecheck and frontend tests
make coverage    # both, with the 75% floor CI enforces
make build       # bin/rongo with the UI embedded
```

Design decisions and invariants: [`AGENTS.md`](AGENTS.md). The measurements
behind them: [`docs/measurements/`](docs/measurements). Checks that need a
real browser: [`docs/manual-verification.md`](docs/manual-verification.md).
