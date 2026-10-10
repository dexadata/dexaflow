"""
Single source of truth for the Dexaflow docs Hugo+Docsy migration (Phase F1).

Maps every source page under docs/ to its new home under website/content/, per the
approved 9-section UX-first IA. Drives BOTH file placement and internal-link
rewriting (a link's old target is resolved relative to its source file, then looked
up here to get the new Hugo URL).

`PAGES` is the copy-and-convert set (one source md -> one dest md). Merges, splits,
_index landings, stubs and Scalar are handled by hand in build_site.py; their URLs
are registered in EXTRA_URLS so links still resolve.
"""

# section dir -> nav weight (top navbar + sidebar order)
SECTIONS = {
    "get-started": 10,
    "author-dags": 20,
    "connections": 30,
    "operate": 40,
    "concepts": 50,
    "reference": 60,
    "contribute": 70,
    "project": 80,
}

# Straight copy+convert pages.
# key: source path relative to docs/
# val: dict(dest, title, link, weight, desc)
PAGES = {
    # ---- Get started ----
    "quickstart.md": dict(
        dest="get-started/quickstart.md", title="Quickstart", link="Quickstart",
        weight=10,
        desc="Get Dexaflow Lite running locally in two commands — the fastest path to a running DAG."),
    "installation.md": dict(
        dest="get-started/installation.md", title="Installation", link="Installation",
        weight=30, desc="Install the leoflow CLI and provision the managed Python runtime."),

    # ---- Author DAGs ----
    "dag-authoring.md": dict(
        dest="author-dags/dag-authoring.md", title="DAG authoring", link="DAG authoring",
        weight=10, desc="Author a DAG: dexaflow.yaml plus dag.py compiled to one immutable artifact."),
    "airflow-operators.md": dict(
        dest="author-dags/operators-sensors.md", title="Airflow operators & sensors",
        link="Operators & sensors", weight=20,
        desc="Use Airflow operators and sensors from your DAGs on Dexaflow."),
    "dbt.md": dict(
        dest="author-dags/dbt.md", title="dbt projects as DAGs", link="dbt",
        weight=30, desc="Render a dbt project into a Dexaflow DAG with native model-level tasks."),
    "variables-connections.md": dict(
        dest="author-dags/variables-connections.md", title="Variables & Connections",
        link="Variables & Connections", weight=40,
        desc="Expose Variables and Connections to your task pods."),
    "alerting.md": dict(
        dest="author-dags/alerting.md", title="On-failure alerting", link="Alerting",
        weight=50, desc="Notify on run failure from dexaflow.yaml — Slack or a generic webhook, no extra task and no Python."),
    "cookbook/map-reduce.md": dict(
        dest="author-dags/map-reduce.md", title="Map-reduce for ML", link="Map-reduce",
        weight=60, desc="Fan-out plus reduce as a Python list comprehension — native map-reduce for ML/AI."),
    "examples.md": dict(
        dest="author-dags/examples.md", title="Examples", link="Examples",
        weight=70, desc="Runnable example DAGs covering the common authoring patterns."),
    "etl-staging-case-study.md": dict(
        dest="author-dags/etl-case-study.md", title="Case study: 1 GB ETL on staging",
        link="ETL case study", weight=80,
        desc="A worked 1 GB ETL that shares data between tasks through the per-run staging volume."),
    "lite-web-editor.md": dict(
        dest="author-dags/lite-web-editor.md", title="The Lite web editor", link="Lite web editor",
        weight=90, desc="Edit and run DAGs from the browser in Dexaflow Lite."),

    # ---- Deploy & operate ----
    "first-pro-dag.md": dict(
        dest="operate/first-pro-dag.md", title="Deploy your first Pro DAG",
        link="Deploy your first Pro DAG", weight=10,
        desc="Take a DAG from Lite to a Kubernetes control plane — your first Pro deployment."),
    "deploy.md": dict(
        dest="operate/cicd-deploy.md", title="CI/CD & deploy examples", link="CI/CD & deploy",
        weight=20, desc="Build, push and register DAGs from CI — GitHub Actions, GitLab CI, Cloud Build."),
    "upgrades.md": dict(
        dest="operate/upgrades.md", title="Upgrades", link="Upgrades",
        weight=40, desc="Upgrade a Dexaflow control plane safely, edition by edition."),
    "backup-restore.md": dict(
        dest="operate/backup-restore.md", title="Backup & restore", link="Backup & restore",
        weight=50, desc="Back up and restore Dexaflow state — metadata, secrets, and logs."),
    "troubleshooting.md": dict(
        dest="operate/troubleshooting.md", title="Troubleshooting & observability",
        link="Troubleshooting", weight=60,
        desc="Diagnose DAG, scheduler and executor problems; where the logs and signals live."),
    "scheduler-resilience.md": dict(
        dest="operate/scheduler-resilience.md", title="Scheduler resilience", link="Scheduler resilience",
        weight=70, desc="How the scheduler survives restarts, leader loss and partial failure."),
    "warm-pools.md": dict(
        dest="operate/warm-pools.md", title="Warm worker pools", link="Warm pools",
        weight=80, desc="Cut task start latency with pre-warmed worker pods."),
    "agent-credential-transport.md": dict(
        dest="operate/agent-credential-transport.md", title="Agent credential transport",
        link="Agent credential transport", weight=90,
        desc="How declared secrets reach the in-container agent, and the trust boundary."),
    "pro-tls.md": dict(
        dest="operate/pro-tls.md", title="Pro TLS (cert-manager)", link="Pro TLS",
        weight=100, desc="Terminate TLS on the Pro control plane with cert-manager."),
    "staging-volume.md": dict(
        dest="operate/staging-volume.md", title="Staging volume", link="Staging volume",
        weight=110, desc="The ephemeral per-run volume that shares large data between tasks."),

    # ---- Concepts ----
    "architecture.md": dict(
        dest="concepts/architecture.md", title="Architecture", link="Architecture",
        weight=30, desc="The Go control plane, the split API/scheduler roles, and the execution data flow."),
    "ui-compatibility.md": dict(
        dest="concepts/ui-compatibility.md", title="UI compatibility", link="UI compatibility",
        weight=40, desc="What the Airflow-compatible UI supports, and where it intentionally differs."),

    # ---- Reference ----
    "configuration.md": dict(
        dest="reference/configuration.md", title="Configuration", link="Configuration",
        weight=60, desc="The LEOFLOW_* environment variables and config keys for the server."),
    "mcp.md": dict(
        dest="reference/mcp.md", title="MCP server", link="MCP server",
        weight=80, desc="The Dexaflow MCP server — resources and tools for agents."),

    # ---- Contribute ----
    "contributing.md": dict(
        dest="contribute/contributing.md", title="Contributing", link="Contributing",
        weight=10, desc="How to contribute to Dexaflow — workflow, standards, and the TDD gate."),

    # ---- Project ----
    "roadmap-to-release.md": dict(
        dest="project/roadmap.md", title="Roadmap", link="Roadmap",
        weight=20, desc="The historical road to release — where Dexaflow has been heading."),
    "planning/airflow-connector-compatibility.md": dict(
        dest="project/planning/airflow-connector-compatibility.md",
        title="Airflow 3.X connector compatibility", link="Connector compatibility",
        weight=10, desc="Planning note: Airflow 3.X connector compatibility."),
    "planning/connectors-two-tier-model.md": dict(
        dest="project/planning/connectors-two-tier-model.md",
        title="Connectors — two-tier model (ADR 0035 + shim)", link="Two-tier connectors",
        weight=20, desc="Planning note: the two-tier connector model."),
    "reverse-analysis-mvp.md": dict(
        dest="project/background/reverse-analysis-mvp.md", title="MVP reverse analysis",
        link="MVP reverse analysis", weight=10,
        desc="Background: reverse analysis of the MVP."),
    "ui-walk-report.md": dict(
        dest="project/background/ui-walk-report.md", title="UI walk report", link="UI walk report",
        weight=20, desc="Background: a walkthrough report of the UI."),
    "frontend-assessment.md": dict(
        dest="project/background/frontend-assessment.md", title="Frontend assessment",
        link="Frontend assessment", weight=30, desc="Background: assessment of the frontend."),
}

