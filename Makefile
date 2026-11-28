.PHONY: build check test lint probe fmt-check

# GOLANGCI_LINT_VERSION must match the version: given to golangci-lint-action in .github/workflows/ci.yml
GOLANGCI_LINT_VERSION := v2.15.0
# Per-package go test timeout. The internal/engine suite takes about 87s alone on an idle
# machine, but on a host shared by many agents it is starved of CPU and has exceeded 15m. The
# bound exists to catch a hang, so it sits well above the loaded-host run time.
GO_TEST_TIMEOUT ?= 30m
# Optional number of packages tested at once (go test -p). Empty leaves the go default.
GO_TEST_P ?=
GO_TEST_P_FLAG = $(if $(GO_TEST_P),-p $(GO_TEST_P) )

build:
	go build ./...

fmt-check:
	@if [ -n "$$(gofmt -l .)" ]; then echo "Formatting issues found:"; gofmt -l .; exit 1; fi

check: fmt-check build lint
	go vet ./...
	go test -timeout $(GO_TEST_TIMEOUT) $(GO_TEST_P_FLAG)./...

test:
	go test -timeout $(GO_TEST_TIMEOUT) $(GO_TEST_P_FLAG)./...

lint: fmt-check
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

probe:
	go run ./cmd/syndic probe --endpoint "$(ENDPOINT)" --model "$(MODEL)"
