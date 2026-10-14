---
title: "ADR 0078: the dependency image is built from wheels without a daemon"
linkTitle: "0078 · Dependency image from wheels"
weight: 780
description: "ADR 0078: the dependency-layer path of ADR 0073. When the dependency image of a split build is missing and uv is on PATH, the CLI resolves the Python dependencies for each target platform with uv, installs the wheels on the host, packs them as one deterministic layer on the runtime base pinned by digest and pushes the dependency image through the registry API. The key covers the resolved set, so unpinned dependencies take the split too."
---

**Status:** Accepted
**Date:** 2026-10-14
**Relates:** ADR 0073 (daemonless image builds, sections 2 and 4), ADR 0077 (dependency image and code layer), ADR 0076 (reuse by content label), ADR 0075 (the registry is the source of the digest), ADR 0003 (a DAG version is a `dag.json` and an image)
**Issues:** #1706

## Context

ADR 0077 split a pushed build of the generated Dockerfile into a dependency
image, found in the registry by its key, and a code layer appended through
the registry API. When the dependency image is missing, the local daemon
still builds it, and only projects whose Python dependencies are all pinned
take the split, because the key covers the declared pins and not what pip
resolves.

Path 3 of ADR 0073 builds the dependency image with no daemon and no
emulation: the dependencies are installed as wheels for the target platform
on the host and added as a layer to the runtime base. This record decides
how, and where it departs from ADR 0073 section 4.

## Decision

1. **The dependency layer applies** when a build splits (ADR 0077 decision
   1), every platform is `linux/amd64` or `linux/arm64`, the project has no
   `system_packages`, every dependency is a requirement by name (no URL,
   path, VCS reference or inline option), the CLI does not run on Windows,
   `uv` is on `PATH` and finds a Python on the machine (`uv python find`; any
   version, uv needs one to run its resolver and installer, and the CLI runs
   it with `UV_PYTHON_DOWNLOADS=never`, so it never downloads one), and the
   base image is a Dexaflow runtime base for every platform: its config sets
   `PYTHON_VERSION` (X.Y.Z), it has `/usr/local/lib/pythonX.Y/site-packages`,
   its dpkg database lists glibc, and nothing in it tells pip where or what
   to install (decision 8). A `python_version` set in `dexaflow.yaml` must
   name the base's X.Y. Otherwise the daemon builds the dependency image as
   ADR 0077 says, and the builder line says why the dependency layer does
   not apply, starting with the cause.
2. **Resolution, per platform.** The CLI reads the base by digest, per
   platform image of its index, and lists the distributions of its
   site-packages from the `*.dist-info` names in the layer tar headers,
   applying the whiteouts of later layers. The listing is cached under the
   user cache directory by the platform image digest, so a redeploy reads no
   base layer. Then, in an empty directory:

   ```text
   uv pip compile --no-config --quiet --python-platform <arch>-manylinux_2_36 \
      --python-version X.Y.Z --only-binary :all: --generate-hashes --no-header \
      --no-annotate -c base-constraints.txt -o resolved.txt requirements.in
   ```

   `base-constraints.txt` pins every distribution of the base, so a
   dependency that needs another version of one fails the resolution, and
   uv's reason reaches the builder line. uv runs with only the variables that
   locate programs, the home and cache directories, proxies and certificate
   authorities (`UV_NATIVE_TLS` and `UV_SYSTEM_CERTS` among them); every
   other `UV_*` and `PIP_*` variable is dropped, and `--no-config` ignores the
   host's uv configuration, so the host never changes what ships. The index
   is PyPI.
3. **The glibc ceiling is `manylinux_2_36`.** Debian 12 ships glibc 2.36 and
   Debian 13, the runtime base today, 2.41, so a wheel tagged up to 2.36 runs
   on both. A base with an older glibc gets the newest tag uv knows at or
   under its version.
4. **The key (format 2)** is the SHA-256 of a canonical JSON document: the
   format, the installer and its version (`uv`, `uv 0.11.32`), the digest of
   the base index or image, Python X.Y.Z, and, for every platform sorted, its
   wheel tag and the resolved lines (`name==version` with the sorted
   `--hash` values, sorted by name), and the image configuration the
   dependency image sets (`PYTHONPATH` when the project is not dbt, the user).
   The tag stays `deps-<first 32 hex>`. Format 1 keys (ADR 0077) hash another
   document, so an image built from wheels and one the daemon built never
   share a tag.
5. **Unpinned dependencies take the split** when the dependency layer
   applies: the key covers what uv resolved, so a new release that satisfies
   a range gives a new key and a new dependency image. This lifts ADR 0077
   decision 5 for those builds. Without uv, or when the dependency layer
   does not apply, decision 5 holds as before.
6. **Install and layer.** The resolution minus what the base holds at the
   same version is installed with `uv pip install --target <dir>
   --python-platform ... --python-version X.Y.Z --only-binary :all:
   --require-hashes --no-deps`. Everything in the directory but `bin/` lands
   in `/usr/local/lib/pythonX.Y/site-packages/`, and `bin/*` in
   `/usr/local/bin/`. A script whose shebang names a Python gets
   `#!/usr/local/bin/pythonX.Y`, and the RECORD lines of the scripts point at
   `../../../bin/<name>` with the new hash and size, so `pip show -f` and `pip
   uninstall` work in the image. The layer is deterministic as the code layer
   is (ADR 0077 decision 6): sorted, owned by root, dated 1970-01-01, modes
   normalized. Only the directories created under site-packages and the files
   are entries, never `usr`, `usr/local` or site-packages itself, which the
   base holds with its own modes; uv's lock file is left out.
