---
title: How deploy builds the DAG image
linkTitle: Builders
weight: 13
description: The builders `dexaflow deploy`, `compile --build` and `build` choose from, in order, the remote builder on Cloud Build or on the in-cluster builder the Helm chart can run, how the target platform is picked, and the local container daemon as the fallback.
---

A DAG version is a `dag.json` and a container image
([ADR 0003](/project/adrs/0003-dag-as-image/)). `dexaflow deploy`,
`dexaflow compile --build` and `dexaflow build` produce that image with a
**builder**, chosen in a fixed order, and say which one ran. The image is
pushed to your registry and `deploy` registers it by digest, so the cluster
pulls exactly the bytes that were built.

## The order builders are tried in

The CLI uses the first builder that applies
([ADR 0073](/project/adrs/0073-daemonless-image-builds/)). The first four need
no container daemon on your machine.

| Order | Builder | Used when | Status |
|---|---|---|---|
| 1 | `skip` | The whole DAG image for this content is already in the registry under its content label ([see below](#reusing-an-image-already-in-the-registry)) | Available for `deploy` |
| 2 | `code-layer` | The image of your dependencies is already in the registry: only your DAG code is pushed, as one layer on it, through the registry ([see below](#pushing-only-the-dag-code)) | Available for pushed builds |
| 3 | `dependency-layer` | The image of your dependencies is missing and every dependency has a wheel for the target platform: they are installed for that platform with `uv` and added as a layer on the runtime base, with no daemon and no emulation ([see below](#building-the-dependency-image-from-wheels)) | Available when `uv` is on `PATH` |
| 4 | `remote` | A real build is needed (a package without wheels, `system_packages`, your own `Dockerfile`) and a remote builder is configured: Google Cloud Build ([see below](#building-on-cloud-build)) or the [in-cluster builder](#building-on-the-in-cluster-builder) the Helm chart runs | Available |
| 5 | `local-daemon` | None of the above applies: a local container tool builds and pushes the image | Available, the fallback |

Every build prints the builder it took and why before it starts:

```console
$ dexaflow deploy --server https://pro.example.com --yes
server:     https://pro.example.com (--server)
registry:   us-central1-docker.pkg.dev/acme/dags/first-pro-dag (from dexaflow.yaml)
platforms:  linux/amd64 (from the default)
...
builder:    remote (cloud-build, project acme-ci), the project ships its own Dockerfile, which needs a real build, so the remote builder from dexaflow.yaml builds the whole image
...
Deployed first_pro_dag -> https://pro.example.com
  image us-central1-docker.pkg.dev/acme/dags/first-pro-dag@sha256:9f2c...
```

With no remote builder configured, a build that no earlier path covers falls
back to `local-daemon`, and the line says why, for example
`builder:    local-daemon (docker), fallback: the project ships its own Dockerfile, which needs a real build`.

`compile --build` and `build` print the same two lines, `platforms:` with its
source and `builder:`. With `deploy --skip-build` nothing is built and no
builder line is printed.

## Reusing an image already in the registry

The default version label is a hash of everything the image is built from
(`dexaflow.yaml`, the files the image copies, the CLI version). With
`tag_strategy: version`, the default, the image tag is that label, so an
image the registry already holds under it was built from the same inputs.
`deploy` then pins that image and registers it without building or pushing:

```console
builder:    skip, the registry already holds the image for this content label (pass --rebuild to build it again)
```

This happens when the same content was pushed before but is not the current
version on this control plane: a deploy to a second target that shares the
registry, a CI job that already built the image, or a deploy whose register
step failed. When the content is already the current version, `deploy` stops
earlier and says so.

The image is reused only when it holds every requested platform. It is never
reused for an explicit `--dag-version`, for a tag from git
(`tag_strategy: git_sha` or `git_tag`, unless it fell back to the version
label outside git or with no tag on `HEAD`), or with `--rebuild` or
`--force`.
The checks that run before a build still run: a `build.dockerfile` that
resolves outside the project is refused, and secrets the image would carry
are reported. Any registry error (a missing tag, `401`, a timeout) means a
normal build, and the `builder:` line names the error.

`deploy --plan` reads the registry too and says when the image would be
reused:

```console
  build       no
  push        no
  why         content changed: ...; the registry already holds the image for this content label, so it is pinned without a build or push (--rebuild builds it again)
```

### What the label does not cover

Like the "already deployed" check, a reuse trusts the label. It does not
pick up:

- a new upstream release of a dependency without a pinned version
  (`pandas>=2`), or of a `base_image` given by tag;
- anything the label does not hash, such as an environment variable the
  image build reads.

Pass `--rebuild` to build the image again. For images that are reproducible,
pin dependencies to exact versions (or commit a lock) and give `base_image`
by digest.

### Who decides what a reused image contains

The tag of a reused image is predictable from the sources, so anyone who can
push to the DAG's image repository can place an image under a label before
the deploy of that label runs, and the deploy registers it. Treat push access
to that repository as deploy access:

- give push rights only to the identity that deploys (the CI job or the
  operator), and pull rights to the cluster;
- enable tag immutability where your registry offers it (ECR, Artifact
  Registry, Harbor, ACR), so a tag that exists cannot be replaced.

A pipeline that must build what it ships, every time, passes `--rebuild`
when tags are mutable. With tag immutability on, `--rebuild` of a label that
is already in the registry fails at the push; such a pipeline gives every
build its own label instead, for example `--dag-version "$CI_PIPELINE_ID"`,
which never reuses and never collides.

The decision and its trade-offs are recorded in
[ADR 0076](/project/adrs/0076-reuse-image-by-content-label/).

## Pushing only the DAG code

A pushed build of the Dockerfile Dexaflow generates is made of two images:

- the **dependency image**: the base image, `system_packages` and your
  Python dependencies, with `PYTHONPATH` and the non-root user. It is the
  generated Dockerfile without its `COPY` lines;
- the **DAG image**: the dependency image plus one layer holding your DAG
  code, the files those `COPY` lines take.

The dependency image is stored in the DAG's own repository under a tag
derived from what it contains, `deps-<key>`. The key covers the base image
digest, `system_packages`, `dependencies` and `connectors`, and the
platforms; when the image is built from wheels, it covers the versions and
hashes uv resolved instead of the declared ones. When the registry already holds the dependency image for the
key, no container daemon runs: the code layer is added to it through the
registry API and pushed under the DAG image tag.

```console
builder:    code-layer, the registry holds the dependency image ghcr.io/your-org/etl:deps-4f1c... for this build, so only the DAG code is pushed, as a layer on it (pass --rebuild to build the dependency image again)
```

The first build of a key, or a build with `--rebuild` (on `deploy`,
`compile --push` and `build --push`), builds and pushes the dependency image
first, then adds the code layer. With `uv` on `PATH` it is built from wheels
without a daemon ([see below](#building-the-dependency-image-from-wheels));
otherwise the local daemon builds it, and the line says why the wheels could
not:

```console
builder:    local-daemon (docker) for the dependency image, then code-layer, no dependency image ghcr.io/your-org/etl:deps-4f1c... in the registry (...): the daemon builds and pushes it (no daemonless dependency layer: uv is not on PATH), then the DAG code is appended to it
```

The daemon builds the dependency image from an empty directory, so no
project file reaches that build. Every later deploy that changes only DAG code, from any
machine or CI runner that can reach the registry, skips the daemon.

### When the build stays whole

The whole image is built by the [remote builder](#building-on-cloud-build)
when one is configured, otherwise by the local daemon, and the `builder:`
line says why, when:

- a Python dependency is not pinned to one version and the dependency image
  cannot be built from wheels (the line says why, for example `uv is not on
  PATH`). Install `uv`, or pin each dependency (`pandas==2.2.3`); a
  connector's provider package is pinned by listing it pinned in
  `dependencies` too (`apache-airflow-providers-amazon==9.1.0`);
- the project ships its own Dockerfile;
- `dag_source` is in a subdirectory;
- the digest of the base image cannot be read (no network or no credentials
  for its registry), or the registry of the DAG image answers the read of
  the dependency image with an error other than "unknown tag";
- an `include_paths` entry is a pattern (`lib/*.py`);
- the build is not pushed (`compile --build` without `--push`), or has no
  platform.

### What the code layer contains

The layer holds the files the generated Dockerfile copies: the DAG source,
`include_paths`, the dbt project and the directories of `dbt_groups`,
filtered by your `.dockerignore` and `exclude_paths` with Docker's rules, as
the full build filters them. `.git`, `.dockerignore` and the compiled
`dag.json` never ship, even from a project at `.`. Symlinks inside a copied
directory stay links. A file whose name starts with `.wh.` is refused, since
a layer reads it as a deletion. Every
entry is owned by root, with a fixed date, mode `0755` for directories and
executables and `0644` for other files, so the same files always make the
same layer and the non-root task user can read every file.

### Keeping dependency images current

A dependency image is reused for as long as its key holds. One built from
wheels has a key that covers every resolved version, so a new release of a
dependency or of a transitive one gives a new dependency image on the next
build. One the daemon built has transitive dependencies (the packages your
pinned ones pull in) at the versions pip
resolved when it was first built, and its `system_packages` at the versions
apt installed then, so their security updates arrive only with a rebuild.
Pass `--rebuild` to build the dependency image again, for example on a
schedule in CI. A base image given by tag is read by digest on every build, so
a new image under that tag gives a new key and a new dependency image.

`deps-*` tags accumulate in the DAG repository, one per set of dependencies.
A retention rule of your registry can expire old ones; a deploy whose
dependency image is gone builds it again. The access rules of
[Who decides what a reused image contains](#who-decides-what-a-reused-image-contains)
cover these tags too: with tag immutability on, `--rebuild` of dependencies
that are already in the registry fails at the dependency push.

The decision is recorded in
[ADR 0077](/project/adrs/0077-dependency-image-and-code-layer/).

## Building the dependency image from wheels

When the dependency image is missing (or with `--rebuild`) and
[uv](https://docs.astral.sh/uv/getting-started/installation/) is on `PATH`
with a Python it can find (any version: uv needs one to run, even to install
for another; `uv python find` shows which), the CLI builds it with no
container daemon. uv never downloads a Python here; install one with your
system's packages or `uv python install` if `uv python find` finds none.

1. it reads the base image by digest, for each platform: its Python version,
   its glibc, and the Python distributions it already holds. That listing is
   cached in your user cache directory (`~/.cache/dexaflow/base/` on Linux),
   so a redeploy downloads no base layer;
2. uv resolves your `dependencies` and `connectors` for each platform, wheels
   only, with every distribution of the base pinned;
3. uv installs the wheels the base does not already hold into a temporary
   directory on your machine, and the CLI packs it as one layer on the base
   (`/usr/local/lib/pythonX.Y/site-packages` and `/usr/local/bin`), sets
   `PYTHONPATH` and the non-root user as the generated Dockerfile does, and
   pushes the dependency image through the registry.

```console
builder:    dependency-layer (uv 0.11.32), then code-layer, no dependency image ghcr.io/your-org/etl:deps-9b07... in the registry (...): it is built from wheels without a daemon and pushed, then the DAG code is appended to it
```

uv runs in an empty directory with `--no-config` and without your `UV_*`
and `PIP_*` variables (the cache directory, proxies and certificate
settings are kept; Python downloads are off), so your machine's uv or pip configuration never changes
what ships. Packages come from PyPI. Because the key covers what uv
resolved, dependencies with a range (`pandas>=2`) take this path too, and a
new release that matches the range gives a new dependency image. The
resolution runs on every pushed build; the builder line prints how long it
took when that was a second or more.

Wheels are chosen for glibc 2.36 at most (`manylinux_2_36`), which runs on
the Debian 12 and Debian 13 runtime bases. `linux/arm64` is built on an
`amd64` machine, and the reverse, without emulation.

The dependency layer does not apply, and the daemon builds the dependency
image (or the whole image, for dependencies that are not pinned), when:

- `uv` is not on `PATH`, or it finds no Python on your machine;
- the CLI runs on Windows (not supported on this path yet);
- the project has `system_packages`, which need a real build;
- a platform is not `linux/amd64` or `linux/arm64`;
- a dependency is a URL, a local path or a VCS reference (`pkg @ https://...`,
  `./lib`, `git+https://...`);
- the base image is not a Dexaflow runtime base (its config sets no
  `PYTHON_VERSION`, it has no `/usr/local/lib/pythonX.Y/site-packages`, or
  it records no glibc), or `python_version` in `dexaflow.yaml` names another
  Python than the base's;
- the base image configures pip: a `PIP_INDEX_URL`, `PIP_EXTRA_INDEX_URL`,
  `PIP_FIND_LINKS`, `PIP_CONSTRAINT` or other `PIP_*` variable that changes
  where or what pip installs, or a `pip.conf` such as `/etc/pip.conf`. The
  daemon's pip would honor it, and the dependency layer resolves from PyPI;
- uv finds no set of wheels: a dependency has no wheel for a platform, it
  conflicts with a package of the base image at another version, or PyPI
  cannot be reached. The builder line carries uv's reason.

With a [package index](#build-arguments-secrets-and-a-private-package-index),
uv resolves and installs from it instead of PyPI; its URL and credentials
reach uv through its environment (`UV_DEFAULT_INDEX`, and `UV_INDEX_URL` for
uv releases older than `UV_DEFAULT_INDEX`), never its command line.

If the install or the push of the dependency image fails after the path was
chosen and every dependency is pinned, the dependency image the daemon path
uses takes its place: the one already in the registry, or one the local
daemon builds when the container tool is on `PATH`. The output says why;
otherwise the build fails with uv's error.

The layer holds no bytecode: uv does not compile it, and your machine's
Python is another version than the image's. Each task process compiles the
modules it imports in memory the first time, which adds about a second for a
set like `pandas`, `requests` and `sqlalchemy`. The decision and its
trade-offs are recorded in
[ADR 0078](/project/adrs/0078-dependency-image-from-wheels/).

## Building on Cloud Build

When a build needs a real Dockerfile build, the remote builder runs it on
[Google Cloud Build](https://cloud.google.com/build/docs) instead of a
container daemon on your machine. It takes:

- a project that ships its own `Dockerfile`, and the other whole builds
  listed under [When the build stays whole](#when-the-build-stays-whole);
- the dependency image of a split build that cannot be built from wheels
  (`system_packages`, a package without a wheel, uv not on `PATH`), or whose
  wheel install failed. The code layer is then appended through the
  registry as usual, and no project file is uploaded for that build.

Configure it in `dexaflow.yaml`:

```yaml
build:
  remote_builder:
    cloud_build:
      project: acme-ci                 # required: the project the build runs in
      region: us-central1              # optional: default is gcloud's builds/region, else global
      worker_pool: projects/acme-ci/locations/us-central1/workerPools/private   # optional
      service_account: projects/acme-ci/serviceAccounts/builder@acme-ci.iam.gserviceaccount.com   # optional
```

or once for many projects, as `remote_builder` on a target or in the
workspace `defaults` ([workspaces and targets](/operate/workspaces-and-targets/)),
or once for the whole cluster, as `deploy.defaults.remoteBuilder` in the
server's Helm values
([cluster deploy defaults](/operate/workspaces-and-targets/#remote-builder-task-service-account-and-warm-pools)),
which `deploy` and `build --push` read from the server;
the project's own block wins, then the target's, then the workspace's, then
the cluster's, and the `builder:` line names where it came from. A Google Cloud registry alone
never selects Cloud Build: where the build runs, and who pays for it, is
your choice. The block is not part of the content label, so adding it does
not change the DAG version.

The image must live in **Artifact Registry** (`<location>-docker.pkg.dev`)
or **GCR**: Cloud Build pushes the images of a build with its service
account, which other registries (`ghcr.io`, Docker Hub, ECR, Harbor) do not
know. For any other registry the CLI prints a note before the build, and the
push is likely to be refused.

What you need:

1. on the machine that deploys, the
   [Google Cloud CLI](https://cloud.google.com/sdk/docs/install), signed in
   with `gcloud auth login` (or a service account with
   `gcloud auth activate-service-account`);
2. an account with `roles/cloudbuild.builds.editor` on the project, and the
   Cloud Build API enabled;
3. for the build's service account (the project's Cloud Build service
   account, or `service_account`): push access to the image repository
   (`roles/artifactregistry.writer`), and pull access to the base image
   when it is private (`base_image`, or the `FROM` of your Dockerfile).

No container daemon is needed. For a whole build (your own `Dockerfile`),
nothing is pushed from your machine: Cloud Build pushes, and the CLI only
reads the digest. For a split build, the dependency image is pushed by Cloud
Build and the code layer is then pushed **from your machine**, with your own
credential: your account needs push access to the repository too. With an
Artifact Registry or GCR image that credential is your gcloud session, so no
`docker login` is needed either way
([registry credentials](#registry-credentials)).

The deploy preflight checks your own push credential as usual. When the
registry refuses it, a whole build on Cloud Build goes on with a note; every
other build (a split build, an arm64 build, a build that falls back to the
local daemon) stops with the refusal before anything is built, as without a
remote builder.

What happens:

1. the CLI lists the build context as `docker build` would read it: the
   project directory with your `.dockerignore`, `exclude_paths` and the dbt
   artifacts left out, with Docker's rules. A `<Dockerfile>.dockerignore`
   next to your Dockerfile (`Dockerfile.dockerignore`) is read instead of
   `.dockerignore`, as BuildKit does. It packs that list and the Dockerfile
   into one archive, at most 2 GiB compressed. gcloud never reads the
   directory itself, so `.gcloudignore` does not apply, and a file
   `exclude_paths` keeps out never leaves your machine. `.git` is sent only
   when your `.dockerignore` keeps it, as with a local build;
2. it runs `gcloud builds submit <archive> --config=<file> --project=<project>
   --async` with the region, pool and service account you set, and prints
   the build id and its console link. The build has one step,
   `gcr.io/cloud-builders/docker` running `docker build --platform
   linux/amd64`, and Cloud Build pushes the tag. Builds are tagged
   `dexaflow` in Cloud Build's history
   (`gcloud builds list --filter=tags=dexaflow`);
3. it follows the build until it ends. Without `service_account`, the log is
   streamed with `gcloud builds log --stream`. A build with its own service
   account logs to Cloud Logging only, which gcloud does not stream: the CLI
   prints the `gcloud logging read` command for its log and waits for the
   build to end. A failed `gcloud builds describe` is retried a few times
   with a growing wait; when it keeps failing, the CLI cancels the build and
   prints its id and the cancel command. Ctrl-C interrupts gcloud and
   cancels the build on Cloud Build, so it does not go on and push. A Ctrl-C
   while gcloud is still submitting cannot know the build id: the CLI says a
   build may have started and prints
   `gcloud builds list --filter=tags=dexaflow --ongoing` to find it;
4. the digest the deploy registers is read from the registry, and must be
   the digest Cloud Build reports for the image it pushed. When another push
   of the same tag happened in between, the deploy stops instead of
   registering the other image.

```console
builder:    remote (cloud-build, project acme-ci, region us-central1) for the dependency image, then code-layer, no dependency image us-central1-docker.pkg.dev/acme/dags/etl:deps-4f1c... in the registry (...): the remote builder builds and pushes it (no daemonless dependency layer: the project has system_packages, which need a real build), then the DAG code is appended to it
```

The archive leaves your machine: Cloud Build stores it in the project's
source bucket, `gs://<project>_cloudbuild/source/`, and keeps it there until
you delete it (a lifecycle rule on the bucket does this). For a
domain-scoped project such as `example.com:acme-ci`, gcloud names the bucket
`gs://example_com_acme-ci_cloudbuild`, and the warning names that bucket. Before the upload
the CLI warns about any secret-looking file in the context (`.env`,
`.netrc`, `credentials.json`, `.ssh/` and the like), whatever your
Dockerfile copies. A dependency image is built from its Dockerfile alone, so
no project file is uploaded for it.

The remote builder is passed over, and the `builder:` line says why, when:

- the build is not pushed (`compile --build` without `--push`): Cloud Build
  pushes what it builds;
- the platform is not `linux/amd64`, or there are several. Cloud Build's
  default workers are amd64 and the build sets up no emulation, so such a
  build goes to the local daemon;
- `gcloud` is not on `PATH` and the local daemon's tool is. With neither,
  the build fails with how to install gcloud.

A refused submit keeps gcloud's last line and names what to check: the
sign-in, the project and the roles above. A build that fails on Cloud Build
names its id, its status and the command that prints its log.

Build arguments reach the Cloud Build step as `--build-arg`. A project with
build secrets or a package index does not build here: their values come from
your machine's environment, and Cloud Build would have to store them in its
build record. Such a build goes to the local daemon, and the `builder:` line
says why ([see below](#build-arguments-secrets-and-a-private-package-index)).

## Build arguments, secrets and a private package index

`dexaflow.yaml` declares what a build needs beyond the project files:

```yaml
build:
  args:                    # --build-arg for the ARG lines of your own Dockerfile
    APP_ENV: prod
  secrets:                 # RUN --mount=type=secret,id=<id> in your own Dockerfile
    - id: github_token
      env: GITHUB_TOKEN    # read from the environment of the machine that builds
  package_index:           # installs dependencies from this index instead of PyPI
    url: https://pypi.example.com/simple
    username_env: PYPI_USER
    password_env: PYPI_TOKEN
```

- **`args`** are plain values: they stay in the image history, and they are
  part of the content label, so changing one builds a new image. Only your
  own Dockerfile reads them (`ARG APP_ENV`); the generated Dockerfile declares
  none, and the build warns when they are set without one.
- **`secrets`** are passed as `--secret id=<id>,env=<variable>`: the container
  tool reads the value from its own environment, so it never appears in its
  command line, in the build log, in `dag.json` or in a layer. A RUN line of
  your Dockerfile mounts it for that one step:

  ```dockerfile
  RUN --mount=type=secret,id=github_token \
      GITHUB_TOKEN="$(cat /run/secrets/github_token)" ./fetch-private-assets.sh
  ```

  A variable that is not set fails the build before the tool runs, naming it.
  The `env=` form needs BuildKit: docker with buildx (the legacy builder has
  no `--secret`), or a podman or nerdctl release that takes `env=` secrets.
  Its value is not part of the content label; its id and variable are.
- **`package_index`** replaces PyPI rather than adding to it, so a package
  name can never be taken from another index (dependency confusion): mirror
  PyPI through it (an Artifact Registry remote repository, devpi, Nexus,
  Artifactory). The URL is `https` (plain `http` only on the loopback) and
  carries no credentials; the user name and token come from the two variables.
  On the [daemonless path](#building-the-dependency-image-from-wheels) uv
  installs from it. The generated Dockerfile mounts it as the build secret
  `dexaflow_index_url` for its `pip install` only:

  ```dockerfile
  RUN --mount=type=secret,id=dexaflow_index_url,required=true PIP_INDEX_URL="$(cat /run/secrets/dexaflow_index_url)" pip install --no-cache-dir -- 'pandas==2.2.3'
  ```

  Your own Dockerfile can mount the same secret. The index URL is part of the
  content label and of the key of the dependency image, so an image built
  from one index is never reused for another.

Secret mounts need BuildKit: docker (BuildKit is its default builder), docker
buildx, podman and nerdctl all support them. A build with secrets or a
package index runs on your machine, never on a [remote
builder](#building-on-cloud-build); the `builder:` line says so:

```console
builder:    local-daemon (docker), fallback: the project ships its own Dockerfile, which needs a real build; the remote builder from dexaflow.yaml does not apply: build.secrets reach the build from this machine's environment, so only a builder on this machine takes it
```

A deploy that reuses an image already in the registry builds nothing and
needs none of the variables. The decision is recorded in
[ADR 0088](/project/adrs/0088-build-args-secrets-and-package-index/).

## The in-cluster builder

On a cluster with no cloud builder, the Helm chart can run a builder for you:
rootless [BuildKit](https://github.com/moby/buildkit) in a namespace of its
own. `builder.enabled` deploys it, and, unless `builder.advertise` is false,
the server advertises it as the cluster's remote builder, so `deploy` builds
there what no daemonless path covers
([see below](#building-on-the-in-cluster-builder)).

What the chart renders, all in `builder.namespace`:

- a Deployment of one pod running `moby/buildkit:<version>-rootless` as uid
  1000, listening on a unix socket only. It has no port and no Service, and
  needs no TLS: a client reaches it through the Kubernetes API, with
  `kubectl exec`.
- a ServiceAccount with no token and no Role: the builder never calls the
  Kubernetes API, and build steps find no token in the pod.
- a Role that allows `get` and `list` on pods and `create` and `get` on
  `pods/exec` in that namespace and nowhere else, bound to `builder.subjects`.
  Being bound is what lets someone build.
- a NetworkPolicy, on by default with the builder: no ingress, and egress to
  DNS and everywhere except the cloud metadata range (`169.254.0.0/16`) and
  `builder.networkPolicy.extraExcept`. Build steps run code from Dockerfiles,
  and the metadata endpoint would hand them the node's credentials. A CNI
  that does not enforce policies ignores it.

Registry credentials stay with the client: BuildKit asks the client for them
when it pushes, so the builder holds no secret.

### Installing it

The chart does not create the namespace. With the default seccomp and
AppArmor profiles it must allow the privileged Pod Security level (the pod is
not privileged, see [Security posture](#security-posture)):

```sh
kubectl create namespace dexaflow-builder
kubectl label namespace dexaflow-builder pod-security.kubernetes.io/enforce=privileged
```

```yaml
# values.yaml
builder:
  enabled: true
  namespace: dexaflow-builder
  subjects:
    - kind: Group
      name: platform-deployers
    - kind: ServiceAccount
      name: ci-deployer
      namespace: ci
```

The chart refuses to render without `builder.namespace`, with the release
namespace or with the task namespace: the builder runs build steps under a
looser posture than task pods and must not share a namespace with them or
with the control plane.

Check it from a machine bound by `builder.subjects`, with
[`buildctl`](https://github.com/moby/buildkit/releases) and `kubectl` on
`PATH`:

```sh
POD=$(kubectl -n dexaflow-builder get pods -l app.kubernetes.io/component=builder -o jsonpath='{.items[0].metadata.name}')
buildctl --addr "kube-pod://$POD?namespace=dexaflow-builder" debug workers
buildctl --addr "kube-pod://$POD?namespace=dexaflow-builder" build \
  --frontend dockerfile.v0 --local context=. --local dockerfile=. \
  --opt platform=linux/amd64 \
  --output type=image,name=registry.example.com/team/app:dev,push=true
```

The builder builds for the architecture of the node it runs on; schedule it
with `builder.nodeSelector` on nodes of the cluster's architecture.

### Building on the in-cluster builder

`remote_builder.buildkit` sends the builds a remote builder takes (the same
ones as [Cloud Build](#building-on-cloud-build): a project `Dockerfile`, a
dependency image the wheels cannot build) to the in-cluster builder instead
of a container daemon on your machine:

```yaml
build:
  remote_builder:
    buildkit:
      namespace: dexaflow-builder    # required: the chart's builder.namespace
      pod_selector: app.kubernetes.io/instance=dexaflow,app.kubernetes.io/component=builder   # optional
      context: prod-eu               # optional: the kubeconfig context; the current one when unset
```

Set `buildkit` or `cloud_build`, not both. Like Cloud Build, it can be set on
a target or in the workspace `defaults`
([workspaces and targets](/operate/workspaces-and-targets/)), and the cluster
advertises it on its own: with `builder.enabled` and `builder.advertise` (the
default), the chart sets `deploy.defaults.remote_builder.buildkit` to the
builder's namespace and the pod labels of the release, so a deploy that names
no remote builder of its own uses it. A server cannot know the names of your
kubeconfig contexts, so a builder it advertises is reached with the current
one: point `kubectl` at that cluster, or set the block with `context` on the
target. The chart refuses `builder.advertise` next to
`deploy.defaults.remoteBuilder.cloudBuild`; set `builder.advertise: false` to
keep Cloud Build as the cluster's default.

What you need on the machine that deploys:

1. [`buildctl`](https://github.com/moby/buildkit/releases) and `kubectl` on
   `PATH`, `kubectl` signed in to the cluster;
2. to be bound by `builder.subjects` (a user, a group or a ServiceAccount):
   the Role allows `get` and `list` on pods and `create` on `pods/exec` in the
   builder namespace;
3. push access to the image repository in your docker config or one of its
   credential helpers, such as `dexaflow registry login` writes
   ([registry credentials](#registry-credentials)). BuildKit asks `buildctl`
   for the credential when it pushes, so the builder holds none. A credential
   the CLI reads only from a cloud CLI's session
   ([ADR 0080](/project/adrs/0080-registry-credentials-from-cloud-cli-sessions/))
   is not passed on: install that cloud's docker credential helper.

What happens:

1. before choosing the builder, the CLI runs
   `kubectl auth can-i create pods/exec` in the builder namespace. When
   `buildctl` or `kubectl` is missing, or the answer is no, the builder is
   passed over for the local daemon when its tool is on `PATH`, and the
   `builder:` line says why; with no daemon either, the build fails with what
   to install or which binding to ask for;
2. it lists the builder pods with `kubectl get pods --selector=<pod_selector>`
   and takes the first ready one;
3. it runs `buildctl --addr kube-pod://<pod>?namespace=<namespace> build`
   with the Dockerfile frontend, the project directory as the context, the
   Dockerfile by its own name, `--opt platform=<platform>` and the build
   arguments. `buildctl` reads the context with the same ignore file a local
   `docker build` reads: your `.dockerignore` with `exclude_paths` and the dbt
   artifacts merged in, or a `<Dockerfile>.dockerignore` next to your
   Dockerfile. The output is the image, pushed by BuildKit, and the build log
   streams to your terminal. Ctrl-C stops `buildctl`, which ends the session,
   and BuildKit cancels the build;
4. the deploy registers the digest BuildKit reports for the push
   ([ADR 0087](/project/adrs/0087-local-daemon-digest-from-the-push/)), or the
   one the registry holds when it reports none.

```console
builder:    remote (buildkit, namespace dexaflow-builder), the project ships its own Dockerfile, which needs a real build, so the remote builder from the deploy defaults of https://flow.acme.dev builds the whole image
building on the in-cluster builder in namespace dexaflow-builder, on pod dexaflow-builder-7d9c6b5f4-x2k8p
```

The push leaves from the builder with your credential, so the deploy
preflight's refusal of your push credential stops the deploy before anything
is built, as with the local daemon. Before the build the CLI warns about any
secret-looking file in the context (`.env`, `.netrc`, `.ssh/` and the like):
it reaches the build cache everyone bound to the builder shares.

The in-cluster builder is passed over, and the `builder:` line says why, when
the build is not pushed, and when it is for several platforms: the rootless
builder sets up no emulation and builds for the architecture of its node, so
a single `--platform` must be that architecture (a `RUN` step for another one
fails with `exec format error`). A build with build secrets or a package
index never runs there: their values come from your environment, and build
steps in the shared pod can read each other's processes.

### Security posture

The pod runs with the least rootless BuildKit starts with:

| Setting | Value | Why |
|---|---|---|
| `runAsUser`, `runAsNonRoot` | 1000, true | The image's own user. Build steps run as root inside a user namespace that maps to it, never as root on the node. |
| `privileged` | false | Build steps share the pod's process namespace (`--oci-worker-no-process-sandbox`) instead of one of their own, which would need a privileged pod. |
| `capabilities` | drop `ALL`, add `SETUID` and `SETGID` | The file capabilities of `newuidmap` and `newgidmap`, which write the user namespace's id maps. Without them the daemon does not start. |
| `allowPrivilegeEscalation` | true | `no_new_privs` stops `newuidmap` from gaining those two capabilities. |
| `seccompProfile`, `appArmorProfile` | `Unconfined` | The runtime default profiles refuse the `unshare` and `mount` calls a user namespace needs. Both are values: a `Localhost` profile that allows them replaces `Unconfined`, and `null` leaves the AppArmor field out. |
| `readOnlyRootFilesystem` | true | Every write goes to an `emptyDir`; the cache's is limited by `builder.cache.sizeLimit`. |

The `Unconfined` profiles are why the namespace needs the privileged Pod
Security level and why the builder has a namespace of its own. With
`Localhost` profiles for both, the baseline level admits it.

Everyone bound by `builder.subjects` shares one daemon and its cache: a build
step can see the other builds running in the pod, and `pods/exec` opens a
shell in it. Bind people who already trust each other, such as a platform
team and its CI.

The cache lives in an `emptyDir` and is lost when the pod restarts. buildkitd's
garbage collector keeps it under `builder.cache.gcKeepStorage` (MB); keep
`builder.cache.sizeLimit` above that plus your largest build, or the kubelet
evicts the pod. All values are listed in the
[chart README](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md).

## Choosing the platform

The image is built for the cluster's architecture, not your machine's. The
platform comes from the first of these that is set, and the output names which:

1. `--platform` on `deploy`, `compile` or `build`;
2. `build.platforms` in the project's `dexaflow.yaml`;
3. the target's `platforms`
   ([workspaces and targets](/operate/workspaces-and-targets/));
4. `defaults.platforms` in the workspace file;
5. `linux/amd64`.

```bash
dexaflow deploy --platform linux/arm64 --yes    # a Graviton cluster
```

A platform is written `os/arch` or `os/arch/variant`, such as `linux/amd64` or
`linux/arm64/v8`. A malformed value is refused before anything is parsed,
built or sent to the control plane, wherever it came from, and the error names
that source.

Several platforms (`--platform linux/amd64,linux/arm64`) need a tool that
pushes one image per platform under one manifest list. The `local-daemon`
builder hands the list to the tool, each tool the way it builds a manifest
list ([see below](#the-local-daemon-fallback)): docker with buildx, docker
with the containerd image store enabled, podman and nerdctl all push one.

Whatever the tool, a multi-platform push is then checked before anything is
registered: the CLI reads the pushed image from the registry itself, at the
digest the tool reported for the push (at the tag when the tool reports
none), and it must be a manifest list holding an image for every requested
platform. A tool
can succeed at build and push and still leave a partial image, so the build
fails when the build itself fails, when the list misses a platform, and when
the registry cannot be read or answers something that is not a list. The
error keeps the cause and says how to build one platform:

```text
Error: the multi-platform build failed: building for linux/amd64,linux/arm64 with local-daemon (docker) needs a tool that pushes one image per platform under one manifest list, such as docker with buildx or with the containerd image store enabled, or podman. Otherwise build one platform: set build.platforms in dexaflow.yaml (or pass --platform where the command has it), for example linux/amd64: the pushed image ghcr.io/your-org/first-pro-dag@sha256:... is not a manifest list holding every platform; missing linux/arm64
```

`compile --build --push` runs the same check; a one-platform build is a plain
image and is not checked.

## The local daemon fallback

`local-daemon` is the last path, for a build no earlier path covers and no
remote builder takes. It runs a container tool on your machine: `docker` by
default, or `podman` or `nerdctl` with `--builder`. The digest `deploy`
registers is the one the tool reports for its own push, so another push of
the same tag right after yours (two CI jobs building one version) cannot
change which image is registered
([ADR 0087](/project/adrs/0087-local-daemon-digest-from-the-push/)):

| Tool | Build and push | Digest taken from |
|---|---|---|
| docker with buildx | `docker buildx build --platform ... --push --metadata-file <file>` | `containerimage.digest` in the metadata file |
| docker without buildx | `docker build --platform ...`, then `docker push` | the `digest: sha256:...` line `docker push` ends with |
| podman, one platform | `podman build --platform ... -t <image>`, then `podman push --digestfile <file>` | the digest file |
| podman, several platforms | `podman build --platform a,b --manifest <image>`, then `podman manifest push --all --digestfile <file>` | the digest file |
| nerdctl | `nerdctl build --platform ...`, then `nerdctl push` (with `--all-platforms` for several platforms) | the registry, at the tag |

With podman, a manifest list left under the image name by an earlier build
is removed first, so its old images are not pushed again. nerdctl reports no
digest in a form the CLI can rely on, and a tool that reports none, or one
that is not a `sha256:` digest, falls back to reading the tag from the
registry right after the push, with a note, as before
([ADR 0075](/project/adrs/0075-daemonless-builds-in-0-5-4/)). A build that is
not pushed (`compile --build` without `--push`) is a plain `<tool> build`.

`--builder` takes any tool that answers those two commands as docker does
(`build --platform <platforms> -t <image> -f <Dockerfile> <dir>`, then
`push <image>`), so `--builder buildah` builds the generated Dockerfile with
buildah, with no daemon, in a pipeline pod. kaniko takes other arguments and
runs only in its own image, so it is not a `--builder`: a pipeline builds a
project's own Dockerfile with it and registers the digest it pushed. Both
are under
[Recipes for Argo Workflows, Tekton, Jenkins, buildah and kaniko](/operate/cicd-deploy/#recipes-for-argo-workflows-tekton-jenkins-buildah-and-kaniko).

Ctrl-C (or SIGTERM) during the build or the push interrupts the tool, which
cancels it, and the CLI kills it if it has not exited 10 seconds later. The
CLI then removes the Dockerfile it generated and restores your
`.dockerignore` before it exits with code `130`, so an interrupted build
leaves the project as it found it. A second Ctrl-C ends the CLI at once,
without that cleanup.

## Registry credentials

The CLI reads and pushes to the registry in process, so it needs a credential
for it, and finds one in this order
([ADR 0080](/project/adrs/0080-registry-credentials-from-cloud-cli-sessions/)):

1. The docker config, `~/.docker/config.json` (or `$DOCKER_CONFIG`), with its
   `credsStore` and `credHelpers`: what [`dexaflow registry
   login`](#signing-in-without-docker), `docker login`, `gcloud auth
   configure-docker`, `az acr login` or a helper such as
   `docker-credential-ecr-login` on `PATH` wrote.
2. When the docker config holds nothing for the registry, and the registry
   belongs to a cloud, the session of that cloud's CLI. You need no
   `docker login` and no helper, only to be signed in to the cloud:

   | Registry host | Signed in with | The CLI runs |
   |---|---|---|
   | `gcr.io`, `*.gcr.io`, `<location>-docker.pkg.dev` | `gcloud auth login` | `gcloud auth print-access-token` |
   | `<account>.dkr.ecr.<region>.amazonaws.com` | `aws sso login`, `aws configure`, a profile or an instance role | `aws ecr get-login-password --region <region>` |
   | `<name>.azurecr.io` | `az login` | `az acr login --name <name> --expose-token` |

   The token stays in memory for ten minutes at most, and a failure of the
   CLI (signed out, not installed) for thirty seconds; each run of the CLI
   has thirty seconds and no input. Dexaflow never stores a cloud session's
   token.

`registry.auth_method` in `dexaflow.yaml` picks the sources: `auto` (the
default) is the order above, `docker_config` reads only the docker config,
and `cloud` reads only the cloud CLI for a registry a cloud owns, which helps
when the docker config keeps an expired entry for it (an ECR login lasts twelve
hours) that would otherwise be sent first; a registry no cloud owns (`ghcr.io`,
Harbor) still reads the docker config under `cloud`:

```yaml
registry:
  url: 123456789012.dkr.ecr.eu-west-1.amazonaws.com/dags
  auth_method: cloud
```

`env` and `iam`, accepted by older files, still load, print a warning and read
as `auto`, so a `docker login` made for them keeps working.

`local-daemon` is different: `docker`, `podman` and `nerdctl` push with
their own credentials, which a cloud session alone does not give them. When
the daemon builds, sign the tool in once with the command the deploy
preflight, `dexaflow doctor` and a refused push name for your registry:

| Registry | docker | another tool (`podman` shown) |
|---|---|---|
| Artifact Registry, GCR | `gcloud auth configure-docker <host>` | `gcloud auth print-access-token \| podman login --username oauth2accesstoken --password-stdin <host>` |
| ECR | `aws ecr get-login-password --region <region> \| docker login --username AWS --password-stdin <host>` | the same, piped to `podman login` |
| ACR | `az acr login --name <name>` | `az acr login --name <name> --expose-token --output tsv --query accessToken \| podman login --username 00000000-0000-0000-0000-000000000000 --password-stdin <host>` |
| any other (GHCR, Docker Hub, Harbor) | `dexaflow registry login <host>` | `podman login <host>` |

### Signing in without docker

For a registry no cloud owns, `dexaflow registry login` signs in with no
docker installed. It checks the credential with the registry first (the
same handshake a push makes), then stores it in the docker config, or in the
`credsStore` or `credHelpers` helper the config names (the system keychain),
as `docker login` does. The CLI's registry client reads it from there, and
so do docker, podman and nerdctl, so a `local-daemon` build pushes with it
too ([ADR 0091](/project/adrs/0091-registry-login-without-docker/)).

- **GHCR.** With the [GitHub CLI](https://cli.github.com) signed in
  (`gh auth login`), no token is needed: the CLI exchanges the `gh` session
  (`gh auth token`, and the user from `gh api user`).

  ```bash
  dexaflow registry login ghcr.io
  ```

  A push needs the `write:packages` scope, which `gh auth login` does not
  ask for by default: `gh auth refresh -h github.com -s write:packages`.
  In CI, pass a token instead (`GITHUB_TOKEN` with `packages: write`):

  ```bash
  echo "$GITHUB_TOKEN" | dexaflow registry login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
  ```

- **Docker Hub.** Create a personal access token with read and write
  access (Account settings, Personal access tokens) and pass it on stdin:

  ```bash
  echo "$DOCKERHUB_TOKEN" | dexaflow registry login docker.io --username <user> --password-stdin
  ```

- **Any other registry** (Harbor, Nexus, a self-hosted one) takes
  `--username` and its password or token on stdin the same way.

The token is read from stdin only, never from a flag, so it stays out of
`ps` and the shell history, and it is never printed. A credential the
registry refuses is not stored. The docker config is replaced atomically
and stays readable by its owner only; when `config.json` is a symlink, the
link is replaced by a file and the file it pointed at is left as it was.
For Artifact Registry, GCR, ECR and ACR, sign in to the cloud's CLI
instead, as above.

Only a registry on `localhost`, a `*.localhost` name (as k3d names its
registries) or a loopback address may answer over plain HTTP; every other
one, private addresses included, is read over verified TLS, and a registry
that answers only HTTP there is refused rather than sent the credentials in
clear. `*.localhost` names are dialed on `127.0.0.1` and never sent through a
proxy.

podman users: the CLI reads `~/.docker/config.json`, and podman's
`auth.json` only when that file does not exist. If you have both, run
`dexaflow registry login` for the registry too, or the push succeeds and the read is
refused with a 401. When that read fails after a push, `dexaflow deploy`
stops, and `dexaflow compile --build --push` keeps the tag in `dag.json` with
a warning (see [the explicit steps](/operate/cicd-deploy/)).

On a machine whose architecture differs from the target platform (an Apple
Silicon laptop building `linux/amd64`), the tool builds under emulation, which
is slower; Docker Desktop includes it, and Colima, Lima or podman may need a
one-time `docker run --privileged tonistiigi/binfmt --install all`.

`deploy --skip-build` registers an image that is already in the registry,
whoever pushed it: a CI job, a cloud builder or another machine. It reads the
digest from the registry and needs no container tool at all.

A project that ships its own `Dockerfile` (or `build.dockerfile` in
`dexaflow.yaml`) is built from it as-is: on
[Cloud Build](#building-on-cloud-build) when a remote builder is configured,
otherwise by the local daemon.
