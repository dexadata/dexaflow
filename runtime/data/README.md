# runtime/data

The package list of the **data variant** of the task image,
`ghcr.io/dexadata/dexaflow-runtime:py3.11-data-<tag>`: the base image
(`runtime/Dockerfile`, stage `runtime-base`) plus Airflow providers (Google's
included) and dbt, for a platform that runs DAGs on the runtime
image as is, with no image build (source mode, ADR 0067).

- `requirements.in`: the top-level packages. Edit this file.
- `requirements.txt`: the lock the image installs with `pip --require-hashes`.
  Generated; never edit it by hand.

## Recompile the lock

```sh
uv pip compile --python-version 3.11 --python-platform x86_64-manylinux_2_28 \
  --generate-hashes --no-header --emit-index-url \
  runtime/data/requirements.in -o runtime/data/requirements.txt
```

The image is built for amd64 and arm64 from this one lock. After a recompile,
check that arm64 resolves to the same versions:

```sh
uv pip compile --python-version 3.11 --python-platform aarch64-manylinux_2_28 \
  --no-header runtime/data/requirements.in -c runtime/data/requirements.txt
```

Its output should list exactly the versions of `requirements.txt`.

## Rules

- **Python 3.11 only.** The lock is resolved for 3.11, and the build fails on
  any other `PYTHON_VERSION`.
- **The base's pins come first.** `requirements.in` repeats the Task SDK,
  certifi and setuptools pins of `runtime/Dockerfile`; bump them in both.
- **Mind the size.** `apache-airflow-providers-google` alone pulls ray,
  litellm, scipy and pyarrow, and roughly doubles the image (about 1.6 GB
  installed). Check `du` and the advisories (`pip-audit -r requirements.txt`)
  before adding another provider.
- **Every promised import is checked at build time.** The `runtime-data` stage
  of `runtime/Dockerfile` imports each provider, driver and dbt adapter, and
  runs `dbt --version`. When you add a package here, add its import there.
