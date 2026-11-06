package serverapp

import (
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/storage"
)

// TestSharedRateLimitsFollowsTheDatastore: with Redis as the datastore the
// sign-in limits share a Redis counter across replicas; without it (Lite)
// they stay in process (#2357).
func TestSharedRateLimitsFollowsTheDatastore(t *testing.T) {
	if got := sharedRateLimits(nil); got != nil {
		t.Errorf("without Redis = %T, want nil", got)
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	if _, ok := sharedRateLimits(&storage.Redis{Client: client}).(*storage.RedisRateCounter); !ok {
		t.Error("with Redis the limits are not shared through a RedisRateCounter")
	}
}
