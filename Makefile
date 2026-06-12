APP := agentbox
PKG := ./...

# Version metadata embedded into the binary (see cmd/agentbox/main.go).
# Falls back to "dev" when git metadata is unavailable (e.g. source tarballs).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Release targets: GOOS/GOARCH pairs cross-compiled by `make release`.
PLATFORMS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

# Embedded egress-proxy artifacts (linux-only: containers are linux). Built by
# `make proxy` BEFORE the main binary so go:embed picks them up; this is what
# makes network.mode=allowlist enforceable from a single agentbox binary.
PROXY_DIR := internal/netproxy/embedded

.PHONY: fmt test build lint proxy release clean

fmt:
	go fmt $(PKG)

test:
	go test $(PKG)

proxy:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(PROXY_DIR)/netproxy_linux_amd64 ./cmd/netproxy
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o $(PROXY_DIR)/netproxy_linux_arm64 ./cmd/netproxy

build: proxy
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/agentbox

lint:
	go vet $(PKG)

# Cross-compile static binaries for every supported platform into dist/
# and write a SHA-256 checksum manifest alongside them.
release: proxy
	rm -rf dist
	mkdir -p dist
	$(foreach platform,$(PLATFORMS),\
		CGO_ENABLED=0 GOOS=$(word 1,$(subst /, ,$(platform))) GOARCH=$(word 2,$(subst /, ,$(platform))) \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o dist/$(APP)_$(word 1,$(subst /, ,$(platform)))_$(word 2,$(subst /, ,$(platform))) ./cmd/agentbox &&) true
	cd dist && shasum -a 256 $(APP)_* > checksums.txt

clean:
	rm -rf bin dist $(PROXY_DIR)/netproxy_linux_*
