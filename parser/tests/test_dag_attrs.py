"""DAG-level scheduling/metadata attributes captured by the shim.

``max_active_runs`` (concurrency), ``catchup``/``start_date`` (backfill) and
``description`` (UI) are all honored by the domain + scheduler; the shim was
dropping them silently. The compiler now emits each only when set, so a DAG that
declares none keeps its compiled shape.
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str, config: dict | None = None) -> dict:
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps(config or {"schema_version": "1.0"}),
    )
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1", dag_version="v1")


def test_dag_attrs_captured(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from datetime import datetime, timezone
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", description="nightly etl",
                 start_date=datetime(2026, 1, 1, tzinfo=timezone.utc),
                 max_active_runs=3, catchup=True):
            a()
    """)
    assert spec["description"] == "nightly etl"
    assert spec["start_date"] == "2026-01-01T00:00:00+00:00"
    assert spec["max_active_runs"] == 3
    assert spec["catchup"] is True


def test_dag_attrs_omitted_when_unset(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    for key in ("description", "start_date", "max_active_runs", "catchup"):
        assert key not in spec


def test_catchup_false_is_omitted(monkeypatch, tmp_path):
    # catchup defaults to False in Airflow 3; only an explicit True is emitted,
    # so the domain's zero-value (no backfill) is the compiled default.
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", catchup=False):
            a()
    """)
    assert "catchup" not in spec


def test_is_paused_upon_creation_true_and_false_are_emitted(monkeypatch, tmp_path):
    # Airflow's DAG kwarg overrides the server's dags_are_paused_at_creation when
    # the DAG is first registered, in both directions, so both values are kept.
    for value in (True, False):
        spec = _compile(monkeypatch, tmp_path, f"""
            from airflow.sdk import DAG, task
            @task
            def a() -> None: ...
            with DAG("g", is_paused_upon_creation={value}):
                a()
        """)
        assert spec["is_paused_upon_creation"] is value


def test_is_paused_upon_creation_omitted_when_unset(monkeypatch, tmp_path):
    # Unset (Airflow's None) leaves the decision to the server setting, and keeps
    # the compiled shape, and so the spec hash, of a DAG that does not use it.
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", is_paused_upon_creation=None):
            a()
    """)
    assert "is_paused_upon_creation" not in spec


def test_is_paused_upon_creation_from_dag_decorator(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import dag, task
        @task
        def a() -> None: ...
        @dag(dag_id="g", is_paused_upon_creation=False)
        def g():
            a()
        g()
    """)
    assert spec["is_paused_upon_creation"] is False


# ───────────── max_active_tasks, per DAG run as in Airflow 3 (#2427) ─────────────

def test_max_active_tasks_is_emitted(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", max_active_tasks=3):
            a()
    """)
    assert spec["max_active_tasks"] == 3


def test_max_active_tasks_from_the_dag_decorator(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import dag, task
        @task
        def a() -> None: ...
        @dag(max_active_tasks=2)
        def g():
            a()
        g()
    """)
    assert spec["max_active_tasks"] == 2


def test_max_active_tasks_absent_when_unset(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    assert "max_active_tasks" not in spec


@pytest.mark.parametrize("value", ["0", "-1", "True", "'4'", "2.5"])
def test_max_active_tasks_must_be_a_positive_integer(monkeypatch, tmp_path, value):
    with pytest.raises(ValueError) as excinfo:
        _compile(monkeypatch, tmp_path, f"""
            from airflow.sdk import DAG, task
            @task
            def a() -> None: ...
            with DAG("g", max_active_tasks={value}):
                a()
        """)
    message = str(excinfo.value)
    assert "max_active_tasks" in message
    assert "positive integer" in message


def test_concurrency_is_refused_naming_max_active_tasks(monkeypatch, tmp_path):
    with pytest.raises(ValueError) as excinfo:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task
            @task
            def a() -> None: ...
            with DAG("g", concurrency=5):
                a()
        """)
    message = str(excinfo.value)
    assert "concurrency" in message
    assert "max_active_tasks" in message
    assert "Airflow 3" in message


@pytest.mark.parametrize(("start", "zone"), [
    # Airflow 3's docs: pendulum, which the compile shims with zoneinfo.
    ('pendulum.datetime(2026, 1, 1, tz="America/Sao_Paulo")', "America/Sao_Paulo"),
    ('datetime(2026, 1, 1, tzinfo=ZoneInfo("Europe/Lisbon"))', "Europe/Lisbon"),
    # A tzinfo with no zone name keeps its offset.
    ("datetime(2026, 1, 1, tzinfo=timezone(timedelta(hours=-3)))", "-03:00"),
    ("datetime(2026, 1, 1, tzinfo=timezone(timedelta(hours=5, minutes=30)))", "+05:30"),
])
def test_schedule_timezone_comes_from_the_start_date(monkeypatch, tmp_path, start, zone):
    # Airflow reads a DAG's cron in the zone of its start_date (#2453).
    spec = _compile(monkeypatch, tmp_path, f"""
        from datetime import datetime, timedelta, timezone
        from zoneinfo import ZoneInfo
        import pendulum
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", schedule="0 6 * * *", start_date={start}):
            a()
    """)
    assert spec["schedule_timezone"] == zone


@pytest.mark.parametrize("start", [
    "datetime(2026, 1, 1)",  # naive: Airflow's default zone, UTC
    "datetime(2026, 1, 1, tzinfo=timezone.utc)",
    'pendulum.datetime(2026, 1, 1, tz="UTC")',
    "datetime(2026, 1, 1, tzinfo=timezone(timedelta(0)))",
    'datetime(2026, 1, 1, tzinfo=ZoneInfo("Etc/UTC"))',
    None,
])
def test_schedule_timezone_omitted_for_utc(monkeypatch, tmp_path, start):
    # UTC is the server default, so a UTC DAG keeps its compiled shape.
    arg = f", start_date={start}" if start else ""
    spec = _compile(monkeypatch, tmp_path, f"""
        from datetime import datetime, timedelta, timezone
        from zoneinfo import ZoneInfo
        import pendulum
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", schedule="0 6 * * *"{arg}):
            a()
    """)
    assert "schedule_timezone" not in spec


def test_schedule_timezone_from_default_args(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        import pendulum
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", schedule="@daily",
                 default_args={"start_date": pendulum.datetime(2026, 1, 1, tz="Asia/Tokyo")}):
            a()
    """)
    assert spec["schedule_timezone"] == "Asia/Tokyo"
