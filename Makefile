BIN := bin/md2pdf
VERSION ?= 0.1.0

.PHONY: build test test-short assets clean

build: assets
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/md2pdf

# Download the embedded fonts and JavaScript libraries if missing.
assets:
	./scripts/fetch-assets.sh

test:
	go vet ./...
	go test ./...

test-short:
	go test -short ./...

clean:
	rm -rf bin
