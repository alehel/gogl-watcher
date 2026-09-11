# Bare version (1.2.3, or 1.2.3-4-gabcdef between tags): the UI adds the "v".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)

.PHONY: all web build test run mock docker clean

all: build

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/alehel/gogl-watcher/internal/config.Version=$(VERSION)" -o bin/gogl-watcher ./cmd/gogl-watcher

test:
	go test ./...
	cd web && npx tsc --noEmit

run:
	DATA_DIR=./data LIBRARY_DIR=./library go run ./cmd/gogl-watcher

mock:
	MOCK_GOG=1 DATA_DIR=./data-mock LIBRARY_DIR=./library-mock go run ./cmd/gogl-watcher

docker:
	docker build --build-arg VERSION=$(VERSION) -t gogl-watcher:$(VERSION) -t gogl-watcher:latest .

clean:
	rm -rf bin web/dist/* data-mock library-mock
	touch web/dist/.gitkeep
