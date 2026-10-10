# Chaos dogfood — Phase 2a (#231) — clean-by-construction runner.
#
# Phase 1's harness checks the host contract; this image makes the contract
# pass by construction: a fresh Python 3.12 + Go toolchain + golangci-lint
# with no Dexaflow state on disk and no leoflow_parser in site-packages.
#
# Image is ~700 MB; first build ~5 min, cached after. Re-pull strategy: the
# image tag includes the Go + golangci-lint versions so a toolchain bump
# busts the cache deterministically (see Makefile target).
#
# Usage: built via `make chaos-dogfood-docker` (don't `docker build` directly
#        — the Makefile passes the right build-args).

ARG GO_VERSION=1.26.9
ARG GOLANGCI_LINT_VERSION=v2.12.2

FROM python:3.12-slim-bookworm AS base

ARG GO_VERSION
ARG GOLANGCI_LINT_VERSION

# Toolchain prereqs:
#   - git: harness reads HEAD for the report
#   - curl: golangci-lint installer
#   - build-essential: cgo for the few deps that touch it
#   - ca-certificates: HTTPS verifies on the curl pulls
RUN apt-get update -qq \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Pinned Go toolchain. Architectures: amd64 and arm64 (the Lima target).
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) goarch=amd64 ;; \
        arm64) goarch=arm64 ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz; \
    ln -s /usr/local/go/bin/go /usr/local/bin/go; \
    ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
ENV GOPATH=/go
ENV PATH=/usr/local/go/bin:/go/bin:$PATH

# Pinned golangci-lint (matches .github/workflows/ci.yaml's
# GOLANGCI_LINT_VERSION — bumping one must bump the other; the Makefile
# build-args wire them together). Installed via `go install` rather than the
# upstream installer script because that script's checksum verification has
# been flaky on at least one v2.12.2 arm64 build; `go install` uses Go's
# module checksum database which is the same source of truth as our CI.
RUN GOBIN=/usr/local/bin go install \
        "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"

# Pre-install pytest + ruff + the parser-test runtime deps so the harness
# doesn't have to (otherwise each `pytest -q` first run installs them).
# Versions track parser/pyproject.toml's [dev] extra and
# parser/tests/test_*.py imports; bump when those bump.
RUN pip install --no-cache-dir \
        "pytest>=8.0,<10.0" \
        "pytest-cov>=5.0,<8.0" \
        "pytest-asyncio>=0.23,<2.0" \
        "ruff>=0.6.0,<0.16.0" \
        "pyyaml>=6.0,<7.0" \
        "jsonschema>=4.0,<5.0"

# The harness lives at /chaos; the repo mounts at /workspace at run time so
# rebuilds aren't needed for source changes.
WORKDIR /workspace
COPY run.sh /chaos/run.sh
RUN chmod +x /chaos/run.sh

# A dedicated non-root user keeps the container output owned by something
# sensible when bind-mounting from the host. The harness needs no privileges.
# `go test ./...` writes to the Go module cache + build cache; give the chaos
# user a writable GOPATH + GOCACHE under its own HOME rather than chowning
# the root-owned /go (which would bloat the image layer with thousands of
# duplicated module files).
RUN useradd -u 1000 -m chaos
USER chaos
ENV HOME=/home/chaos
ENV GOPATH=/home/chaos/go
ENV GOCACHE=/home/chaos/.cache/go-build
ENV GOMODCACHE=/home/chaos/go/pkg/mod

ENTRYPOINT ["/chaos/run.sh"]
