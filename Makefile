# Developer shortcuts. Without make (e.g. on Windows), run the same commands
# by hand, or deploy/deploy.sh from Git Bash.
VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PI ?= pi

.PHONY: all test build pi sim deploy

all: test build

test:
	go vet ./...
	go test ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

build:
	go build -ldflags="-X main.version=$(VERSION)" -o bin/ ./cmd/...

# Binaries for the Raspberry Pi (Go is not installed there).
pi:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/ ./cmd/...

# The whole service against the simulated piano: http://127.0.0.1:8080
sim:
	go run ./cmd/sepianod -simulate -addr 127.0.0.1:8080 -data ./devdata

# Build, copy and install sepianod on the Pi (ssh alias $(PI)).
deploy:
	deploy/deploy.sh $(PI)
