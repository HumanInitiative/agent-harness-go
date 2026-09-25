.PHONY: help setup hooks run build test test-race cover fmt vet lint swagger swagger-check tools tidy check-conventions ci docker-build clean

BINARY       := bin/harness
SWAG_VERSION := v1.16.6
SWAG         := $(shell go env GOPATH)/bin/swag
SWAGGER_DIR  := api/swagger

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

## setup: one-time setup for a fresh clone (git hooks + tools)
setup: hooks tools

## hooks: use the repository's git hooks (commit message + pre-push checks)
hooks:
	git config core.hooksPath .githooks
	@echo "git hooks installed from .githooks/"

## run: start the harness locally (export the variables from .env first)
run:
	go run ./cmd/harness

## build: compile a static binary into bin/
build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/harness

## test: run all unit tests
test:
	go test ./...

## test-race: run all tests with the race detector
test-race:
	go test -race -count=1 ./...

## cover: run tests and print per-package coverage
cover:
	go test -cover ./...

## fmt: format all Go source files
fmt:
	gofmt -l -w .

## vet: run go vet
vet:
	go vet ./...

## lint: formatting check + vet (fails on unformatted files)
lint: vet
	@test -z "$$(gofmt -l .)" || (echo "unformatted files:"; gofmt -l .; exit 1)

## tools: install the pinned swag CLI (matches the swag library in go.mod)
tools:
	go install github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION)

## swagger: regenerate api/swagger from the @-annotations
swagger:
	$(SWAG) init --generalInfo cmd/harness/main.go --output $(SWAGGER_DIR) --outputTypes go,json,yaml --parseInternal
	$(SWAG) fmt --dir cmd,internal/adapters/inbound/httpapi

## swagger-check: fail if the generated spec or annotation formatting is stale
swagger-check: swagger
	@git diff --exit-code -- $(SWAGGER_DIR) cmd internal/adapters/inbound/httpapi \
		|| (echo "Swagger output is stale: run 'make swagger' and commit the result"; exit 1)

## tidy: sync go.mod/go.sum with imports
tidy:
	go mod tidy

## check-conventions: check this branch's name and commits against origin/main
check-conventions:
	@scripts/check-branch-name.sh "$$(git symbolic-ref --short HEAD)"
	@scripts/check-commits.sh origin/main HEAD

## ci: run locally what CI runs on every pull request
ci: lint test-race swagger-check build

## docker-build: build the production container image
docker-build:
	docker build -t agent-harness-go:local .

## clean: remove build artifacts
clean:
	rm -rf bin/
