# Dexaflow task base image.
#
# A DAG image is built FROM this base (ADR 0003): it provides the leoflow-agent
# (PID 1, talks gRPC to the control plane) and the leoflow_runtime Python helper
# (runs the user callable, bridges XCom). The DAG author adds their code and
# Python dependencies on top.
#
# Build from the repository root so both the Go source and runtime/python are in
# context:
#   docker build -f runtime/Dockerfile \
#     --build-arg PYTHON_VERSION=3.11 -t leoflow-base:py3.11 .
#
# PYTHON_VERSION selects the interpreter (3.10 / 3.11 / 3.12 / 3.13). The list
# is not authoritative here: internal/domain/schemas/leoflow-yaml-schema.json's
# python_version enum is, and scripts/check-python-runtime-matrix.sh fails this
# comment when it falls behind. 3.10 is deprecated (upstream EOL 2026-10-31).

# GO_VERSION is not authoritative here either: go.mod's `toolchain` line is the
# source of truth for the toolchain, and scripts/check-go-toolchain-pin.sh fails
# this default when it falls behind (#1036).
ARG GO_VERSION=1.26.9
ARG PYTHON_VERSION=3.11

# ── Stage 1: build the static agent binary ───────────────────────────────────
# Deliberately still bookworm while stage 2 is trixie — do not "fix" the
# inconsistency without re-reading this. Nothing from this stage reaches a task
# pod: the only thing copied out is /out/leoflow-agent, built CGO_ENABLED=0, so
# it links no glibc and no OpenSSL from here. Go does not use OpenSSL at all
# (crypto/tls is pure Go), and crypto/x509 reads the FINAL image's trust store,
# not this one's. So this stage's suite changes the build environment only,
# never what a user runs — a different risk class from stage 2, and a change
# worth making on its own evidence rather than as a rider on a user-facing one.
#
# What keeps it current is the Go pin, not Debian's support calendar. GO_VERSION
# is pinned to a PATCH release, so this image is whatever docker-library/golang
# published for that exact tag: a Debian security update never reaches it in
# place, and Debian 12's remaining LTS window buys it nothing. Every bump of
# GO_VERSION pulls a freshly built image with a current package set — that is
# the whole refresh mechanism, and it runs on the Go release cadence. The move
# off bookworm therefore becomes forced, not optional, on the first GO_VERSION
# bump after upstream stops publishing a `-bookworm` variant.
FROM golang:${GO_VERSION}-bookworm AS agent-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Stamp the agent with version/commit/date, exactly as the release binaries are
# (Makefile LDFLAGS). Without this the baked agent reports empty version/commit,
# so `leoflow-agent version` in a task pod is untraceable to a commit and a
# control-plane↔agent version skew is undiagnosable. Defaults keep a local/source
# build meaningful; the release passes the real values as build-args.
ARG VERSION=dev
ARG GIT_COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
	-ldflags="-s -w -X 'github.com/dexadata/dexaflow/internal/version.version=${VERSION}' -X 'github.com/dexadata/dexaflow/internal/version.gitCommit=${GIT_COMMIT}' -X 'github.com/dexadata/dexaflow/internal/version.buildDate=${BUILD_DATE}'" \
	-o /out/dexaflow-agent ./cmd/dexaflow-agent

# ── Stage 2: runtime base ─────────────────────────────────────────────────────
# Debian 13 (trixie). The suite is pinned explicitly rather than left implicit
# so a base-image move is a reviewable diff, not a silent consequence of
# upstream retagging.
#
# The reason it is trixie and not bookworm is OpenSSL, not the Debian release
# calendar on its own. `python:3.x-slim` links CPython's `ssl` module against
# the SYSTEM OpenSSL, so every TLS call a task makes — every provider hitting an
# API, every `requests` call, every warehouse driver — terminates in this
# image's libssl. bookworm ships OpenSSL 3.0.x, whose UPSTREAM support ended
# 2026-09-07; trixie ships 3.5.x, supported to 2030-04-08. Debian 12 also left
# regular security support on 2026-06-11 and is now oldstable under the LTS
# team's narrower, best-effort scope, while Debian 13 has full Security Team
# support to 2028-08-09.
#
# Note that this suite is also what a user's `system_packages:` resolves
# against: the compiler emits `apt-get install` into the generated DAG image
# (internal/cli/compile_build.go), so the package versions a DAG gets move with
# this line. That makes it a user-visible change, not an internal one.
FROM python:${PYTHON_VERSION}-slim-trixie

LABEL org.opencontainers.image.source="https://github.com/dexadata/dexaflow"
LABEL org.opencontainers.image.description="Dexaflow task base image (agent + Python runtime)"
LABEL org.opencontainers.image.licenses="Apache-2.0"

