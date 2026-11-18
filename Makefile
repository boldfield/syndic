.PHONY: build check test lint probe fmt-check

build:
	go build ./...

fmt-check:
	@if [ -n "$$(gofmt -l .)" ]; then echo "Formatting issues found:"; gofmt -l .; exit 1; fi

check: fmt-check build
	go vet ./...
	go test ./...

test:
	go test ./...

lint: fmt-check
	golangci-lint run ./...

probe:
	go run ./cmd/syndic probe "$(ENDPOINT)"
