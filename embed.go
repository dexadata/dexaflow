// Package dexaflow embeds the Python parser and runtime package sources so a
// binary-only install (no source checkout) can provision them. The dev source
// trees under parser/ and runtime/python/ remain the canonical copies; this
// embed reads them directly at build time, so there is no duplicated source.
//
// The all: prefix is required on the package directories: Python packages
// contain __init__.py, and go:embed's default rules drop names beginning with
// "_" or ".". The patterns are scoped to the package dir plus pyproject.toml /
// README.md (what pip needs) so the dev-only test fixtures and dot-caches
// (.pytest_cache, .ruff_cache, .coverage) are not embedded. Build from a clean
// tree (no __pycache__) so stale bytecode is not embedded.
package dexaflow

import "embed"

// Five patterns across two directives, and runtime/python/dexaflow and
// runtime/python/leoflow (its pre-rename re-export) each have to be their own:
// all:runtime/python/leoflow_runtime cannot match them. Omitting it is silent
// — hatchling ships a wheel without a `packages` entry whose directory is absent,
// no error and no warning, so a binary-only Lite install would build every
// per-DAG venv from a pysrc tree with no authoring package in it (#17). The all:
// prefix is what carries files beginning with an underscore, which __init__.py
// does. .gitignore is named explicitly for the same reason — a directory walk
// skips dotfiles, and that one has to travel: hatchling resolves its ignore
// patterns by walking up from its own root, so without it a build from
// ~/.dexaflow/pysrc reaches $HOME's. embed_test.go asserts each of these arrives.
//
//go:embed all:parser/leoflow_parser parser/pyproject.toml parser/README.md
//go:embed all:runtime/python/leoflow_runtime all:runtime/python/dexaflow all:runtime/python/leoflow runtime/python/pyproject.toml runtime/python/README.md runtime/python/.gitignore
var pythonSources embed.FS

// PythonSources returns the embedded parser and runtime package sources, rooted
// so that "parser/..." and "runtime/python/..." resolve.
func PythonSources() embed.FS { return pythonSources }

//go:embed docker-compose.dev.yaml
var devCompose []byte

// DevCompose returns the embedded docker-compose for Leoflow Lite's local
// Postgres + Redis, so a binary-only install (no source checkout) can bring the
// datastores up with `leoflow lite` alone — it is materialized under ~/.dexaflow
// on first run.
func DevCompose() []byte { return devCompose }

//go:embed all:examples
var exampleDAGs embed.FS

// ExampleDAGs returns the embedded DAG examples (one subdirectory per DAG,
// each with dag.py + dexaflow.yaml). The Lite IDE's "Download examples"
// button materializes them into the user's workspace under examples/, so a
// fresh install can try every operator without a separate git checkout.
// Root is "examples/" — the same layout as the source tree.
func ExampleDAGs() embed.FS { return exampleDAGs }
