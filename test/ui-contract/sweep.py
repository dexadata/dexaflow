"""UI contract sweep — drive every major Airflow-SPA view in a real browser and
fail if any frontend<->backend integration is broken.

For each view it records every backend response (status), console errors, and a
screenshot, then reports any view with a non-2xx /api or /ui call or a console
error. This guards the API contract against UI regressions — notably when the
embedded Airflow SPA is upgraded to a new version (a renamed field, a new
endpoint, or a changed Accept header surfaces here as a broken view).

Config via env (all optional):
  LEOFLOW_BASE_URL   default http://host.docker.internal:8080
  LEOFLOW_USER       default admin@dexaflow.local, or admin@leoflow.local
                     when only that one logs in (installs from before the rename)
  LEOFLOW_PASSWORD   default admin
  LEOFLOW_DAG_ID     default: first DAG that has a run (auto-discovered)
  LEOFLOW_RUN_ID     default: that DAG's latest run (auto-discovered)
  LEOFLOW_TASK_ID    default: first task instance of that run (auto-discovered)
  UICONTRACT_OUT     screenshot dir (default: none)

Exit code: 0 if every view is clean, 1 otherwise.
"""
import json
import os
import sys
import time
import urllib.request

BASE = os.environ.get("LEOFLOW_BASE_URL", "http://host.docker.internal:8080")
PWD = os.environ.get("LEOFLOW_PASSWORD", "admin")
ADMIN_CANDIDATES = ("admin@dexaflow.local", "admin@leoflow.local")


def token_for(user):
    """Return an access token for user, or "" when the login is refused."""
    data = json.dumps({"username": user, "password": PWD}).encode()
    req = urllib.request.Request(BASE + "/auth/token", data=data,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=15) as r:  # noqa: S310 (trusted local URL)
            return json.load(r).get("access_token", "")
    except Exception:  # noqa: BLE001 (any failure means this user does not log in)
        return ""


def resolve_user():
    """LEOFLOW_USER, else the first bootstrap admin address that logs in."""
    if os.environ.get("LEOFLOW_USER"):
        return os.environ["LEOFLOW_USER"]
    for candidate in ADMIN_CANDIDATES:
        if token_for(candidate):
            return candidate
    return ADMIN_CANDIDATES[0]


# Set by discover(), the first call of every entry point (main() here and in
# clear_version_toggle.py), so importing this module makes no request.
USER = ""
OUT = os.environ.get("UICONTRACT_OUT", "")

# Console errors that are environmental, not contract breaks. Keep this list
# short and documented — every entry is a known gap, not a license to ignore.
CONSOLE_ALLOWLIST = (
    "sqlparser_rs_wasm",  # optional SQL syntax-highlight wasm, not embedded
)


def api(path, token=None):
    req = urllib.request.Request(BASE + path)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=15) as r:  # noqa: S310 (trusted local URL)
        return json.load(r)


def discover():
    """Find a DAG with a run + a task instance, so the task-scoped views resolve."""
    global USER  # resolved once, on first use, never at import time
    if not USER:
        USER = resolve_user()
    token = token_for(USER)
    if not token:
        print(f"WARN: could not get a token for {USER}; task views may be skipped")
    dag = os.environ.get("LEOFLOW_DAG_ID", "")
    run = os.environ.get("LEOFLOW_RUN_ID", "")
    task = os.environ.get("LEOFLOW_TASK_ID", "")
    if not dag and token:
        for d in api("/api/v2/dags?limit=50", token).get("dags", []):
            runs = api(f"/api/v2/dags/{d['dag_id']}/dagRuns?limit=1&order_by=-logical_date", token).get("dag_runs", [])
            if runs:
                dag, run = d["dag_id"], runs[0]["dag_run_id"]
                break
    if dag and not run and token:
        # A DAG named without a run: take its latest, so the run and task
        # views are swept too instead of silently skipped.
        runs = api(f"/api/v2/dags/{dag}/dagRuns?limit=1&order_by=-logical_date", token).get("dag_runs", [])
        if runs:
            run = runs[0]["dag_run_id"]
    if dag and run and not task and token:
        tis = api(f"/api/v2/dags/{dag}/dagRuns/{run}/taskInstances?limit=1", token).get("task_instances", [])
        if tis:
            task = tis[0]["task_id"]
    return dag, run, task


