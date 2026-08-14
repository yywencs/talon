SHELL := /bin/sh

BINARY ?= bin/talon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev')
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf 'unknown')
AGENT_VERSION := talon-toolops-agent/$(VERSION)
BUILDINFO_PACKAGE := github.com/wen/opentalon/internal/buildinfo
LDFLAGS := -X $(BUILDINFO_PACKAGE).AgentVersion=$(AGENT_VERSION) -X $(BUILDINFO_PACKAGE).Commit=$(COMMIT)

.PHONY: build version

build:
	mkdir -p $(dir $(BINARY))
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/talon

version: build
	./$(BINARY) --version
