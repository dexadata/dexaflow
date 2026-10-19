---
title: "ADR 0083: the remote builder runs on Cloud Build first, through the gcloud CLI"
linkTitle: "0083 · Remote builder on Cloud Build"
weight: 830
description: "ADR 0083: the remote path of ADR 0073. When a build needs a real Dockerfile build (a package without wheels, system_packages, a project Dockerfile) and build.remote_builder (or the target's or workspace's remote_builder) names a Cloud Build project, the CLI packs the build context itself with the .dockerignore applied, submits it with gcloud builds submit --async, follows it, lets Cloud Build push, and registers the digest the registry holds once it matches the one the build reports. No cloud SDK is linked. The in-cluster build pod is the next backend behind the same seam."
---

**Status:** Accepted
**Date:** 2026-10-14
**Relates:** ADR 0073 (daemonless image builds, path 4), ADR 0075 (0.5.4 scope, the registry is the source of the digest), ADR 0077 (dependency image and code layer), ADR 0078 (dependency image from wheels), ADR 0080 (cloud CLI sessions), ADR 0048 (no user code in the control plane), ADR 0015 (no Docker SDK)
**Issues:** #1706

## Context

ADR 0073 put a remote builder at path 4, between the daemonless dependency
layer and the local daemon: an in-cluster build pod the chart offers, or a
configured cloud builder, Cloud Build first. Until it existed, every build
the first three paths could not take (a package without a wheel,
`system_packages`, a project `Dockerfile`) needed a container daemon on the
machine that deploys, which the maintainers decided is a fallback and never
the expected path. ADR 0075 moved path 4 into 0.5.4.

The two backends differ in what they need. The in-cluster build pod needs a
chart change, a namespace, a service account, limits and an API through
which the CLI hands it the context and follows its logs. Cloud Build needs
none of that: the CLI of the cloud is already the way people sign in to it,
and ADR 0080 already reads registry tokens from that same session.

## Decision

1. **Cloud Build is the first backend of the remote path.** It is chosen,
   in the order of ADR 0073, when no earlier path applies and a remote
   builder is configured. It builds the whole image of a build that is not
   split (a project `Dockerfile`, an unpinned dependency without uv, and the
   other reasons the builder line names), and the dependency image of a
   split build when the wheels do not apply or fail (ADR 0077, ADR 0078), so
   the code layer is still appended through the registry. The in-cluster
   build pod is the next backend; it implements the same `build.Remote`
   interface (a builder that says whether it can run here and whether it
   builds the requested platforms) and changes no caller.
2. **Configuration, not detection.** `build.remote_builder.cloud_build` in
   `dexaflow.yaml`, then `remote_builder` on the target, then in the
   workspace `defaults`, names the project (required) and optionally the
   region, a private worker pool and the service account the build runs as.
   The builder line names the source. A Google Cloud registry alone does not
   select Cloud Build: where the build runs is a cost and a trust decision.
   The block is not an input of the image, so it is left out of the content
   label, as `registry.auth_method` is (ADR 0080).
3. **The gcloud CLI, no SDK, GA commands only.** The CLI runs `gcloud builds
   submit <archive> --config=<file> --project=<p> [--region] [--worker-pool]
   [--service-account] --async --format=json --quiet` with no input, and
   prints the build id and its console link. It then follows the build:
   `gcloud builds log --stream` while the build logs to Cloud Storage (the
   default), then `gcloud builds describe` for the outcome. A build with a
   service account of its own must log to Cloud Logging only, which the GA
   `builds log` and a synchronous `builds submit` cannot show; the beta
   component that can is not installed with gcloud by default. For such a
   build the CLI prints the `gcloud logging read` command of its log and
   describes the build until it ends. A failed build names its id, its
   status and that command. No Google Cloud module is linked, so ADR 0073's
   list of allowed packages stands. Every configured value is validated when
   the file loads (a worker pool must be in the configured region) and
   passed as `--flag=value`, so none can be read as another flag.
   A stopped command sends gcloud SIGINT, not SIGKILL, then runs `gcloud
   builds cancel`, so the build does not go on and push. A failed describe
   is retried a bounded number of times with a doubling wait, and a build
   still not described is canceled the same way, its id and cancel command
   in the error. The id gcloud prints must look like a build id (a UUID)
   before it goes into another command, and a stop before gcloud printed it
   points to `gcloud builds list --filter=tags=dexaflow --ongoing`.
4. **The CLI packs the context.** The context leaves the machine, so it is
   listed by the CLI with the `.dockerignore` the build reads (the project's
   own lines, `exclude_paths` and the dbt artifacts, with Docker's rules) and
   packed as a gzipped tar with fixed dates and normalized modes, plus the
   Dockerfile under a name of its own; a `<Dockerfile>.dockerignore` next to
   a project Dockerfile is read instead of `.dockerignore`, as BuildKit
   does, and the archive is refused once its written bytes pass 2 GiB. The archive stays in the
   project's source bucket (`gs://<project>_cloudbuild/source/`, with ':'
   and '.' of a domain-scoped project id turned into '_', as gcloud does), so a
   secret-looking file in it is warned about whatever the Dockerfile copies.
   gcloud never walks the directory: its
   `.gcloudignore` and `.gitignore` rules are not Docker's, and a directory
   upload would send what `exclude_paths` keeps out. A dependency image is
   built from its Dockerfile alone, so no project file is uploaded for it.
