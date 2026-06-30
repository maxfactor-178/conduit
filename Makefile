# Conduit — build & dev tasks
BINARY := conduit
PKG    := ./cmd/conduit

.PHONY: build run test vet check clean tidy

## build: compile the conduit binary into ./bin
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(BINARY) $(PKG)

## run: run against the dev config (expects dockerized ejabberd)
run:
	go run $(PKG) -config config/config.dev.yaml

## test: run the full test suite
test:
	go test ./...

## vet: static analysis
vet:
	go vet ./...

## check: vet + test (use this before pushing)
check: vet test

## tidy: prune and verify go.mod/go.sum
tidy:
	go mod tidy

## clean: remove build artifacts
clean:
	rm -rf bin
