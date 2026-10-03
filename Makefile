GO      ?= go
BIN     := bin/server

.PHONY: build run test test-go test-js lint fmt docker

build:
	$(GO) build -o $(BIN) ./cmd/server

run: build
	./$(BIN)

test: test-go test-js

test-go:
	$(GO) test ./...

test-js:
	@if ls web/*.test.js >/dev/null 2>&1; then node --test web/*.test.js; else echo "no js tests yet"; fi

lint:
	$(GO) vet ./...
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt: files need formatting" && exit 1)

fmt:
	gofmt -w .

docker:
	docker build -f deploy/Dockerfile -t variantsvc:local .
