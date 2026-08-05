.PHONY: build deps lint test docker integration-test plugins install-plugins publish-aptly-repo install bump-captcha-protect

BINARY_NAME=sitectl
DOCS_PORT ?= 3000
INSTALL_DIR ?= $(or $(dir $(shell which $(BINARY_NAME) 2>/dev/null)),/usr/local/bin/)
SUPPORTED_PLUGIN_NAMES := archivesspace drupal isle ojs omeka-classic omeka-s wp
WORKSPACE_PLUGIN_DIRS := $(foreach name,$(SUPPORTED_PLUGIN_NAMES),$(wildcard ../sitectl-$(name)))

deps:
	go get .
	go mod tidy

build: deps plugins
	go build -o $(BINARY_NAME) .

install: build
	sudo cp $(BINARY_NAME) $(INSTALL_DIR)$(BINARY_NAME)
	$(MAKE) install-plugins

plugins:
	@set -e; for dir in $(WORKSPACE_PLUGIN_DIRS); do \
		echo "Building $${dir##*/}"; \
		$(MAKE) -C "$$dir" build; \
	done

install-plugins:
	@set -e; for dir in $(WORKSPACE_PLUGIN_DIRS); do \
		echo "Installing $${dir##*/}"; \
		$(MAKE) -C "$$dir" install; \
	done

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

bump-captcha-protect:
	bash ./scripts/bump-captcha-protect.sh
