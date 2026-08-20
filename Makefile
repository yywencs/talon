SHELL := /bin/sh

BINARY ?= bin/talon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev')
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf 'unknown')
AGENT_VERSION := talon-toolops-agent/$(VERSION)
BUILDINFO_PACKAGE := github.com/wen/opentalon/internal/buildinfo
LDFLAGS := -X $(BUILDINFO_PACKAGE).AgentVersion=$(AGENT_VERSION) -X $(BUILDINFO_PACKAGE).Commit=$(COMMIT)

ifeq ($(origin EVAL_VERSION), undefined)
EVAL_VERSION := eval-$(shell date -u +%Y%m%dT%H%M%SZ)-$(COMMIT)
endif
EVAL_AGENT_BINARY ?= bin/talon-eval
EVAL_EXPORT_BINARY ?= bin/talon-export-eval
EVAL_DATA_ROOT ?= data
EVAL_DATASET ?= toolops-v1
EVAL_REPEAT ?= 3
EVAL_TIMEOUT ?= 15m
EVAL_MAX_STEPS ?= 24
EVAL_ENV_FILE ?= .env
EVAL_JUDGE ?= 0
EVAL_PARALLEL ?= 1
EVAL_JUDGE_CONCURRENCY ?= 1
EVAL_OUTPUT_DIR ?=
EVAL_AGENT_VERSION := talon-toolops-agent/$(EVAL_VERSION)
EVAL_LDFLAGS := -X $(BUILDINFO_PACKAGE).AgentVersion=$(EVAL_AGENT_VERSION) -X $(BUILDINFO_PACKAGE).Commit=$(COMMIT)

.PHONY: build version build-eval-binaries test-evaluator eval-local eval-full eval-baseline

build:
	mkdir -p $(dir $(BINARY))
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/talon

version: build
	./$(BINARY) --version

build-eval-binaries:
	mkdir -p $(dir $(EVAL_AGENT_BINARY)) $(dir $(EVAL_EXPORT_BINARY))
	go build -trimpath -ldflags "$(EVAL_LDFLAGS)" -o $(EVAL_AGENT_BINARY) ./cmd/talon
	go build -trimpath -o $(EVAL_EXPORT_BINARY) ./cmd/talon-export

test-evaluator:
	PYTHONPATH=evaluator/src python3 -m unittest discover -s evaluator/tests -v

eval-local:
	@test -n "$(EVAL_INPUT)" || (echo "EVAL_INPUT is required" >&2; exit 2)
	PYTHONPATH=evaluator/src python3 -m talon_evaluator "$(EVAL_INPUT)" $(if $(EVAL_OUTPUT),--output "$(EVAL_OUTPUT)",) --pretty

eval-full:
	@test -n "$(EVAL_INPUT)" || (echo "EVAL_INPUT is required" >&2; exit 2)
	PYTHONPATH=evaluator/src python3 -m talon_evaluator "$(EVAL_INPUT)" $(if $(EVAL_OUTPUT),--output "$(EVAL_OUTPUT)",) --pretty --judge

eval-baseline: build-eval-binaries
	EVAL_AGENT_BINARY="$(EVAL_AGENT_BINARY)" \
	EVAL_EXPORT_BINARY="$(EVAL_EXPORT_BINARY)" \
	EVAL_DATA_ROOT="$(EVAL_DATA_ROOT)" \
	EVAL_DATASET="$(EVAL_DATASET)" \
	EVAL_VERSION="$(EVAL_VERSION)" \
	EVAL_REPEAT="$(EVAL_REPEAT)" \
	EVAL_TIMEOUT="$(EVAL_TIMEOUT)" \
	EVAL_MAX_STEPS="$(EVAL_MAX_STEPS)" \
	EVAL_ENV_FILE="$(EVAL_ENV_FILE)" \
	EVAL_JUDGE="$(EVAL_JUDGE)" \
	EVAL_PARALLEL="$(EVAL_PARALLEL)" \
	EVAL_JUDGE_CONCURRENCY="$(EVAL_JUDGE_CONCURRENCY)" \
	EVAL_OUTPUT_DIR="$(EVAL_OUTPUT_DIR)" \
	./scripts/eval-baseline.sh
