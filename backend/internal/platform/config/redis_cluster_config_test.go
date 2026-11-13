package config

import "testing"

func clusterValues(extra map[string]string) map[string]string {
	values := runtimeValues()
	delete(values, "EVIA_REDIS_URL")
	values["EVIA_ALLOW_INSECURE_DEPENDENCIES"] = "true"
	values["EVIA_REDIS_CLUSTER_TLS_ENABLED"] = "false"
	values["EVIA_REDIS_CLUSTER_ADDRS"] = "127.0.0.1:16379,127.0.0.1:16380,127.0.0.1:16381"
	for key, value := range extra {
		values[key] = value
	}
	return values
}

func TestRedisClusterConfiguration(t *testing.T) {
	values := clusterValues(nil)
	c, err := ReadFor("serve", func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("cluster configuration rejected: %v", err)
	}
	if c.Budget.URL != "" || c.Budget.Sentinel.MasterName != "" {
		t.Fatal("cluster mode must clear the URL and Sentinel modes")
	}
	if len(c.Budget.Cluster.Addrs) != 3 {
		t.Fatalf("cluster seed count = %d, want 3", len(c.Budget.Cluster.Addrs))
	}
	if c.Budget.Cluster.TLSEnabled {
		t.Fatal("explicit plaintext loopback cluster must keep TLS disabled")
	}
}

func TestRedisClusterTLSDefaultsOn(t *testing.T) {
	values := clusterValues(map[string]string{
		"EVIA_REDIS_CLUSTER_ADDRS":       "redis-1.example:6379,redis-2.example:6379",
		"EVIA_REDIS_CLUSTER_TLS_ENABLED": "true",
	})
	delete(values, "EVIA_ALLOW_INSECURE_DEPENDENCIES")
	c, err := ReadFor("serve", func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("TLS cluster configuration rejected: %v", err)
	}
	if !c.Budget.Cluster.TLSEnabled {
		t.Fatal("cluster TLS must default to enabled")
	}
}

func TestRedisClusterRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{"mixed with url", map[string]string{"EVIA_REDIS_URL": "rediss://redis.example:6379"}},
		{"mixed with sentinel", map[string]string{
			"EVIA_REDIS_SENTINEL_ADDRS":  "127.0.0.1:26379",
			"EVIA_REDIS_SENTINEL_MASTER": "evia-redis",
		}},
		{"bad endpoint", map[string]string{"EVIA_REDIS_CLUSTER_ADDRS": "127.0.0.1:16379,bad-host"}},
		{"bad port", map[string]string{"EVIA_REDIS_CLUSTER_ADDRS": "127.0.0.1:0"}},
		{"nine seeds", map[string]string{"EVIA_REDIS_CLUSTER_ADDRS": "10.0.0.1:6379,10.0.0.2:6379,10.0.0.3:6379,10.0.0.4:6379,10.0.0.5:6379,10.0.0.6:6379,10.0.0.7:6379,10.0.0.8:6379,10.0.0.9:6379"}},
		{"non loopback plaintext", map[string]string{"EVIA_REDIS_CLUSTER_ADDRS": "10.0.0.1:6379", "EVIA_REDIS_CLUSTER_TLS_ENABLED": "false"}},
		{"bad tls flag", map[string]string{"EVIA_REDIS_CLUSTER_TLS_ENABLED": "yes"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := clusterValues(tt.values)
			if _, err := ReadFor("serve", func(k string) string { return values[k] }); err == nil {
				t.Fatal("invalid cluster setting accepted")
			}
		})
	}
}

func TestRedisClusterKeepsOtherModesIntact(t *testing.T) {
	urlValues := runtimeValues()
	urlConfig, err := ReadFor("serve", func(k string) string { return urlValues[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(urlConfig.Budget.Cluster.Addrs) != 0 {
		t.Fatal("the default single-endpoint URL path must not enable cluster routing")
	}

	sentinelEnv := sentinelValues()
	sentinelConfig, err := ReadFor("serve", func(k string) string { return sentinelEnv[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(sentinelConfig.Budget.Cluster.Addrs) != 0 || sentinelConfig.Budget.Sentinel.MasterName == "" {
		t.Fatal("sentinel mode must remain independent from cluster routing")
	}
}
