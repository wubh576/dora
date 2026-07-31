.PHONY: install dev backend frontend scan quota test build verify

FRONTEND_DIST := frontend/dist
WEB_ASSET_STAGE := backend/internal/webassets/dist
BINARY := bin/dora

install:
	go -C backend mod download
	npm --prefix frontend ci

dev:
	$(MAKE) -j2 backend frontend

backend:
	go -C backend run ./cmd/dora serve

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
		go -C backend build -tags production -o ../$(BINARY) ./cmd/dora

verify: test build
