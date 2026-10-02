# leoflow-parser

The Leoflow DAG parser compiles an Airflow DAG (Python source) into the
canonical Leoflow `dag.json`, without executing user task code.

It is invoked by `leoflow compile` as a subprocess:

```bash
python -m leoflow_parser compile \
    --source ./dag.py \
    --config ./dexaflow.yaml \
    --output ./dag.json \
    --image myrepo/etl:v1.2.3
```

Supported operators in Phase 1: Python (including TaskFlow `@task`), Bash, and
HTTP. See `docs/api/dag-schema.json` for the output contract.
