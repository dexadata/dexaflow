"""hello — a minimal starter DAG. Edit and save; `leoflow dev` hot-reloads."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def greet() -> str:
    print("hello from your first Dexaflow DAG")
    return "hello"


@task
def shout(message: str) -> None:
    print(f"{message.upper()}!")


with DAG("hello", schedule=None, catchup=False, tags=["starter"]):
    shout(greet())
