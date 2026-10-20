.PHONY: test lint build

build:
	go build -o bin/chronicle ./cmd/chronicle

test:
	go test ./...

lint:
	go vet ./...
	staticcheck ./...
