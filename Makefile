.PHONY: build test coverage backend-coverage fe-build fe-test fe-coverage run dev tidy

tidy:
	cd backend && go mod tidy

test:
	cd backend && go test ./...

# Line coverage with the same floor and scripts as ../peeq and ../loom
# (hack/coverage-floors, hack/coverage-gate.sh). CI runs the patch gate on top
# (hack/patch-coverage.sh); locally the floor is the thing to watch.
coverage: backend-coverage fe-coverage

# -coverpkg=./... attributes coverage across package boundaries: code exercised
# only by another package's tests (httpapi drives threads and store) is
# otherwise reported as uncovered. The race detector needs cgo; that is
# independent of the app's CGO_ENABLED=0 build.
backend-coverage:
	mkdir -p coverage
	cd backend && CGO_ENABLED=1 go test -race -covermode=atomic -coverpkg=./... -coverprofile=../coverage/backend.out ./...
	cd backend && go run github.com/boumenot/gocover-cobertura@v1.5.0 < ../coverage/backend.out > ../coverage/backend.xml
	./hack/coverage-gate.sh backend

fe-test:
	cd ui && npx tsc -b && npm run test -- --run

fe-coverage:
	cd ui && npm run test -- --run --coverage
	./hack/coverage-gate.sh ui

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
	cd backend && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/rongo ./cmd/rongo

run:
	cd backend && go run ./cmd/rongo

dev:
	./hack/dev.sh
