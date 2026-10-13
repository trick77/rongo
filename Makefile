.PHONY: build test fe-build fe-test run dev tidy

# Stamped into the binary and shown in the composer's footer. "dev" is the
# honest answer for a local build, and the UI drops the version sentence for
# it; a release passes the tag it is about to create (release.yaml).
VERSION ?= dev

tidy:
	cd backend && go mod tidy

# -race needs cgo; that is independent of the app's CGO_ENABLED=0 build.
test:
	cd backend && CGO_ENABLED=1 go test -race ./...

fe-test:
	cd ui && npx tsc -b && npm run test

# vite.config.ts sets emptyOutDir:false so the tracked backend/web/dist/.gitkeep
# survives a build (go:embed needs it); we clean the whole build output here
# instead, explicitly, so the two halves of this workaround stay together.
# Cleaning only dist/assets misses anything vite copies verbatim from
# ui/public/ into dist/ root — index.html is overwritten every build, but a
# file removed from ui/public/ later would otherwise survive in dist/ forever
# and stay embedded in every binary.
fe-build:
	find backend/web/dist -mindepth 1 ! -name '.gitkeep' -delete
	cd ui && npm ci && npm run build

build: fe-build
	cd backend && CGO_ENABLED=0 go build \
		-ldflags="-s -w -X github.com/trick77/ship/kit/version.Version=$(VERSION)" \
		-o ../bin/rongo ./cmd/rongo

run:
	cd backend && go run ./cmd/rongo

dev:
	./scripts/dev.sh

# ship:begin (owned by ship init)
# The gate: everything CI checks, configured in ship.yaml. Master's ship, as the pre-push hook and the @master
# workflows run it: its sha asked of GitHub (a proxy lags on @master). A local ship: SHIP_CMD=<go-installed binary>,
# one name for here and the hook, so make check and the push gate never run different ships.
# Exactly refs/heads/master (ls-remote matches by suffix), a 40-hex sha or nothing runs, as in the workflows.
SHIP_CMD ?= sha=$$(git ls-remote https://github.com/trick77/ship refs/heads/master | \
		awk '$$2 == "refs/heads/master" { print $$1 }'); \
	echo "$$sha" | grep -Eqx '[0-9a-f]{40}' || \
		{ echo "cannot resolve ship's master on GitHub: '$$sha' (git ls-remote, error above)"; exit 1; }; \
	go run github.com/trick77/ship@$$sha

.PHONY: check hooks lint

check:
	$(SHIP_CMD) check

hooks:
	$(SHIP_CMD) hooks

# Quick lint while editing, at the version check runs.
lint:
	$(SHIP_CMD) lint
# ship:end
