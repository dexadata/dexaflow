"""The in-pod branch decision of ADR 0064 D1: the runtime normalises a branch
callable's result with Airflow's rules, checks it against the DAG's task ids,
and writes it for the agent, which reports it with the task's success."""

from __future__ import annotations

import itertools
import json

import pytest

from leoflow_runtime import branch, runner

_counter = itertools.count()


def _module(tmp_path, monkeypatch, body: str) -> str:
    name = f"branchmod_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(body)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def _branch_env(tmp_path, monkeypatch, kind="branch",
                ids=("pick", "a", "b", "c"), children=("a", "b")):
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_PUSHES_PATH", str(tmp_path / "pushes.json"))
    monkeypatch.setenv("LEOFLOW_BRANCH_KIND", kind)
    monkeypatch.setenv("LEOFLOW_BRANCH_PATH", str(tmp_path / "branch.json"))
    monkeypatch.setenv("LEOFLOW_BRANCH_TASK_IDS", json.dumps(list(ids)))
    monkeypatch.setenv("LEOFLOW_BRANCH_DOWNSTREAM", json.dumps(list(children)))
    return tmp_path / "branch.json"


@pytest.mark.parametrize("result, want", [
    ("a", ["a"]),
    (["b", "a", "a"], ["a", "b"]),
    (("a",), ["a"]),
    ((x for x in ["b"]), ["b"]),
    ({"a", "b"}, ["a", "b"]),
    (None, []),
])
def test_normalise_follows_airflow(result, want):
    assert branch.normalise(result, {"a", "b", "c"}) == want


def test_normalise_refuses_a_non_string_member():
    with pytest.raises(branch.BranchDecisionError, match="expected all task IDs are strings"):
        branch.normalise(["a", 1], {"a"})


def test_normalise_refuses_another_type():
    with pytest.raises(branch.BranchDecisionError, match="must be either None, a task ID"):
        branch.normalise(3, {"a"})


def test_normalise_refuses_an_unknown_id():
    with pytest.raises(branch.BranchDecisionError, match="must contain only valid task_ids"):
        branch.normalise(["a", "nope"], {"a"})


def test_branch_task_writes_its_decision_and_keeps_its_return_value(tmp_path, monkeypatch):
    path = _branch_env(tmp_path, monkeypatch)
    mod = _module(tmp_path, monkeypatch, "def pick():\n    return 'a'\n")
    runner.run(f"{mod}:pick")
    assert json.loads(path.read_text()) == {"follow_task_ids": ["a"]}
    assert json.loads((tmp_path / "rv.json").read_text()) == "a"
    pushes = json.loads((tmp_path / "pushes.json").read_text())
    assert pushes == {"skipmixin_key": {"followed": ["a"]}}


def test_branch_returning_none_follows_nothing(tmp_path, monkeypatch):
    path = _branch_env(tmp_path, monkeypatch)
    mod = _module(tmp_path, monkeypatch, "def pick():\n    return None\n")
    runner.run(f"{mod}:pick")
    assert json.loads(path.read_text()) == {"follow_task_ids": []}


def test_branch_to_an_unknown_task_fails_the_task(tmp_path, monkeypatch):
    path = _branch_env(tmp_path, monkeypatch)
    mod = _module(tmp_path, monkeypatch, "def pick():\n    return 'nope'\n")
    with pytest.raises(branch.BranchDecisionError):
        runner.run(f"{mod}:pick")
    assert not path.exists()


def test_branch_to_a_task_that_is_not_a_child_warns(tmp_path, monkeypatch, capsys):
    path = _branch_env(tmp_path, monkeypatch)
    mod = _module(tmp_path, monkeypatch, "def pick():\n    return 'c'\n")
    runner.run(f"{mod}:pick")
    assert json.loads(path.read_text()) == {"follow_task_ids": ["c"]}
    assert "not a direct downstream task" in capsys.readouterr().out


@pytest.mark.parametrize("result, falsy", [
    (0, True), ([], True), (None, True), ("x", False), (1, False),
])
def test_short_circuit_records_truthiness(tmp_path, monkeypatch, result, falsy):
    path = _branch_env(tmp_path, monkeypatch, kind="short_circuit")
    mod = _module(tmp_path, monkeypatch, f"def gate():\n    return {result!r}\n")
    runner.run(f"{mod}:gate")
    assert json.loads(path.read_text()) == {"short_circuit_falsy": falsy}
    pushes = tmp_path / "pushes.json"
    if falsy:
        assert json.loads(pushes.read_text()) == {"skipmixin_key": {"skipped": ["a", "b"]}}
    else:
        assert not pushes.exists()


def test_branch_without_children_pushes_no_skipmixin_key(tmp_path, monkeypatch):
    _branch_env(tmp_path, monkeypatch, children=())
    mod = _module(tmp_path, monkeypatch, "def pick():\n    return None\n")
    runner.run(f"{mod}:pick")
    assert not (tmp_path / "pushes.json").exists()


def test_plain_task_writes_no_decision(tmp_path, monkeypatch):
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.delenv("LEOFLOW_BRANCH_KIND", raising=False)
    mod = _module(tmp_path, monkeypatch, "def t():\n    return 'a'\n")
    runner.run(f"{mod}:t")
    assert not (tmp_path / "branch.json").exists()
