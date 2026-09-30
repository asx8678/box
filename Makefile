GO      ?= go
LDFLAGS := -s -w
PREFIX  ?= $(HOME)/.local
export CGO_ENABLED := 0

.PHONY: build build-linux install test test-linux vet golden clean

build:
	$(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/box .

# box runs on Linux (Ubuntu on WSL2 is the target); these are the release builds.
build-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/box-linux-amd64 .
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/box-linux-arm64 .

# Installs the binary built for this machine; run it on the Linux/WSL machine.
install: build
	install -d $(PREFIX)/bin
	install -m 755 bin/box $(PREFIX)/bin/box

# The quick checks: -short skips the tests that build box and run it.
test:
	$(GO) test -short ./...

# Everything, uncached; on Linux/WSL that includes the end-to-end tests
# with real bwrap.
test-linux:
	$(GO) test -count=1 ./...

vet:
	$(GO) vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

# Rewrite the sandbox golden files after an intended change; review the diff.
golden:
	$(GO) test ./internal/sandbox -run TestGolden -update

clean:
	rm -rf bin
