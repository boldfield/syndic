.PHONY: build check test lint probe

build:
	go build ./...

check: build
	go vet ./...
	go test ./...

test:
	go test ./...

lint:
	golangci-lint run ./...

probe:
	go run ./cmd/syndic probe "$(ENDPOINT)"
