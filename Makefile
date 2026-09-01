VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
SOURCE_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PCR_LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(SOURCE_COMMIT) -X main.buildDate=$(BUILD_DATE)"

.DEFAULT_GOAL := build

.PHONY: build clean test test-short coverage lint fmt run vet audit smoke smoke-docker seed-demo

build:
	mkdir -p bin
	go build -o bin/pcr-server ./cmd/server
	go build $(PCR_LDFLAGS) -o bin/pcr ./cmd/pcr

clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

test:
	go test -race -cover ./...

test-short:
	go test -short ./...

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	open coverage.html

lint:
	golangci-lint run --build-tags=integration ./...

fmt:
	gofmt -w .
	goimports -w .

run:
	go run ./cmd/server

vet:
	go vet ./...

audit:
	go vet ./...
	govulncheck ./...

# Smoke / integration tests against a running pcr-server. Two flavours:
#   make smoke         spawns an ephemeral local server on :18080
#   make smoke-docker  hits whatever is on :8080 (e.g. `make docker-compose-up` first)
# Override SMOKE_TOKEN to match PCR_API_TOKENS on the target.
SMOKE_TOKEN ?= smoke-token-abc
SMOKE_DATABASE_URL ?= $(PCR_DATABASE_URL)
SMOKE_DOCKER_URL ?= http://localhost:8080
SMOKE_DOCKER_TOKEN ?= changeme
DEMO_URL ?= http://127.0.0.1:18082
DEMO_TOKEN ?= demo-token

smoke:
	go run ./cmd/smoke --start-local --token=$(SMOKE_TOKEN) --database-url='$(SMOKE_DATABASE_URL)'

smoke-docker:
	go run ./cmd/smoke --base-url=$(SMOKE_DOCKER_URL) --token=$(SMOKE_DOCKER_TOKEN)

seed-demo:
	go run ./cmd/seed --base-url=$(DEMO_URL) --token=$(DEMO_TOKEN) --fixture=testdata/functional/phosphor-demo.json

# Docker targets
.PHONY: docker-build docker-run docker-compose-up docker-compose-down

docker-build:
	docker build -t pcr-server .

docker-run: docker-build
	@test -n "$${PCR_DATABASE_URL}" || (echo "PCR_DATABASE_URL is required" >&2; exit 2)
	docker run --rm -p 8080:8080 \
		-e PCR_API_TOKENS=$${PCR_API_TOKENS:-changeme} \
		-e PCR_SESSION_SECRET=$${PCR_SESSION_SECRET:-dev-default-please-override-in-production} \
		-e PCR_DATABASE_URL \
		-e PCR_COOKIE_SECURE=false \
		-e PCR_HUMAN_AUTH_PROVIDER=$${PCR_HUMAN_AUTH_PROVIDER:-github} \
		-e PCR_PUBLIC_URL=$${PCR_PUBLIC_URL:-http://localhost:8080} \
		-e PCR_OAUTH_CLIENT_ID=$${PCR_OAUTH_CLIENT_ID:-replace-me} \
		-e PCR_OAUTH_CLIENT_SECRET=$${PCR_OAUTH_CLIENT_SECRET:-replace-me} \
		-e PCR_OIDC_ISSUER_URL=$${PCR_OIDC_ISSUER_URL:-} \
		-e PCR_HUMAN_AUTH_ALLOW_ANY=$${PCR_HUMAN_AUTH_ALLOW_ANY:-true} \
		pcr-server

docker-compose-up:
	docker compose up --build

docker-compose-down:
	docker compose down
