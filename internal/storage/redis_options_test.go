package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/config"
)

// A URL alone is a single node, as before #2358, with the URL's
// credentials, database and the client's default timeouts.
func TestRedisOptionsSingleNodeFromTheURL(t *testing.T) {
	u, err := redisUniversalOptions(config.RedisSection{URL: "redis://user:pw@cache:6380/2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Addrs) != 1 || u.Addrs[0] != "cache:6380" || u.DB != 2 || u.Username != "user" || u.Password != "pw" {
		t.Errorf("options = %+v", u)
	}
	if u.MasterName != "" || u.IsClusterMode {
		t.Error("a URL alone must stay a single node")
	}
	if _, ok := redis.NewUniversalClient(u).(*redis.Client); !ok {
		t.Error("a URL alone must build a single node client")
	}
}

// Sentinel takes the sentinels' addresses and the master's name; the URL
// still gives the master's password and database.
func TestRedisOptionsSentinel(t *testing.T) {
	u, err := redisUniversalOptions(config.RedisSection{
		URL:      "redis://:masterpw@ignored:6379/1",
		Sentinel: config.RedisSentinelSection{Master: "mymaster", Addrs: []string{"s1:26379", "s2:26379"}, Password: "sentpw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.MasterName != "mymaster" || strings.Join(u.Addrs, ",") != "s1:26379,s2:26379" || u.SentinelPassword != "sentpw" || u.Password != "masterpw" || u.DB != 1 {
		t.Errorf("options = %+v", u)
	}
	c := redis.NewUniversalClient(u)
	defer c.Close()
	if _, ok := c.(*redis.Client); !ok {
		t.Errorf("a Sentinel master builds a failover client, got %T", c)
	}
}

// Cluster takes the seed nodes, even a single configuration endpoint, and
// refuses a database other than 0.
func TestRedisOptionsCluster(t *testing.T) {
	u, err := redisUniversalOptions(config.RedisSection{
		URL:     "rediss://:pw@ignored:6379/0",
		Cluster: config.RedisClusterSection{Addrs: []string{"cfg.cache.example:6379"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsClusterMode || strings.Join(u.Addrs, ",") != "cfg.cache.example:6379" || u.Password != "pw" || u.TLSConfig == nil {
		t.Errorf("options = %+v", u)
	}
	c := redis.NewUniversalClient(u)
	defer c.Close()
	if _, ok := c.(*redis.ClusterClient); !ok {
		t.Errorf("cluster addresses build a cluster client, got %T", c)
	}
	if _, err := redisUniversalOptions(config.RedisSection{URL: "redis://x:6379/3", Cluster: config.RedisClusterSection{Addrs: []string{"a:1"}}}); err == nil {
		t.Error("a Cluster with database 3 was accepted")
	}
}

// The explicit timeouts and pool size win over the URL and the client's
// defaults; zero keeps them.
func TestRedisOptionsTimeouts(t *testing.T) {
	u, err := redisUniversalOptions(config.RedisSection{
		URL:         "redis://cache:6379/0?read_timeout=4s",
		DialTimeout: 2 * time.Second, WriteTimeout: 500 * time.Millisecond, PoolSize: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.DialTimeout != 2*time.Second || u.ReadTimeout != 4*time.Second || u.WriteTimeout != 500*time.Millisecond || u.PoolSize != 7 {
		t.Errorf("options = dial %s read %s write %s pool %d", u.DialTimeout, u.ReadTimeout, u.WriteTimeout, u.PoolSize)
	}
	u, err = redisUniversalOptions(config.RedisSection{URL: "redis://cache:6379/0", ReadTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if u.ReadTimeout != time.Second || u.DialTimeout != 0 || u.PoolSize != 0 {
		t.Errorf("options = read %s dial %s pool %d", u.ReadTimeout, u.DialTimeout, u.PoolSize)
	}
}
