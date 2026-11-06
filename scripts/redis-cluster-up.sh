#!/usr/bin/env bash
# Start a three-node Redis Cluster on 7000-7002 for the cluster integration
# test (#2358), as local processes with a redis-server binary on PATH, or as
# containers of the redis:7 image on the host network (CI). Then run:
#   DEXAFLOW_TEST_REDIS_CLUSTER=127.0.0.1:7000 go test -tags integration \
#     -run Cluster ./internal/storage/
set -euo pipefail

image=mirror.gcr.io/library/redis:7
ports=(7000 7001 7002)
for p in "${ports[@]}"; do
  args=(--port "$p" --cluster-enabled yes --cluster-config-file "nodes-$p.conf" --save '' --appendonly no)
  if command -v redis-server >/dev/null; then
    dir=$(mktemp -d)
    (cd "$dir" && redis-server "${args[@]}" --daemonize yes --logfile "$dir/redis.log")
  else
    docker run -d --name "redis-cluster-$p" --network host "$image" redis-server "${args[@]}"
  fi
done

cli() {
  if command -v redis-cli >/dev/null; then redis-cli "$@"; else docker exec redis-cluster-7000 redis-cli "$@"; fi
}
for p in "${ports[@]}"; do
  for _ in $(seq 30); do cli -p "$p" ping >/dev/null 2>&1 && break; sleep 1; done
done
cli --cluster create 127.0.0.1:7000 127.0.0.1:7001 127.0.0.1:7002 --cluster-yes >/dev/null
for _ in $(seq 30); do
  if cli -p 7000 cluster info | grep -q 'cluster_state:ok'; then
    echo "redis cluster up on 7000-7002"
    exit 0
  fi
  sleep 1
done
echo "the cluster did not reach state ok" >&2
exit 1
