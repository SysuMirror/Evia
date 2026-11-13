package budget

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func clusterConfig() Config {
	c := DefaultConfig("")
	c.AllowInsecureLocal = true
	c.PoolSize = 7
	c.Timeout = 130 * time.Millisecond
	c.Cluster = ClusterConfig{Addrs: []string{"127.0.0.1:16379", "127.0.0.1:16380"}}
	return c
}

func TestClusterValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"no endpoints", func(c *Config) { c.Cluster.Addrs = nil }},
		{"too many endpoints", func(c *Config) {
			c.Cluster.Addrs = make([]string, 9)
			for i := range c.Cluster.Addrs {
				c.Cluster.Addrs[i] = "127.0.0.1:6379"
			}
		}},
		{"malformed endpoint", func(c *Config) { c.Cluster.Addrs = []string{"127.0.0.1:6379", "127.0.0.1"} }},
		{"zero port", func(c *Config) { c.Cluster.Addrs = []string{"127.0.0.1:0"} }},
		{"oversized port", func(c *Config) { c.Cluster.Addrs = []string{"127.0.0.1:65536"} }},
		{"plaintext non loopback", func(c *Config) {
			c.Cluster.TLSEnabled = false
			c.Cluster.Addrs = []string{"10.0.0.1:6379"}
		}},
		{"plaintext opt in missing", func(c *Config) {
			c.AllowInsecureLocal = false
			c.Cluster.TLSEnabled = false
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := clusterConfig()
			tt.change(&c)
			if _, err := New(c); err == nil {
				t.Fatal("invalid cluster configuration accepted")
			}
		})
	}
}

func TestClusterOptionsSafetyBounds(t *testing.T) {
	c := clusterConfig()
	co, err := clusterOptions(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(co.Addrs) != 2 || co.Addrs[0] != "127.0.0.1:16379" || co.Addrs[1] != "127.0.0.1:16380" {
		t.Fatalf("cluster seed addresses not mapped: %+v", co.Addrs)
	}
	// The ambiguous-debit no-replay invariant and the same pool/timeout
	// bounds as the standalone path must survive Cluster routing.
	if co.MaxRetries != -1 || co.DialerRetries != 1 || co.PipelinePoolSize != -1 || co.Protocol != 2 || !co.ContextTimeoutEnabled {
		t.Fatal("cluster client escaped retry or protocol safety bounds")
	}
	if co.PoolSize != 7 || co.MaxActiveConns != 7 || co.MaxIdleConns != 7 || co.MinIdleConns != 0 {
		t.Fatal("cluster client escaped pool bounds")
	}
	if co.DialTimeout != c.Timeout || co.ReadTimeout != c.Timeout || co.WriteTimeout != c.Timeout || co.PoolTimeout != c.Timeout {
		t.Fatal("cluster client escaped timeout bounds")
	}
	if co.ConnMaxIdleTime != time.Minute || co.ConnMaxLifetime != 5*time.Minute {
		t.Fatal("cluster client escaped connection lifetime bounds")
	}
	if co.ClusterStateReloadInterval != clusterStateReloadInterval {
		t.Fatal("cluster client escaped the bounded topology reload interval")
	}
	if co.TLSConfig != nil {
		t.Fatal("plaintext loopback mode must not enable TLS")
	}
	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	if _, ok := limiter.client.(*redis.ClusterClient); !ok {
		t.Fatalf("cluster mode must construct a ClusterClient, got %T", limiter.client)
	}
}

func TestClusterTLSEnabled(t *testing.T) {
	c := clusterConfig()
	c.Cluster.TLSEnabled = true
	c.Cluster.Addrs = []string{"redis-1.example:6379", "redis-2.example:6379"}
	co, err := clusterOptions(c)
	if err != nil {
		t.Fatal(err)
	}
	if co.TLSConfig == nil || co.TLSConfig.MinVersion < tls.VersionTLS12 || co.TLSConfig.ServerName != "" {
		t.Fatal("TLS must require version 1.2 and verify each dialed endpoint hostname")
	}
}

func TestClusterOptionsRejectOtherModes(t *testing.T) {
	withURL := clusterConfig()
	withURL.URL = "redis://127.0.0.1:6379"
	if _, err := clusterOptions(withURL); err == nil {
		t.Fatal("cluster options must reject a coexisting URL")
	}
	withSentinel := clusterConfig()
	withSentinel.Sentinel = SentinelConfig{MasterName: "evia-redis", Addrs: []string{"127.0.0.1:26379"}}
	if _, err := clusterOptions(withSentinel); err == nil {
		t.Fatal("cluster options must reject coexisting Sentinel discovery")
	}
}
