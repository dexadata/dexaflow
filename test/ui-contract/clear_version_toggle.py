"""Clear dialog: the "Run with latest bundle version" toggle must reach the server.

The embedded Airflow SPA (3.2 and 3.3) renders this checkbox in the Clear
dialog and sends `run_on_latest_version` in the request body. The control
plane did not read the field: every clear re-bound the run to the DAG's current
version regardless. So the checkbox was inert — unticking it changed nothing,
and the UI told the user something that was not true.

This drives the real dialog in a real browser and asserts the toggle's two
positions produce two different requests, and that the server honours both. A
unit test on the handler cannot see this: it proves the server reads a field,
not that the button a person clicks sends it.

Config via env (same as sweep.py):
  LEOFLOW_BASE_URL, LEOFLOW_USER, LEOFLOW_PASSWORD
  LEOFLOW_DAG_ID / LEOFLOW_RUN_ID / LEOFLOW_TASK_ID (auto-discovered otherwise)

Exit code: 0 if the toggle is wired end to end, 1 otherwise.
"""
import json
import os
import sys

from playwright.sync_api import sync_playwright

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sweep  # noqa: E402

CLEAR_PATH = "/clearTaskInstances"


def login(page):
    """Same login flow sweep.py uses: the SPA gates on a password form."""
    page.goto(sweep.BASE, wait_until="networkidle")
    page.wait_for_timeout(2000)
    if page.locator("input[type=password]").count() == 0:
        return
    for sel in ["input[name=username]", "input[type=email]", "input[type=text]"]:
        if page.locator(sel).count() > 0:
            page.fill(sel, sweep.USER)
            break
    page.fill("input[type=password]", sweep.PWD)
    page.locator("button[type=submit]").first.click()
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(3000)


def clear_bodies(page, dag, run, task, want_latest):
    """Open the Clear dialog, set the toggle, submit, and return every
    clearTaskInstances request body the page sent."""
    bodies = []

    def on_request(req):
        if CLEAR_PATH in req.url and req.method == "POST":
            try:
                bodies.append(json.loads(req.post_data or "{}"))
            except ValueError:
                bodies.append({"__unparseable__": req.post_data})

    page.on("request", on_request)
    page.goto(f"{sweep.BASE}/dags/{dag}/runs/{run}/tasks/{task}", wait_until="networkidle")

    # The Clear action lives behind the task instance's action menu. Fail loudly
    # if it is not there: a silently skipped assertion is the bug this file
    # exists to prevent.
    page.get_by_role("button", name="Clear", exact=False).first.click()
    toggle = page.get_by_label("Run with latest bundle version", exact=False)
    if toggle.count() == 0:
        raise AssertionError(
            "the Clear dialog has no 'Run with latest bundle version' control; "
            "the SPA changed and this test's premise is gone"
        )
    if toggle.is_disabled():
        raise AssertionError(
            "the 'Run with latest bundle version' control is disabled: the SPA thinks the run "
            "has no Dag version (empty dag_versions) and forces the latest one"
        )
    if toggle.is_checked() != want_latest:
        # The 3.3 SPA (Chakra v3) lays the styled control over the native input,
        # so a plain click on the input is intercepted, and a forced one fails
        # when the hidden input sits outside the viewport. Click it from the
        # page instead: the browser toggles it and React sees the change.
        toggle.evaluate("el => el.click()")
    if toggle.is_checked() != want_latest:
        raise AssertionError(f"could not set the toggle to {want_latest}")

    page.get_by_role("button", name="Confirm", exact=False).first.click()
    page.wait_for_timeout(2000)
    page.remove_listener("request", on_request)
    return bodies


def main():
    dag, run, task = sweep.discover()
    failures = []
    with sync_playwright() as p:
        browser = p.chromium.launch()
        page = browser.new_page()
        login(page)

        # False first: a clear on the latest version re-pins the run, and the
        # 3.3 SPA only offers the control while the run's version differs from
        # the DAG's, so the other order would hide it on the second pass.
        for want in (False, True):
            bodies = [b for b in clear_bodies(page, dag, run, task, want) if "__unparseable__" not in b]
            if not bodies:
                failures.append(f"toggle={want}: the page sent no clearTaskInstances request")
                continue
            sent = bodies[-1].get("run_on_latest_version")
            if sent is None:
                failures.append(
                    f"toggle={want}: the request omitted run_on_latest_version, "
                    f"so the server falls back to its default and the control is inert; body={bodies[-1]}"
                )
            elif sent is not want:
                failures.append(f"toggle={want}: request carried run_on_latest_version={sent}")

        browser.close()

    if failures:
        for f in failures:
            print("FAIL:", f)
        return 1
    print("OK: the Clear dialog's version toggle reaches the server in both positions")
    return 0


if __name__ == "__main__":
    sys.exit(main())
