"""ml_hparam_search — parallel hyperparameter search, classic ML map-reduce.

Five trial tasks train (toy) models in parallel with different learning rates;
the `select_best` task fans in all five results and picks the highest score.
This is the canonical map-reduce DAG pattern in ML: independent compute per
trial, then a single aggregation step.

Topology (map-reduce):

    trial(lr=0.001)  ─┐
    trial(lr=0.01)   ─┤
    trial(lr=0.05)   ─┼─► select_best  ── prints winner + reports XCom
    trial(lr=0.1)    ─┤
    trial(lr=0.5)    ─┘

Each `trial` runs as its own pod/process (parallel by default); `select_best`
receives the list of all trial outputs and reduces them into a single result.

For a real workload, swap the body of `trial` for actual training (a single
DataLoader pass, a scikit-learn fit, a transformers training step, etc.) and
return the metrics dict that matters to you. The Dexaflow contract is the
same: each parameter receives its upstream's return value; a fan-in parameter
receives the list of all upstream return values (issue #257 / xcom_input_many).
"""
from __future__ import annotations

from airflow.sdk import DAG, task

LEARNING_RATES = [0.001, 0.01, 0.05, 0.1, 0.5]


@task
def trial(lr: float) -> dict:
    """Train one model with the given learning rate; return the eval metric.

    The body here is a deterministic stand-in for actual training so the
    example runs in milliseconds. In a real DAG this is where you call
    model.fit / Trainer.train / sklearn-pipeline / etc.
    """
    # Toy "model": a quadratic with a sweet spot near lr≈0.05.
    score = -((lr - 0.05) ** 2) * 100 + 1.0
    print(f"trial lr={lr:.3f}: score={score:.4f}")
    return {"lr": lr, "score": score}


@task
def select_best(trials: list[dict]) -> dict:
    """Reduce: pick the trial with the highest score and emit the winner.

    `trials` is the list of every upstream `trial` task's return value, in
    declaration order. The runtime delivers it as a Python list — no special
    boilerplate, no XCom pulls in the function body, no Airflow API calls.
    """
    winner = max(trials, key=lambda t: t["score"])
    print(f"best lr={winner['lr']} score={winner['score']:.4f} "
          f"(searched {len(trials)} trials)")
    return winner


with DAG("ml_hparam_search", schedule=None, catchup=False,
         tags=["example", "ml", "map-reduce"]):
    select_best([trial(lr) for lr in LEARNING_RATES])
