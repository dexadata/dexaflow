"""Tests for XCom input access inside a task container."""

from leoflow_runtime import xcom


def test_xcom_pull_decodes_env(monkeypatch):
    monkeypatch.setenv("LEOFLOW_XCOM_UPSTREAM", '{"n": 1}')
    assert xcom.xcom_pull("upstream") == {"n": 1}


def test_xcom_pull_uppercases_name(monkeypatch):
    monkeypatch.setenv("LEOFLOW_XCOM_MY_INPUT", "[1, 2, 3]")
    assert xcom.xcom_pull("my_input") == [1, 2, 3]


def test_xcom_pull_missing_returns_default(monkeypatch):
    monkeypatch.delenv("LEOFLOW_XCOM_NOPE", raising=False)
    assert xcom.xcom_pull("nope") is None
    assert xcom.xcom_pull("nope", default=42) == 42


def test_xcom_pull_reads_a_file_input(monkeypatch, tmp_path):
    """An input too large for the environment arrives as a file (#2455)."""
    big = {"rows": list(range(200_000))}
    path = tmp_path / "rows.json"
    path.write_text(__import__("json").dumps(big))
    monkeypatch.delenv("LEOFLOW_XCOM_ROWS", raising=False)
    monkeypatch.setenv("LEOFLOW_XCOMFILE_ROWS", str(path))
    assert xcom.xcom_pull("rows") == big
    assert "rows" in xcom.input_names()


def test_xcom_env_wins_over_a_file(monkeypatch, tmp_path):
    path = tmp_path / "v.json"
    path.write_text("2")
    monkeypatch.setenv("LEOFLOW_XCOM_V", "1")
    monkeypatch.setenv("LEOFLOW_XCOMFILE_V", str(path))
    assert xcom.xcom_pull("v") == 1