def views(dag, run, task):
    v = [
        ("home", "/"),
        ("dags_list", "/dags"),
        ("variables", "/variables"),
        ("connections", "/connections"),
        ("pools", "/pools"),
        ("providers", "/providers"),
        ("plugins", "/plugins"),
        ("jobs", "/jobs"),
        ("config", "/config"),
        ("assets", "/assets"),
        ("audit_global", "/events"),
    ]
    if dag:
        v += [
            ("dag_overview", f"/dags/{dag}"),
            ("dag_runs", f"/dags/{dag}/runs"),
            ("dag_tasks", f"/dags/{dag}/tasks"),
            ("dag_code", f"/dags/{dag}/code"),
            ("dag_details", f"/dags/{dag}/details"),
            ("dag_events", f"/dags/{dag}/events"),
        ]
    if dag and run:
        v.append(("run_overview", f"/dags/{dag}/runs/{run}"))
    if dag and run and task:
        base = f"/dags/{dag}/runs/{run}/tasks/{task}"
        v += [
            ("task_logs", base),
            ("task_details", f"{base}/details"),
            ("task_xcom", f"{base}/xcom"),
            ("task_code", f"{base}/code"),
            ("task_events", f"{base}/events"),
        ]
    return v


def allowed(msg):
    return any(a in msg for a in CONSOLE_ALLOWLIST)


def main():
    from playwright.sync_api import sync_playwright

    dag, run, task = discover()
    print(f"sweep target: base={BASE} dag={dag!r} run={run!r} task={task!r}\n")
    report = {}

    with sync_playwright() as p:
        browser = p.chromium.launch()
        ctx = browser.new_context(viewport={"width": 1600, "height": 1000})
        page = ctx.new_page()

        page.goto(BASE, wait_until="networkidle")
        time.sleep(2)
        if page.locator("input[type=password]").count() > 0:
            for sel in ["input[name=username]", "input[type=email]", "input[type=text]"]:
                if page.locator(sel).count() > 0:
                    page.fill(sel, USER)
                    break
            page.fill("input[type=password]", PWD)
            page.locator("button[type=submit]").first.click()
            page.wait_for_load_state("networkidle")
            time.sleep(3)

        for label, path in views(dag, run, task):
            calls, errors = [], []
            on_resp = lambda r, calls=calls: calls.append((r.status, r.url.replace(BASE, "").split("?")[0])) \
                if ("/api/" in r.url or "/ui/" in r.url) else None
            on_err = lambda m, errors=errors: errors.append(m.text[:200]) \
                if (m.type == "error" and not allowed(m.text)) else None
            page.on("response", on_resp)
            page.on("console", on_err)
            try:
                page.goto(BASE + path, wait_until="networkidle", timeout=20000)
            except Exception as e:
                errors.append(f"NAV ERROR: {e}")
            time.sleep(2)
            if OUT:
                page.screenshot(path=f"{OUT}/sweep_{label}.png", full_page=True)
            page.remove_listener("response", on_resp)
            page.remove_listener("console", on_err)
            report[label] = {
                "path": path,
                "bad_calls": [c for c in calls if c[0] >= 400],
                "console_errors": errors,
            }
        ctx.close()
        browser.close()

    broken = 0
    for label, r in report.items():
        if r["bad_calls"] or r["console_errors"]:
            broken += 1
            print(f"[BROKEN] {label}  ({r['path']})")
            for st, u in r["bad_calls"]:
                print(f"    {st} {u}")
            for e in r["console_errors"][:3]:
                print(f"    console: {e}")
        else:
            print(f"[ok]     {label}")
    print(f"\n{broken}/{len(report)} views with contract problems")
    return 1 if broken else 0


if __name__ == "__main__":
    sys.exit(main())
