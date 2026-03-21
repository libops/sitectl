.PHONY: build deps lint test docker integration-test plugins install-plugins publish-aptly-repo install

BINARY_NAME=sitectl
DOCS_PORT ?= 3000
INSTALL_DIR ?= $(or $(dir $(shell which $(BINARY_NAME) 2>/dev/null)),/usr/local/bin/)

deps:
	go get .
	go mod tidy

build: deps
	go build -o $(BINARY_NAME) .

install: build
	sudo cp $(BINARY_NAME) $(INSTALL_DIR)$(BINARY_NAME)
	@if [ -d ../sitectl-isle ]; then $(MAKE) -C ../sitectl-isle install; fi
	@if [ -d ../sitectl-drupal ]; then $(MAKE) -C ../sitectl-drupal install; fi

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