# ADR ordering label for the generated index (filled programmatically).
ADR_SECTION_WEIGHT = 10  # adrs subsection under Project

# Old source path -> new Hugo URL, for links that don't map 1:1 through PAGES
# (merges, splits, _index landings, stubs, Scalar, generated trees).
EXTRA_URLS = {
    "index.md": "/",
    "why-leoflow.md": "/why-leoflow/",
    # merges
    "editions.md": "/concepts/editions/",
    "operating-modes.md": "/concepts/editions/",
    "dev-workflow.md": "/contribute/local-dev-loop/",
    "local-deploy.md": "/contribute/local-dev-loop/",
    # split
    "concepts.md": "/concepts/core-concepts/",
    # section landings
    "reference.md": "/reference/",
    "adrs.md": "/project/adrs/",
    "connections/index.md": "/connections/",
    # generated / stubs
    "go-api.md": "/reference/go/",
    "python-api.md": "/reference/python-api/",
    "helm-chart.md": "/operate/helm-chart/",
    "api-reference.html": "/api-reference.html",
}

# glossary anchor on the old concepts page now lives on its own reference page
ANCHOR_REDIRECTS = {
    ("concepts.md", "glossary"): "/reference/glossary/",
}


def page_url(dest_rel: str) -> str:
    """content-relative dest path -> served Hugo URL."""
    u = dest_rel
    if u.endswith("/_index.md"):
        u = u[: -len("_index.md")]
    elif u.endswith(".md"):
        u = u[:-3] + "/"
    if not u.startswith("/"):
        u = "/" + u
    if not u.endswith("/"):
        u = u + "/"
    return u


def build_url_index():
    """source-rel path (posix, relative to docs/) -> new Hugo URL."""
    idx = {}
    for src, meta in PAGES.items():
        idx[src] = page_url(meta["dest"])
    # ADRs: adr/NNNN-*.md -> /project/adrs/NNNN-*/
    idx.update(EXTRA_URLS)
    return idx
