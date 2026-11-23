---
title: "Argo, Tekton, Jenkins, buildah and kaniko"
linkTitle: "Other CI and builders"
weight: 40
description: "Deploy recipes for Argo Workflows, Tekton, Jenkins, buildah and kaniko."
---

Recipes for the systems `dexaflow ci init` does not write a file for. Each one runs the same [`dexaflow deploy`](/operate/cicd-deploy/).

## Recipes for Argo Workflows, Tekton, Jenkins, buildah and kaniko

`dexaflow ci init` writes the pipeline for GitHub, GitLab and Cloud Build. On
another CI system, run the script that `dexaflow ci init generic` writes,
`ci/dexaflow-deploy.sh`, in the [`dexaflow-cli` image](/operate/cicd-deploy/credentials/#the-dexaflow-cli-image).
It does what the generated pipelines do: it deploys the DAGs changed since a
base commit, prints a summary of the report, and fails when a DAG fails to
deploy. The recipes below run it on Argo Workflows, Tekton and Jenkins, and
show buildah and kaniko for the DAGs whose image needs a real build.

Set up once, for every recipe:

1. Write the script at the top of the DAG repository and commit it:

   ```bash
   dexaflow ci init generic
   ```

   The script pins the CLI version that wrote it. Run it in the
   `dexaflow-cli` image of that same version: the script then finds the CLI on
   `PATH` and downloads nothing (the image has no `curl` to download with).
   After upgrading the CLI, run `dexaflow ci init generic --force` and move the
   image tag in the same commit.
2. Store a [deploy token](/operate/cicd-deploy/credentials/#credentials-for-the-control-plane) as a Kubernetes
   Secret (for Jenkins, as a credential, shown in its tab):

   ```bash
   kubectl create secret generic dexaflow-deploy-token --from-literal=token="$DEPLOY_TOKEN"
   ```

3. Store the registry credential as a Secret of type
   `kubernetes.io/dockerconfigjson`. The recipes mount it as `config.json` and
   point `DOCKER_CONFIG` at its folder, which the CLI reads like
   `~/.docker/config.json` ([Registry credentials](/operate/builders/#registry-credentials)):

   ```bash
   kubectl create secret docker-registry dexaflow-registry \
     --docker-server=registry.example.com --docker-username=ci --docker-password="$REGISTRY_PASSWORD"
   ```

   The image carries no `gcloud`, `aws` or `az`, so it cannot read a cloud
   session: for Artifact Registry, ECR or ACR, store a credential the registry
   takes as a password (a service account key, an ACR token), and renew one
   that expires (an ECR password lasts twelve hours).

**Which builder runs.** The CLI picks the [builder](/operate/builders/) as
it does on a laptop. The `dexaflow-cli` image has `uv` and no container tool,
so a deploy from it takes `skip`, `code-layer` or `dependency-layer`, with
no daemon and no privileged pod. A DAG whose image needs a real build
(`system_packages`, a package without a wheel, its own `Dockerfile`) fails
at the build there: the [Cloud Build remote builder](/operate/builders/#building-on-cloud-build)
runs `gcloud`, which the image does not carry. The buildah and kaniko tabs
build such a DAG in the cluster; an image of your own with `gcloud` added
can use the remote builder instead.

**Which commit to compare with.** The script reads the base commit from
`DEXAFLOW_BASE_REF`. The Argo Workflows and Tekton recipes take it as a
parameter, the commit the branch was on before the push; the Jenkins recipe
passes the commit of the last successful build. Empty, all zeros or missing
from the clone deploys every DAG, and a DAG whose content is already the
current version registers nothing. A run that failed is not caught up by the
next one when the base is the previous push: see
[Deploying only what changed](/operate/cicd-deploy/workflows/#deploying-only-what-changed) for a ref that
moves only after a successful deploy.

{{< tabpane text=true >}}
{{% tab header="Argo Workflows" %}}

A `WorkflowTemplate` that clones the repository at the commit to deploy and
runs the script. The mutex (Argo Workflows 3.6 or later) runs one deploy at a
time.

<!-- ci-recipe: argo -->
```yaml title="dexaflow-deploy-workflowtemplate.yaml"
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: dexaflow-deploy
spec:
  entrypoint: deploy
  arguments:
    parameters:
      - name: revision # the commit to deploy
      - name: base-ref # the commit deployed before it; empty deploys every DAG
        value: ""
  synchronization:
    mutexes:
      - name: dexaflow-deploy
  volumes:
    - name: registry-auth
      secret:
        secretName: dexaflow-registry
        items:
          - key: .dockerconfigjson
            path: config.json
  templates:
    - name: deploy
      container:
        image: ghcr.io/dexadata/dexaflow-cli:<VERSION>
        command: [sh, -c]
        args:
          - |
            set -eu
            git clone --quiet "$REPOSITORY" /tmp/repo
            git -C /tmp/repo checkout --quiet "$REVISION"
            sh /tmp/repo/ci/dexaflow-deploy.sh
        env:
          - name: REPOSITORY
            value: https://github.com/acme/dags
          - name: REVISION
            value: "{{workflow.parameters.revision}}"
          - name: DEXAFLOW_BASE_REF
            value: "{{workflow.parameters.base-ref}}"
          - name: DEXAFLOW_SERVER_URL
            value: https://cluster.example.com
          - name: DEXAFLOW_TOKEN
            valueFrom:
              secretKeyRef:
                name: dexaflow-deploy-token
                key: token
          - name: DOCKER_CONFIG
            value: /etc/dexaflow/docker
        volumeMounts:
          - name: registry-auth
            mountPath: /etc/dexaflow/docker
            readOnly: true
```

Run it for a commit:

```bash
argo submit --from workflowtemplate/dexaflow-deploy \
  -p revision="$NEW_COMMIT" -p base-ref="$PREVIOUS_COMMIT"
```

With Argo Events, a Sensor on the repository's push webhook passes the push's
`after` commit as `revision` and its `before` commit as `base-ref`. A private
repository needs a credential in the clone, for example a read-only token
from another Secret in the `REPOSITORY` URL.

{{% /tab %}}
{{% tab header="Tekton" %}}

A `Task` that clones the repository and runs the script, and a `TaskRun`
that starts it. The parameters reach the script as environment variables, so
a value is never pasted into the script's text.

<!-- ci-recipe: tekton -->
```yaml title="dexaflow-deploy-task.yaml"
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: dexaflow-deploy
spec:
  params:
    - name: repository
      type: string
    - name: revision # the commit to deploy
      type: string
    - name: base-ref # the commit deployed before it; empty deploys every DAG
      type: string
      default: ""
  workspaces:
    - name: source
  volumes:
    - name: registry-auth
      secret:
        secretName: dexaflow-registry
        items:
          - key: .dockerconfigjson
            path: config.json
  steps:
    - name: deploy
      image: ghcr.io/dexadata/dexaflow-cli:<VERSION>
      workingDir: $(workspaces.source.path)
      env:
        - name: REPOSITORY
          value: $(params.repository)
        - name: REVISION
          value: $(params.revision)
        - name: DEXAFLOW_BASE_REF
          value: $(params.base-ref)
        - name: DEXAFLOW_SERVER_URL
          value: https://cluster.example.com
        - name: DEXAFLOW_TOKEN
          valueFrom:
            secretKeyRef:
              name: dexaflow-deploy-token
              key: token
        - name: DOCKER_CONFIG
          value: /etc/dexaflow/docker
      volumeMounts:
        - name: registry-auth
          mountPath: /etc/dexaflow/docker
          readOnly: true
      script: |
        #!/bin/sh
        set -eu
        git clone --quiet "$REPOSITORY" repo
        git -C repo checkout --quiet "$REVISION"
        sh repo/ci/dexaflow-deploy.sh
---
apiVersion: tekton.dev/v1
kind: TaskRun
metadata:
  generateName: dexaflow-deploy-
spec:
  taskRef:
    name: dexaflow-deploy
  params:
    - name: repository
      value: https://github.com/acme/dags
    - name: revision
      value: "<the commit to deploy>"
    - name: base-ref
      value: "<the commit deployed before it>"
  workspaces:
    - name: source
      emptyDir: {}
```

Create the `TaskRun` with `kubectl create -f` (it has a `generateName`), or
from a Tekton Triggers `TriggerTemplate` that passes the push's `after` and
`before` commits. Tekton runs two `TaskRun`s at once if two pushes arrive
together, so start them from one trigger that queues, or accept that two
deploys of different commits may overlap. With a `PersistentVolumeClaim` as
the workspace instead of `emptyDir`, give the pod `fsGroup: 65532` (in the
`TaskRun`'s `podTemplate.securityContext`) so the image's user can write the
clone.

{{% /tab %}}
{{% tab header="Jenkins" %}}

A `Jenkinsfile` for a multibranch pipeline on the
[Kubernetes plugin](https://plugins.jenkins.io/kubernetes/). The agent
checks the repository out, and the `dexaflow-cli` container runs the script
in the same workspace. The base is `GIT_PREVIOUS_SUCCESSFUL_COMMIT`, the
commit of the last successful build of the branch, so a failed build is
caught up by the next one. `BUILD_URL` is recorded as the version's
[`pipeline_url`](/operate/cicd-deploy/workflows/#where-a-version-was-built).

Set up once: the credentials `dexaflow-deploy-token` (secret text, the deploy
token) and `dexaflow-registry` (username and password for the registry), and,
in the branch source, the clone behaviours that fetch tags and do not clone
shallow (`git describe` and `--changed-since` need the history).

<!-- ci-recipe: jenkins -->
```groovy title="Jenkinsfile"
// Deploys the DAGs changed on main since the last successful build to Dexaflow.
pipeline {
  agent {
    kubernetes {
      yaml '''
        apiVersion: v1
        kind: Pod
        spec:
          securityContext:
            runAsUser: 1000 # the user of the agent container, which writes the checkout
            runAsGroup: 1000
          containers:
            - name: dexaflow
              image: ghcr.io/dexadata/dexaflow-cli:<VERSION>
              command: ["sleep"]
              args: ["infinity"]
      '''
    }
  }
  options {
    disableConcurrentBuilds()
    skipDefaultCheckout()
  }
  environment {
    DEXAFLOW_SERVER_URL = 'https://cluster.example.com'
  }
  stages {
    stage('Deploy DAGs') {
      when { branch 'main' }
      steps {
        script {
          def scmVars = checkout scm
          env.DEXAFLOW_BASE_REF = scmVars.GIT_PREVIOUS_SUCCESSFUL_COMMIT ?: ''
        }
        container('dexaflow') {
          withCredentials([
            string(credentialsId: 'dexaflow-deploy-token', variable: 'DEXAFLOW_TOKEN'),
            usernamePassword(credentialsId: 'dexaflow-registry', usernameVariable: 'REGISTRY_USER', passwordVariable: 'REGISTRY_PASSWORD')
          ]) {
            sh '''
              export HOME=/tmp/dexaflow-home # the image's home belongs to its own user, 65532
              mkdir -p "$HOME/.docker"
              auth="$(printf '%s:%s' "$REGISTRY_USER" "$REGISTRY_PASSWORD" | base64 -w0)"
              printf '{"auths":{"registry.example.com":{"auth":"%s"}}}' "$auth" >"$HOME/.docker/config.json"
              sh ci/dexaflow-deploy.sh
            '''
          }
        }
      }
    }
  }
}
```

The shell steps are in single quotes, so Groovy never interpolates a secret
into the command line. On an agent outside Kubernetes, with Python 3.11 to
3.13, git and curl, `sh ci/dexaflow-deploy.sh` installs the pinned CLI
itself, and the same stage works without the `container` block.

{{% /tab %}}
{{% tab header="buildah" %}}

For DAGs whose image needs a real build and no remote builder: the deploy
runs next to [buildah](https://buildah.io/), and `--builder buildah` makes
buildah the local tool of the [fallback](/operate/builders/#the-local-daemon-fallback).
The CLI still generates the Dockerfile from `dexaflow.yaml` and hands it to
`buildah build`, then `buildah push`; the earlier builders still apply first,
so a DAG with a dependency image in the registry only pushes its code layer.

buildah needs no daemon, but it runs as root in its container, with the
`vfs` storage driver and `chroot` isolation. The step installs buildah on
the Debian image the GitLab job runs on, so the CLI has Python 3.12 for
`compile`. Whether a `RUN` step of the build (the `apt-get` of
`system_packages`, the `pip install`) works without `privileged: true`
depends on the cluster's runtime and policies; many clusters allow a
privileged or root pod only in a namespace kept for builds.

<!-- ci-recipe: buildah -->
```yaml title="dexaflow-deploy-buildah-task.yaml"
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: dexaflow-deploy-buildah
spec:
  params:
    - name: repository
      type: string
    - name: revision
      type: string
    - name: base-ref
      type: string
      default: ""
  workspaces:
    - name: source
  volumes:
    - name: registry-auth
      secret:
        secretName: dexaflow-registry
        items:
          - key: .dockerconfigjson
            path: config.json
  steps:
    - name: deploy
      image: python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
      workingDir: $(workspaces.source.path)
      securityContext:
        runAsUser: 0
      env:
        - name: REPOSITORY
          value: $(params.repository)
        - name: REVISION
          value: $(params.revision)
        - name: DEXAFLOW_BASE_REF
          value: $(params.base-ref)
        - name: DEXAFLOW_SERVER_URL
          value: https://cluster.example.com
        - name: DEXAFLOW_TOKEN
          valueFrom:
            secretKeyRef:
              name: dexaflow-deploy-token
              key: token
        - name: DOCKER_CONFIG # the CLI reads the registry credential here
          value: /etc/dexaflow/docker
        - name: REGISTRY_AUTH_FILE # and buildah reads the same file
          value: /etc/dexaflow/docker/config.json
        - name: STORAGE_DRIVER
          value: vfs
        - name: BUILDAH_ISOLATION
          value: chroot
      volumeMounts:
        - name: registry-auth
          mountPath: /etc/dexaflow/docker
          readOnly: true
      script: |
        #!/bin/sh
        set -eu
        apt-get update -qq
        apt-get install -y -qq --no-install-recommends buildah git curl ca-certificates >/dev/null
        git clone --quiet "$REPOSITORY" repo
        git -C repo checkout --quiet "$REVISION"
        sh repo/ci/dexaflow-deploy.sh install # the CLI the script pins, checked against the release checksums
        PATH="$HOME/.dexaflow/bin:$PATH"
        cd repo
        set --
        case "$DEXAFLOW_BASE_REF" in
        *[!0]*) set -- --changed-since "$DEXAFLOW_BASE_REF" ;;
        esac
        dexaflow deploy --all "$@" --builder buildah --yes
```

Start it with a `TaskRun` like the one in the Tekton tab, with
`taskRef.name: dexaflow-deploy-buildah`. The same script works as an Argo
Workflows container with `securityContext.runAsUser: 0`.

Notes:

- **One platform per build.** Set one entry in `build.platforms`. With
  several, the CLI checks that the pushed tag is a manifest list holding
  every platform and fails the deploy otherwise; buildah makes a manifest
  list only with `--manifest`, which the CLI does not pass.
- **One credential file.** buildah reads `REGISTRY_AUTH_FILE`, the CLI reads
  `$DOCKER_CONFIG/config.json`; pointing both at the mounted Secret keeps
  the push and the digest read on the same credential.
- The script's own summary and its `--changed-since` fallbacks are not used
  here, since the script does not pass `--builder`: a base missing from the
  clone stops the deploy instead of deploying every DAG.

{{% /tab %}}
{{% tab header="kaniko" %}}

kaniko builds the Dockerfile it is given. The CLI does not hand kaniko the
Dockerfile it generates from `dexaflow.yaml`, so kaniko fits a project that
ships [its own Dockerfile](/operate/first-cluster-dag/#advanced-your-own-dockerfile)
(the escape hatch of [ADR 0003](/project/adrs/0003-dag-as-image/)). For a
project built from `dexaflow.yaml` alone, use the daemonless builders of the
other tabs, or buildah.

The `Task` takes the explicit steps: `dexaflow compile` writes `dag.json` and
names the image from the `registry:` block, kaniko builds and pushes that
image and writes the digest it pushed, and `dexaflow push` registers the
version with the image pinned to that digest. kaniko runs as root in its own
image and needs no privileged pod.

<!-- ci-recipe: kaniko -->
```yaml title="dexaflow-kaniko-task.yaml"
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: dexaflow-deploy-kaniko
spec:
  params:
    - name: repository
      type: string
    - name: revision
      type: string
    - name: dag-dir # the project to deploy, such as dags/etl
      type: string
  workspaces:
    - name: source
  volumes:
    - name: registry-auth
      secret:
        secretName: dexaflow-registry
        items:
          - key: .dockerconfigjson
            path: config.json
  steps:
    - name: compile
      image: ghcr.io/dexadata/dexaflow-cli:<VERSION>
      workingDir: $(workspaces.source.path)
      env:
        - name: REPOSITORY
          value: $(params.repository)
        - name: REVISION
          value: $(params.revision)
        - name: DAG_DIR
          value: $(params.dag-dir)
      script: |
        #!/bin/sh
        set -eu
        git clone --quiet "$REPOSITORY" repo
        git -C repo checkout --quiet "$REVISION"
        mkdir -p out
        dexaflow compile "repo/$DAG_DIR" -o out/dag.json
        python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["image"])' out/dag.json >out/image
    - name: build
      image: gcr.io/kaniko-project/executor:debug # pin the executor you trust by digest
      workingDir: $(workspaces.source.path)
      env:
        - name: DAG_DIR
          value: $(params.dag-dir)
        - name: DOCKER_CONFIG
          value: /kaniko/.docker
      volumeMounts:
        - name: registry-auth
          mountPath: /kaniko/.docker
          readOnly: true
      script: |
        #!/busybox/sh
        set -eu
        /kaniko/executor --context="dir://$PWD/repo/$DAG_DIR" --dockerfile=Dockerfile \
          --destination="$(cat out/image)" --digest-file=out/digest --custom-platform=linux/amd64
    - name: register
      image: ghcr.io/dexadata/dexaflow-cli:<VERSION>
      workingDir: $(workspaces.source.path)
      env:
        - name: DEXAFLOW_SERVER_URL
          value: https://cluster.example.com
        - name: DEXAFLOW_TOKEN
          valueFrom:
            secretKeyRef:
              name: dexaflow-deploy-token
              key: token
      script: |
        #!/bin/sh
        set -eu
        image="$(cat out/image)"
        repo="$image"
        case ${image##*/} in *:*) repo=${image%:*} ;; esac # drop the tag, never a registry port
        dexaflow push out/dag.json --image "$repo@$(cat out/digest)"
```

Notes:

- **The digest is kaniko's.** `--digest-file` holds the digest kaniko
  pushed, and `push --image <repo>@sha256:...` registers it as given, so a
  later push of the same tag cannot change what is registered.
- **One DAG per run.** `deploy --all --changed-since` does not drive this
  path: start one `TaskRun` per project that changed.
- **The upstream kaniko repository is archived** (2025). Use the executor
  image of a maintained fork your organization trusts, pinned by digest; the
  flags above are the executor's.
- `--custom-platform` must match `build.platforms`; kaniko builds one
  platform per run.

{{% /tab %}}
{{< /tabpane >}}
