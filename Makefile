.PHONY: build deps lint test docker integration-test plugins install-plugins publish-aptly-repo

BINARY_NAME=sitectl
DOCS_PORT ?= 3000

deps:
	go get .
	go mod tidy

build: deps
	go build -o $(BINARY_NAME) .

lint:
	go fmt ./...
	golangci-lint run

	@if command -v json5 > /dev/null 2>&1; then \
		echo "Running json5 validation on renovate.json5"; \
		json5 --validate renovate.json5 > /dev/null; \
	else \
		echo "json5 not found, skipping renovate validation"; \
	fi

test: build
	go test -v -race ./...

publish-aptly-repo:
	bash ./scripts/publish-aptly-repo.sh