7. **The dependency image** is the base for every requested platform (other
   platforms and attestation manifests of the base index dropped) with the
   layer appended, `PYTHONPATH=/home/leoflow` set unless the project is dbt,
   and the user `65532:65532` when there are dependencies, as the generated
   Dockerfile sets them. It is pushed as an index when the base is one. The
   code layer is then appended as ADR 0077 decisions 3 and 7 say. The builder
   line says `dependency-layer (uv <version>), then code-layer`, and how long
   the resolution took when that was a second or more.
8. **A base that configures pip declines.** The daemon's `pip install` in
   the generated Dockerfile honors the pip configuration of the base: a
   `PIP_*` variable of its config that changes where or what pip installs
   (`PIP_INDEX_URL`, `PIP_EXTRA_INDEX_URL`, `PIP_FIND_LINKS`,
   `PIP_CONSTRAINT`, ..., every `PIP_*` but a short list that only changes
   how pip runs, such as `PIP_NO_CACHE_DIR`), `XDG_CONFIG_DIRS` or
   `XDG_CONFIG_HOME`, and a `pip.conf` (or `pip.ini`) in any place pip reads
   one (`/etc/pip.conf`, `/etc/xdg/pip/pip.conf`, `/usr/local/pip.conf`,
   `/root/.config/pip/pip.conf`, `/root/.pip/pip.conf`; any file of that
   name outside site-packages counts). The dependency layer resolves from
   PyPI and would ship packages of the same name from another index, so the
   base listing records these and the dependency layer declines such a base.
   A `uv.toml` in the image changes nothing: uv runs on the host, never in
   the image, and with `--no-config`.
9. **When the wheels fail after the decision** (the install or the push of
   the dependency image fails, before the DAG image is written) and the
   dependencies are all pinned (the format 1 key of ADR 0077 cannot cover a
   range), the dependency image under that key is used: the one the
   registry already holds, or one the daemon builds when its tool is on
   `PATH`. The output says why. Otherwise, and when the build was canceled,
   the build fails with uv's error.

## Where this departs from ADR 0073 section 4

- **Locks are not read yet.** Every build resolves; a project with a lock
  gets no faster path.
- **uv only.** uv resolves and installs for a target platform and Python
  version other than those of the Python it runs with, fast enough to run
  on every build; an installer choice is left for later.
- **The glibc tag is a ceiling** (decision 3), not the exact glibc of the
  base, so one wheel set serves the Debian 12 and Debian 13 bases.
- **No bytecode.** uv compiles nothing, and the host's Python is another
  version. Site-packages is owned by root, so the task process compiles every
  module in memory on first import, in every process. Measured on
  2026-10-14 with CPython 3.11.9, importing `pandas`, `requests`,
  `sqlalchemy` and `black` (20 distributions, 4,561 files) took 1.9 to 2.1 s
  without bytecode against 0.65 to 0.74 s with the bytecode pip writes.
- **One key covers the list of platforms**, with a resolution per platform
  inside it, because the platforms are pushed under one tag as one index.
- **No private index, local or VCS dependency yet.** They decline to the
  daemon, which is why the index URL is not in the key.
- **Data files of a wheel** (`.data/headers`, `.data/data`) land under
  site-packages, where `--target` puts them, instead of `/usr/local/include`
  and `/usr/local/share`. On the measured set the file lists of a pip
  install into `/usr/local` and of this layer were the same but for one C
  header of `greenlet`.
- **What else differs from pip in the image:** `INSTALLER` says `uv`, every
  distribution has a `REQUESTED` file (all are installed by name with
  `--no-deps`), the console scripts are uv's (their shebang is rewritten to
  `/usr/local/bin/pythonX.Y`, so their hashes differ from pip's), and the
  data files and headers are under site-packages as said above.

## Consequences

- The first build of a key needs no daemon either: a deploy from a machine
  or CI runner without docker builds and pushes everything, given uv, a
  Python on that machine and PyPI, and builds `linux/arm64` on an `amd64`
  host without emulation.
- Every pushed build with uv runs a resolution, even when only DAG code
  changed, since the key needs it. It is a metadata request per package that
  uv's cache does not hold; the builder line prints its time from one second.
- When PyPI cannot be reached, the resolution fails and the build declines
  to the daemon (pinned dependencies) or to the whole build (unpinned ones),
  with uv's reason.
- A new release of a transitive dependency changes the resolution and so the
  key: dependency images built from wheels pick up security updates on the
  next build, without `--rebuild`. More `deps-*` tags accumulate than with
  the daemon's key.
- A new uv version changes the key and rebuilds the dependency image once.
- The first import of each module costs its compilation in every task
  process (see above). Writing bytecode into the layer, with an interpreter
  of the base's Python version, is a later step.
- uv is the user's own binary from `PATH`, trusted as the container tool of
  the daemon path is.
