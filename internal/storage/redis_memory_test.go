package storage

import "testing"

func TestParseRedisInfo(t *testing.T) {
	text := "# Memory\r\nused_memory:1048576\r\nmaxmemory_policy:volatile-lru\r\n\r\n# Stats\r\nevicted_keys:7\r\nbad line\r\n"
	got := parseRedisInfo(text)
	want := map[string]string{"used_memory": "1048576", "maxmemory_policy": "volatile-lru", "evicted_keys": "7"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRedisMemoryInfoEvictionSafe(t *testing.T) {
	for policy, safe := range map[string]bool{
		"noeviction":      true,
		"volatile-lru":    false,
		"volatile-ttl":    false,
		"allkeys-lru":     false,
		"allkeys-random":  false,
		"volatile-random": false,
	} {
		if got := (RedisMemoryInfo{Policy: policy}).EvictionSafe(); got != safe {
			t.Errorf("EvictionSafe(%s) = %v, want %v", policy, got, safe)
		}
	}
}
