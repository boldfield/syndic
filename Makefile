.PHONY: build test lint probe

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run ./...

probe:
	go run ./cmd/syndic probe "$(ENDPOINT)"
