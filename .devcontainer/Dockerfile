# Development image for the devcontainer (#1574). Built from the repository
# root (see docker-compose.yml) so the installer can read every version from
# the file that already pins it; nothing in this file names a tool version.
FROM mcr.microsoft.com/devcontainers/base:1-bookworm

# Python 3.11 is Debian bookworm's python3 and the version CI tests the parser
# with (ci.yaml PYTHON_VERSION). The venv keeps `make setup`'s pip installs off
# the system interpreter, which Debian marks externally managed. jq and the
# PostgreSQL client are what the e2e scripts and `make ci-local` call.
RUN apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		build-essential ca-certificates curl git jq make \
		postgresql-client python3 python3-pip python3-venv \
	&& rm -rf /var/lib/apt/lists/* \
	&& python3 -m venv /opt/venv \
	&& chown -R vscode:vscode /opt/venv

COPY go.mod /tmp/pins/go.mod
COPY .github/workflows/website-build.yml .github/workflows/helm-ci.yaml .github/workflows/ci.yaml /tmp/pins/.github/workflows/
COPY .devcontainer/install-tools.sh /tmp/pins/.devcontainer/install-tools.sh
RUN bash /tmp/pins/.devcontainer/install-tools.sh && rm -rf /tmp/pins

ENV PATH=/opt/venv/bin:/usr/local/go/bin:/home/vscode/go/bin:${PATH} \
	VIRTUAL_ENV=/opt/venv \
	GOTOOLCHAIN=local
