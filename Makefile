.PHONY: install dev backend frontend scan quota test build verify

FRONTEND_DIST := frontend/dist
WEB_ASSET_STAGE := backend/internal/webassets/dist
BINARY := bin/dora
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || printf unknown)
GIT_SHORT_COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
GIT_TAG := $(shell git describe --tags --exact-match HEAD 2>/dev/null)
GIT_DIRTY := $(shell test -z "$$(git status --porcelain --untracked-files=normal 2>/dev/null)" || printf '%s' -dirty)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
ifeq ($(strip $(GIT_TAG)),)
BUILD_VERSION := dev+$(GIT_SHORT_COMMIT)$(GIT_DIRTY)
else ifneq ($(strip $(GIT_DIRTY)),)
BUILD_VERSION := dev+$(GIT_SHORT_COMMIT)$(GIT_DIRTY)
else
BUILD_VERSION := $(GIT_TAG)
endif
BUILD_LDFLAGS := -X github.com/wubh576/dora/backend/internal/buildinfo.version=$(BUILD_VERSION) -X github.com/wubh576/dora/backend/internal/buildinfo.commit=$(GIT_COMMIT) -X github.com/wubh576/dora/backend/internal/buildinfo.buildTime=$(BUILD_TIME)

install:
	go -C backend mod download
	npm --prefix frontend ci

dev:
	$(MAKE) -j2 backend frontend

backend:
	go -C backend run -ldflags "$(BUILD_LDFLAGS)" ./cmd/dora serve

frontend:
	npm --prefix frontend run dev

scan:
	go -C backend run ./cmd/dora scan

quota:
	go -C backend run ./cmd/dora quota refresh

test:
	go -C backend test ./...

build:
	rm -rf $(FRONTEND_DIST) $(WEB_ASSET_STAGE)
	rm -f $(BINARY)
	npm --prefix frontend run build
	@set -e; \
		trap 'rm -rf "$(CURDIR)/$(WEB_ASSET_STAGE)"' EXIT; \
		mkdir -p $(WEB_ASSET_STAGE) bin; \
		cp -R $(FRONTEND_DIST)/. $(WEB_ASSET_STAGE)/; \
		go -C backend build -tags production -ldflags "$(BUILD_LDFLAGS)" -o ../$(BINARY) ./cmd/dora

verify: test build
