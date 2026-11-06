package config

import (
	"strings"
	"testing"
	"time"
)

// Sentinel, Cluster and the client's timeouts bind from both prefixes, lists
// as comma separated values (#2358).
func TestRedisTopologyFromTheEnvironment(t *testing.T) {
	t.Setenv("LEOFLOW_REDIS_SENTINEL_MASTER", "mymaster")
	t.Setenv("DEXAFLOW_REDIS_SENTINEL_ADDRS", "s1:26379,s2:26379")
	t.Setenv("LEOFLOW_REDIS_READ_TIMEOUT", "750ms")
	t.Setenv("DEXAFLOW_REDIS_POOL_SIZE", "20")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	r := c.Redis
	if r.Sentinel.Master != "mymaster" || strings.Join(r.Sentinel.Addrs, ",") != "s1:26379,s2:26379" || r.ReadTimeout != 750*time.Millisecond || r.PoolSize != 20 {
		t.Errorf("redis = %+v", r)
	}
	if !r.Configured() {
		t.Error("a Sentinel master without a URL must count as a configured Redis")
	}
}

// Nothing set is the embedded datastore, and the defaults keep the client's
// own timeouts and pool size.
func TestRedisTopologyDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	r := c.Redis
	if r.Configured() || r.DialTimeout != 0 || r.ReadTimeout != 0 || r.WriteTimeout != 0 || r.PoolSize != 0 {
		t.Errorf("redis defaults = %+v", r)
	}
	if !(RedisSection{Cluster: RedisClusterSection{Addrs: []string{"a:1"}}}).Configured() {
		t.Error("cluster addresses must count as a configured Redis")
	}
}

func TestRedisTopologyValidate(t *testing.T) {
	bad := map[string]RedisSection{
		"sentinel and cluster":     {Sentinel: RedisSentinelSection{Master: "m", Addrs: []string{"s:1"}}, Cluster: RedisClusterSection{Addrs: []string{"c:1"}}},
		"master without sentinels": {Sentinel: RedisSentinelSection{Master: "m"}},
		"sentinels without master": {Sentinel: RedisSentinelSection{Addrs: []string{"s:1"}}},
		"negative read timeout":    {URL: "redis://x", ReadTimeout: -time.Second},
		"negative pool size":       {URL: "redis://x", PoolSize: -1},
	}
	for name, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil", name)
		}
	}
	for name, r := range map[string]RedisSection{
		"url":      {URL: "redis://x"},
		"sentinel": {Sentinel: RedisSentinelSection{Master: "m", Addrs: []string{"s:1"}}},
		"cluster":  {Cluster: RedisClusterSection{Addrs: []string{"c:1"}}},
	} {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
}
