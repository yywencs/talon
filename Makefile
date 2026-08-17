SHELL := /bin/sh

BINARY ?= bin/talon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev')
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf 'unknown')
AGENT_VERSION := talon-toolops-agent/$(VERSION)
BUILDINFO_PACKAGE := github.com/wen/opentalon/internal/buildinfo
LDFLAGS := -X $(BUILDINFO_PACKAGE).AgentVersion=$(AGENT_VERSION) -X $(BUILDINFO_PACKAGE).Commit=$(COMMIT)

.PHONY: build version test-evaluator eval-local eval-full

build:
	mkdir -p $(dir $(BINARY))
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/talon

version: build
	./$(BINARY) --version

test-evaluator:
	PYTHONPATH=evaluator/src python3 -m unittest discover -s evaluator/tests -v

eval-local:
	@test -n "$(EVAL_INPUT)" || (echo "EVAL_INPUT is required" >&2; exit 2)
	PYTHONPATH=evaluator/src python3 -m talon_evaluator "$(EVAL_INPUT)" $(if $(EVAL_OUTPUT),--output "$(EVAL_OUTPUT)",) --pretty

eval-full:
	@test -n "$(EVAL_INPUT)" || (echo "EVAL_INPUT is required" >&2; exit 2)
	PYTHONPATH=evaluator/src python3 -m talon_evaluator "$(EVAL_INPUT)" $(if $(EVAL_OUTPUT),--output "$(EVAL_OUTPUT)",) --pretty --judge
