---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /reference.html
# --- end AUTO redirect aliases ---
title: Reference
linkTitle: Reference
weight: 60
description: References for every Dexaflow surface — the HTTP API, CLI, Go packages, Python runtime, configuration, and MCP server.
cascade: { type: docs }
menu:
  main:
    weight: 60
---

References for every Dexaflow surface. The HTTP API, CLI, Go, and Python references
are **generated from source on every push**, so they never drift from the code. The
[Configuration](/reference/configuration/) page is hand-maintained against
`internal/config` — treat the server source as the final authority.

<div class="lf-cards">
  <a class="lf-card" href="/reference/api/">
    <span class="lf-card__icon"><i class="fa-solid fa-code"></i></span>
    <span class="lf-card__title">HTTP API (Scalar)</span>
    <span class="lf-card__desc">The <code>/api/v2/</code> control-plane API — Airflow 3.2.x-compatible — as an interactive Scalar reference.</span>
    <span class="lf-card__more">Open the API reference →</span>
  </a>
  <a class="lf-card" href="/reference/cli/">
    <span class="lf-card__icon"><i class="fa-solid fa-terminal"></i></span>
    <span class="lf-card__title">CLI reference</span>
    <span class="lf-card__desc">Every <code>dexaflow</code> command and flag, generated from Cobra.</span>
    <span class="lf-card__more">CLI commands →</span>
  </a>
  <a class="lf-card" href="/reference/go/">
    <span class="lf-card__icon"><i class="fa-brands fa-golang"></i></span>
    <span class="lf-card__title">Go packages (GoDocs)</span>
    <span class="lf-card__desc">GoDocs for the control plane, scheduler, executor, agent, and storage — one page per package, each symbol linking to source.</span>
    <span class="lf-card__more">Go packages →</span>
  </a>
  <a class="lf-card" href="/reference/python-api/">
    <span class="lf-card__icon"><i class="fa-brands fa-python"></i></span>
    <span class="lf-card__title">Python runtime API</span>
    <span class="lf-card__desc">The <code>leoflow_runtime</code> helpers your DAG code imports (XCom, staging paths) — plus the full generated docstring reference.</span>
    <span class="lf-card__more">Python runtime →</span>
  </a>
  <a class="lf-card" href="/reference/configuration/">
    <span class="lf-card__icon"><i class="fa-solid fa-sliders"></i></span>
    <span class="lf-card__title">Configuration</span>
    <span class="lf-card__desc">The <code>DEXAFLOW_*</code> environment variables and config keys for the server.</span>
    <span class="lf-card__more">Configuration →</span>
  </a>
  <a class="lf-card" href="/mcp/">
    <span class="lf-card__icon"><i class="fa-solid fa-robot"></i></span>
    <span class="lf-card__title">MCP server</span>
    <span class="lf-card__desc">The Model Context Protocol server — read-only Tools and Resources that expose Dexaflow to AI agents.</span>
    <span class="lf-card__more">MCP server →</span>
  </a>
</div>

The [Glossary](/reference/glossary/) collects the core vocabulary (DAG, DagRun,
XCom, Executor, …).
