.PHONY: install dev backend frontend scan quota test native-test build verify

FRONTEND_DIST := frontend/dist
WEB_ASSET_STAGE := backend/internal/webassets/dist
BINARY := bin/dora
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || printf unknown)
GIT_DIRTY := $(if $(strip $(shell git status --porcelain --untracked-files=normal 2>/dev/null)),true,false)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
BUILD_LDFLAGS := -X github.com/wubh576/dora/backend/internal/buildinfo.commit=$(GIT_COMMIT) -X github.com/wubh576/dora/backend/internal/buildinfo.buildTime=$(BUILD_TIME) -X github.com/wubh576/dora/backend/internal/buildinfo.dirty=$(GIT_DIRTY)

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

native-test:
	@set -e; \
		pointer_test_binary=$$(mktemp /private/tmp/dora-pointer-monitor-test.XXXXXX); \
		settings_test_binary=$$(mktemp /private/tmp/dora-settings-window-test.XXXXXX); \
		icon_test_binary=$$(mktemp /private/tmp/dora-icon-button-test.XXXXXX); \
		trap 'rm -f "$$pointer_test_binary" "$$settings_test_binary" "$$icon_test_binary"' EXIT; \
		xcrun clang -fobjc-arc -Wall -Wextra -Werror -framework Cocoa \
			-I backend/internal/menubar \
			backend/internal/menubar/pointer_monitor_darwin.m \
			backend/internal/menubar/testdata/pointer_monitor_test.m \
			-o "$$pointer_test_binary"; \
		"$$pointer_test_binary"; \
		xcrun clang -fobjc-arc -Wall -Wextra -Werror -framework Cocoa \
			-I backend/internal/menubar \
			backend/internal/menubar/settings_window_darwin.m \
			backend/internal/menubar/testdata/settings_window_test.m \
			-o "$$settings_test_binary"; \
		"$$settings_test_binary"; \
		xcrun clang -fobjc-arc -Wall -Wextra -Werror -framework Cocoa \
			-I backend/internal/menubar \
			backend/internal/menubar/bridge_darwin.m \
			backend/internal/menubar/pointer_monitor_darwin.m \
			backend/internal/menubar/settings_window_darwin.m \
			backend/internal/menubar/testdata/icon_button_test.m \
			-o "$$icon_test_binary"; \
		"$$icon_test_binary"

build:
	rm -rf $(FRONTEND_DIST) $(WEB_ASSET_STAGE)
	rm -f $(BINARY)
	npm --prefix frontend run build
	@set -e; \
		trap 'rm -rf "$(CURDIR)/$(WEB_ASSET_STAGE)"' EXIT; \
		mkdir -p $(WEB_ASSET_STAGE) bin; \
		cp -R $(FRONTEND_DIST)/. $(WEB_ASSET_STAGE)/; \
		go -C backend build -tags production -ldflags "$(BUILD_LDFLAGS)" -o ../$(BINARY) ./cmd/dora

verify: test native-test build
