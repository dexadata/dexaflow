# Standard DAG image (#318). Built by 'leoflow compile --build' or by hand:
#   docker build -t my-registry/api_chain:<tag> .
# Synthesized by scripts/sync-example-dockerfiles.sh from leoflow.yaml; do
# not hand-edit — re-run the script after changing python_version or
# dependencies, or CI's drift check fails.
FROM leoflow-base:py3.11
RUN pip install --no-cache-dir -- 'requests==2.32.3'
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
