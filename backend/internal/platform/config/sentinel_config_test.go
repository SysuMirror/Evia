package config

import "testing"

func sentinelValues() map[string]string {
	values := runtimeValues()
	delete(values, "EVIA_REDIS_URL")
	values["EVIA_REDIS_SENTINEL_ADDRS"] = "sentinel-1.redis.example:26379, sentinel-2.redis.example:26379, sentinel-3.redis.example:26379"
	values["EVIA_REDIS_SENTINEL_MASTER"] = "evia-redis"
	values["EVIA_REDIS_PASSWORD"] = "data-secret"
	values["EVIA_REDIS_SENTINEL_PASSWORD"] = "sentinel-secret"
	return values
}

func TestRedisSentinelConfiguration(t *testing.T) {
	values := sentinelValues()
	c, err := Read(func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("sentinel configuration rejected: %v", err)
	}
	if c.Budget.URL != "" {
		t.Fatal("sentinel mode must clear the single-endpoint URL")
	}
	sentinel := c.Budget.Sentinel
	if sentinel.MasterName != "evia-redis" || len(sentinel.Addrs) != 3 {
		t.Fatalf("sentinel discovery not parsed: %+v", sentinel)
	}
	if sentinel.Addrs[0] != "sentinel-1.redis.example:26379" {
		t.Fatalf("sentinel endpoint not trimmed or ordered: %q", sentinel.Addrs[0])
	}
	if sentinel.Password != "data-secret" || sentinel.SentinelPassword != "sentinel-secret" {
		t.Fatal("sentinel credentials not mapped")
	}
	if !sentinel.TLSEnabled {
		t.Fatal("sentinel must default to TLS")
	}
}

func TestRedisSentinelLoopbackPlaintext(t *testing.T) {
	values := sentinelValues()
	values["EVIA_ALLOW_INSECURE_DEPENDENCIES"] = "true"
	values["EVIA_REDIS_SENTINEL_TLS_ENABLED"] = "false"
	values["EVIA_REDIS_SENTINEL_ADDRS"] = "127.0.0.1:26379, [::1]:26380"
	c, err := Read(func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("loopback plaintext sentinel rejected: %v", err)
	}
	if c.Budget.Sentinel.TLSEnabled || len(c.Budget.Sentinel.Addrs) != 2 {
		t.Fatalf("loopback plaintext parsing mismatch: %+v", c.Budget.Sentinel)
	}
}

func TestRedisSentinelRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]string)
	}{
		{"mutually exclusive with URL", func(v map[string]string) { v["EVIA_REDIS_URL"] = "rediss://redis.example:6379/0" }},
		{"master without endpoints", func(v map[string]string) { delete(v, "EVIA_REDIS_SENTINEL_ADDRS") }},
		{"endpoints without master", func(v map[string]string) { delete(v, "EVIA_REDIS_SENTINEL_MASTER") }},
		{"illegal master name", func(v map[string]string) { v["EVIA_REDIS_SENTINEL_MASTER"] = "bad name!" }},
		{"too many endpoints", func(v map[string]string) {
			v["EVIA_REDIS_SENTINEL_ADDRS"] = "s-1:26379,s-2:26379,s-3:26379,s-4:26379,s-5:26379,s-6:26379,s-7:26379,s-8:26379,s-9:26379"
		}},
		{"endpoint without port", func(v map[string]string) { v["EVIA_REDIS_SENTINEL_ADDRS"] = "127.0.0.1" }},
		{"endpoint invalid port", func(v map[string]string) { v["EVIA_REDIS_SENTINEL_ADDRS"] = "127.0.0.1:65536" }},
		{"non boolean tls flag", func(v map[string]string) { v["EVIA_REDIS_SENTINEL_TLS_ENABLED"] = "yes" }},
		{"plaintext without insecure opt in", func(v map[string]string) {
			v["EVIA_REDIS_SENTINEL_TLS_ENABLED"] = "false"
			v["EVIA_REDIS_SENTINEL_ADDRS"] = "127.0.0.1:26379"
		}},
		{"plaintext non loopback", func(v map[string]string) {
			v["EVIA_ALLOW_INSECURE_DEPENDENCIES"] = "true"
			v["EVIA_REDIS_SENTINEL_TLS_ENABLED"] = "false"
			v["EVIA_REDIS_SENTINEL_ADDRS"] = "10.0.0.1:26379"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := sentinelValues()
			tt.change(values)
			if _, err := Read(func(k string) string { return values[k] }); err == nil {
				t.Fatal("invalid sentinel setting accepted")
			}
		})
	}
}

func TestRedisURLPathKeepsSentinelDisabled(t *testing.T) {
	values := runtimeValues()
	c, err := Read(func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Budget.Sentinel.MasterName != "" || len(c.Budget.Sentinel.Addrs) != 0 || c.Budget.URL == "" {
		t.Fatal("default URL path must leave Sentinel discovery disabled")
	}
}
