.PHONY: install dev backend frontend scan quota test build verify

install:
	cd backend && go mod download
	cd frontend && npm ci

dev:
	$(MAKE) -j2 backend frontend

backend:
	cd backend && go run ./cmd/dora serve

frontend:
	cd frontend && npm run dev

scan:
	cd backend && go run ./cmd/dora scan

quota:
	cd backend && go run ./cmd/dora quota refresh

test:
	cd backend && go test ./...

build:
	cd frontend && npm run build

verify: test build
