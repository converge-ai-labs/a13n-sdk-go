.DEFAULT_GOAL := help
.PHONY: help install generate generated-check format lint typecheck test build check check-all

help:
	@echo 'install | generate | generated-check | format | check | test | build | check-all'

install:
	uv sync --locked
	go mod download

generate:
	bash scripts/sync-contract.sh --check
	uv run --locked python codegen/generate.py

generated-check:
	bash scripts/sync-contract.sh --check
	uv run --locked python codegen/generate.py --check

format:
	gofmt -w *.go generated/*.go
	uv run --locked ruff check --fix .
	uv run --locked ruff format .

lint:
	@test -z "$$(gofmt -l *.go generated/*.go)" || { gofmt -l *.go generated/*.go; exit 1; }
	go vet ./...
	go mod verify
	uv lock --check
	uv run --locked ruff check --no-fix .
	uv run --locked ruff format --check .

typecheck:
	uv run --locked pyright

test:
	go test -race ./...
	uv run --locked python -m pytest

build:
	go build ./...

check: lint typecheck

check-all: install generated-check check test build
