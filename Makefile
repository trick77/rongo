.PHONY: build test fe-build fe-test run dev tidy

tidy:
	cd backend && go mod tidy

test:
	cd backend && go test ./...

fe-test:
	cd ui && npx tsc -b && npm run test -- --run

# vite.config.ts sets emptyOutDir:false so the tracked backend/web/dist/.gitkeep
# survives a build (go:embed needs it); we clean stale hashed assets here
# instead, explicitly, so the two halves of this workaround stay together.
fe-build:
	rm -rf backend/web/dist/assets
	cd ui && npm ci && npm run build

build: fe-build
	cd backend && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/rongo ./cmd/rongo

run:
	cd backend && go run ./cmd/rongo

dev:
	./hack/dev.sh
