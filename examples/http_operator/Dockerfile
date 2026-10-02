# Standard DAG image (#318). Built by 'dexaflow compile --build' or by hand:
#   docker build -t my-registry/http_operator:<tag> .
# Synthesized by scripts/sync-example-dockerfiles.sh from dexaflow.yaml; do
# not hand-edit — re-run the script after changing python_version or
# dependencies, or CI's drift check fails.
FROM leoflow-base:py3.11
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
