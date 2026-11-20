.PHONY: build check test lint probe fmt-check

# GOLANGCI_LINT_VERSION must match the version: given to golangci-lint-action in .github/workflows/ci.yml
GOLANGCI_LINT_VERSION := v2.15.0

build:
	go build ./...

fmt-check:
	@if [ -n "$$(gofmt -l .)" ]; then echo "Formatting issues found:"; gofmt -l .; exit 1; fi

check: fmt-check build lint
	go vet ./...
	go test ./...

test:
	go test ./...

lint: fmt-check
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

probe:
	go run ./cmd/syndic probe --endpoint "$(ENDPOINT)" --model "$(MODEL)"
