#!/usr/bin/env bash
# Start a Redis Sentinel pair for the failover integration test (#2358): a
# master on 6390, its replica on 6391 and one sentinel on 26390 monitoring
# the master as "mymaster". With a redis-server binary on PATH the three run
# as local processes; otherwise (CI) as containers of the redis:7 image on the
# host network. Then run:
#   DEXAFLOW_TEST_REDIS_SENTINEL=127.0.0.1:26390 go test -tags integration \
#     -run Sentinel ./internal/storage/
set -euo pipefail

image=mirror.gcr.io/library/redis:7
dir=$(mktemp -d)
cat > "$dir/sentinel.conf" <<CONF
port 26390
sentinel monitor mymaster 127.0.0.1 6390 1
sentinel down-after-milliseconds mymaster 2000
sentinel failover-timeout mymaster 10000
CONF
# The sentinel rewrites its config (a temp file and a rename) as the
# image's redis user.
chmod 777 "$dir"
chmod 666 "$dir/sentinel.conf"

if command -v redis-server >/dev/null; then
  redis-server --port 6390 --save '' --appendonly no --dir "$dir" --daemonize yes --logfile "$dir/master.log"
  redis-server --port 6391 --save '' --appendonly no --dir "$dir" --replicaof 127.0.0.1 6390 --daemonize yes --logfile "$dir/replica.log"
  redis-server "$dir/sentinel.conf" --sentinel --daemonize yes --logfile "$dir/sentinel.log"
else
  docker run -d --name redis-master --network host "$image" redis-server --port 6390 --save '' --appendonly no
  docker run -d --name redis-replica --network host "$image" redis-server --port 6391 --save '' --appendonly no --replicaof 127.0.0.1 6390
  docker run -d --name redis-sentinel --network host -v "$dir:/conf" "$image" redis-server /conf/sentinel.conf --sentinel
fi

cli() {
  if command -v redis-cli >/dev/null; then redis-cli "$@"; else docker exec redis-master redis-cli "$@"; fi
}
for _ in $(seq 60); do
  if cli -p 26390 sentinel replicas mymaster 2>/dev/null | grep -q 6391; then
    echo "redis sentinel pair up: master 6390, replica 6391, sentinel 26390"
    exit 0
  fi
  sleep 1
done
echo "the sentinel did not see the replica" >&2
exit 1
