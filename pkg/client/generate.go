// Package client is the typed, generated client for the Dexaflow (Airflow-compatible)
// /api/v2 surface. It is the single control-plane client shared by the CLI, the
// MCP server, and the smoke tests (ADR 0050 D8) — no component hand-rolls HTTP
// against /api/v2, and nothing here imports internal/ packages.
//
// client.gen.go is generated from docs/api/openapi.yaml. Do not edit it by hand;
// run `make pkg-client` from the repo root after changing the spec (the
// oapi-codegen config's output path is repo-root-relative, so generation must run
// from there, not via `go generate` in this package). CI catches drift with
// `make pkg-client-check`.
package client