# Run as a NUMERIC non-root UID (not a login name). A PodSecurity `restricted`
# namespace enforces runAsNonRoot by verifying the image's user is non-root, and
# the kubelet can only verify a numeric UID — a NAME like `leoflow` is
# unresolvable, so the container is rejected even though the user is not root.
# 65532 is the de-facto "nonroot" UID from Google's distroless images, matching
# the UID the Helm chart pins for the control-plane and migration pods. The home
# dir is created and owned by this UID so runtime writes to it (e.g. __pycache__)
# succeed; the staging PVC is made group-writable at the pod level via fsGroup.
#
# The trailing chmod pins the home-dir mode instead of inheriting it from
# /etc/login.defs. Debian's `passwd` went 1:4.13 (bookworm) → 1:4.17.4 (trixie)
# and the HOME_MODE default tightened with it: the identical `useradd` line
# produced 0755 on bookworm and produces 0700 on trixie. Nothing supported
# breaks at 0700 — a task pod runs as this very UID (buildSecurityContext sets
# runAsNonRoot with no runAsUser, so the kubelet uses the image's 65532) and the
# owner traverses its own home regardless — so the tighter mode is kept. What is
# not kept is the silence: a filesystem permission that moves because a distro
# default moved is the kind of drift that surfaces as an unexplained EACCES
# three releases later, so it is now stated here and independent of the suite.
RUN groupadd --gid 65532 leoflow \
 && useradd --create-home --home-dir /home/leoflow --uid 65532 --gid 65532 leoflow \
 && chmod 0700 /home/leoflow

COPY --from=agent-build /out/dexaflow-agent /usr/local/bin/dexaflow-agent
# Pre-rename name, kept as a link: control planes and DAG images from before the
# rename launch the agent as `leoflow-agent`, and executor.agent_path defaults to it.
RUN ln -s dexaflow-agent /usr/local/bin/leoflow-agent
COPY runtime/python /opt/leoflow-runtime
RUN pip install --no-cache-dir /opt/leoflow-runtime

# Dexaflow DAGs are authored with the Apache Airflow Task SDK (`from airflow.sdk
# import DAG, task`), so the task container must be able to import it to load the
# user's dag.py. This is the lightweight Task SDK, not airflow-core, pinned to
# the 3.3.x line Dexaflow targets (ADR 0003 / parser SDK version).
ARG AIRFLOW_TASK_SDK_VERSION=1.3.1
RUN pip install --no-cache-dir "apache-airflow-task-sdk==${AIRFLOW_TASK_SDK_VERSION}"

# setuptools is upgraded ONLY where the base image already ships it. The
# python:3.10 and 3.11 slim images preinstall setuptools 79.0.1, whose vendored
# jaraco.context 5.3.0 (CVE-2026-23949) and wheel 0.45.1 (CVE-2026-24049) are
# fixable HIGH findings in the daily image scan (#1299). 80.10.2 is the first
# release vendoring the fixed versions of both (jaraco.context 6.1.0, wheel
# 0.46.3), and it stays on the 80.x line on purpose: setuptools 82 removed
# pkg_resources, which user DAG code on these lines may still import. One
# visible change comes with it: 79.0.1 warns about importing pkg_resources with a
# DeprecationWarning, hidden by default when a library does the import, while
# 80.10.2 raises a UserWarning, which is shown by default. A task whose
# dependencies import pkg_resources gains that warning line in its log, and a
# filter that turns UserWarning into an error now trips on it. The 3.12
# and 3.13 images ship no setuptools at all, and installing it there would add
# a package (and its findings) that the image does not have today, hence the
# guard rather than an unconditional install. Run after the Task SDK install so
# no later resolver step can move it back.
ARG SETUPTOOLS_VERSION=80.10.2
RUN if pip show --quiet setuptools >/dev/null 2>&1; then \
      pip install --no-cache-dir "setuptools==${SETUPTOOLS_VERSION}"; \
    fi

# dbt defaults its writable artifacts (target/, logs/, profiles.yml) to the project
# directory, but a compiled DAG image COPYs the project read-only (root-owned), so
# dbt would explode writing there. Point them at an ephemeral, always-writable /tmp
# dir instead — keeps the project read-only (a stronger posture) and lets the chart
# run task pods with readOnlyRootFilesystem + a /tmp emptyDir. Harmless for non-dbt
# tasks (they never read these). See #852.
ENV DBT_PROFILES_DIR=/tmp/leoflow/dbt \
    DBT_TARGET_PATH=/tmp/leoflow/dbt/target \
    DBT_LOG_PATH=/tmp/leoflow/dbt/logs

USER 65532:65532
WORKDIR /home/leoflow

# The agent is PID 1; it fetches the task spec and runs the user code.
ENTRYPOINT ["dexaflow-agent"]
