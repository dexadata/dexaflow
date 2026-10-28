# The contributor toolchain (#1574). Go comes from this base image, pinned by
# GO_VERSION, which scripts/check-go-toolchain-pin.sh reconciles with go.mod.
# The Go tools, Hugo and k3d are installed by post-create.sh from the pins the
# Makefile and the workflows already carry, so this file holds no second copy
# of them.
ARG GO_VERSION=1.26.10
FROM golang:${GO_VERSION}-bookworm

# python3 is 3.11 on bookworm, the version the parser CI runs first.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates curl git jq make shellcheck sudo \
		python3 python3-pip python3-venv \
	&& rm -rf /var/lib/apt/lists/*