5. **Cloud Build pushes; the registry gives the digest.** The build config
   has one step, `gcr.io/cloud-builders/docker` running `docker build
   --platform`, and lists the tag under `images`, so Cloud Build pushes with
   its own service account, which Artifact Registry and GCR accept; for any
   other registry host the CLI notes that the push may be refused. The
   digest is then read from the registry with the client and credentials of
   ADR 0073 and ADR 0080, as for every other path (ADR 0075), and must equal
   the digest the build reports for the image (`results.images`): a tag
   pushed again in between (the race ADR 0075 records) stops the deploy
   instead of registering the other image. A build with a service account
   of its own sets `CLOUD_LOGGING_ONLY`, which Cloud Build requires for one.
6. **One platform, linux/amd64.** Cloud Build's default workers are amd64,
   and this backend sets up no emulation. Another platform, or several, is
   declined with the reason on the builder line, and the build goes on to
   the local daemon.
7. **Declining and failing.** A remote builder is passed over, with the
   reason on the builder line, when the build is not pushed (Cloud Build
   pushes what it builds), when it declines the platforms, and when gcloud
   is not on `PATH` while the daemon's tool is. With neither on `PATH` it is
   kept, so the error names what was configured and how to install gcloud.
   A failed submit keeps gcloud's last line and names the sign-in, the
   project and the roles to check (`roles/cloudbuild.builds.editor` for the
   account, push access for the Cloud Build service account).
8. **The push preflight.** With a remote builder configured, a refusal of
   this machine's push credential is decided once the builder is chosen:
   a whole build on the remote builder pushes nothing from here, so the
   refusal is a note; every other builder (the code layer, including after
   a remote dependency image, and the local daemon) pushes from here, so
   the refusal stops the deploy before anything is built, as without a
   remote builder.

## Consequences

- A project with `system_packages`, a package without wheels or its own
  `Dockerfile` deploys from a machine with no container daemon, signed in
  with `gcloud auth login`, to an amd64 cluster.
- The build context of a whole build is uploaded to the project's Cloud
  Build source bucket; what reaches it is what a local `docker build` would
  read, so `.git` and other files go only when the `.dockerignore` keeps
  them, and it stays there until a lifecycle rule or a person deletes it.
- The image must be in Artifact Registry or GCR, and the build's service
  account needs pull access to a private base image. A split build still
  pushes its code layer from the deploying machine, so that account needs
  push access too; with Artifact Registry or GCR its gcloud session is the
  credential (ADR 0080).
- A build with its own service account shows no log in the terminal, only
  the command that reads it.
- The build step image is Google's `gcr.io/cloud-builders/docker` by tag.
  Pinning it, build arguments, build secrets and a private package index are
  follow-ups (#1706 item 6).

  > **Note (2026-10, #1884).** Build arguments now reach the Cloud Build step;
  > a build with build secrets or a package index does not go to Cloud Build
  > and builds on the local machine instead. See
  > [ADR 0088](/project/adrs/0088-build-args-secrets-and-package-index/).
- Builds on arm64 or for several platforms keep the local daemon until the
  in-cluster build pod or a multi-platform Cloud Build step lands.

  > **Note (2026-10, #1883).** The in-cluster backend shipped as
  > `remote_builder.buildkit`: the rootless BuildKit daemon the Helm chart runs
  > (`builder.enabled`), reached with `buildctl --addr kube-pod://` over
  > `kubectl exec` as the person who deploys, with no server API. It builds
  > one platform, its node's, and the chart advertises it in the cluster
  > deploy defaults. See
  > [Builders](/operate/builders/#building-on-the-in-cluster-builder).
- Cloud Build is billed to the configured project, and a build waits for a
  worker; a code-only change still takes the code-layer path and never
  reaches Cloud Build.

## Alternatives considered

- **The Cloud Build Go client.** It links the Google Cloud SDK into the CLI,
  which ADR 0073 and ADR 0080 keep out.
- **`gcloud builds submit --tag` on the directory.** It needs a file named
  `Dockerfile` at the root, cannot take a platform, and uploads the
  directory with gcloud's ignore rules instead of Docker's.
- **Kaniko on Cloud Build** (`builds/use_kaniko`). It changes how a project
  Dockerfile is interpreted, and its layer cache is a second cache next to
  the dependency image.
- **The in-cluster build pod first.** It needs the chart and a server API
  that do not exist yet, and a build pod in the cluster is a larger security
  review; Cloud Build ships the path now, behind the same seam.
