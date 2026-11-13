package admissionstats

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func clusterConfig() Config {
	cfg := DefaultConfig("")
	cfg.AllowInsecureLocal = true
	cfg.Timeout, cfg.PoolSize = time.Second, 5
	cfg.Cluster = ClusterConfig{Addrs: []string{"127.0.0.1:16379", "127.0.0.1:16380"}}
	return cfg
}

func TestClusterValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"too many endpoints": func(c *Config) { c.Cluster.Addrs = make([]string, 9) },
		"malformed":          func(c *Config) { c.Cluster.Addrs = []string{"127.0.0.1"} },
		"invalid port":       func(c *Config) { c.Cluster.Addrs = []string{"127.0.0.1:0"} },
		"non local plaintext": func(c *Config) {
			c.Cluster.TLSEnabled = false
			c.Cluster.Addrs = []string{"10.0.0.1:6379"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := clusterConfig()
			change(&c)
			if _, err := New(c); err == nil {
				t.Fatal("invalid cluster configuration accepted")
			}
		})
	}
}

func TestClusterOptionsSafetyBounds(t *testing.T) {
	cfg := clusterConfig()
	co, err := clusterOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(co.Addrs) != 2 {
		t.Fatalf("seed addresses not mapped: %+v", co.Addrs)
	}
	if co.MaxRetries != 0 || co.Protocol != 2 || !co.ContextTimeoutEnabled {
		t.Fatal("cluster client escaped the no-retry or protocol bounds")
	}
	if co.PoolSize != 5 || co.MaxActiveConns != 5 || co.MaxIdleConns != 5 || co.MinIdleConns != 0 {
		t.Fatal("cluster client escaped pool bounds")
	}
	if co.DialTimeout != cfg.Timeout || co.PoolTimeout != cfg.Timeout {
		t.Fatal("cluster client escaped timeout bounds")
	}
	if co.ConnMaxIdleTime != time.Minute || co.ConnMaxLifetime != 5*time.Minute {
		t.Fatal("cluster client escaped connection lifetime bounds")
	}
	if co.ClusterStateReloadInterval != clusterStateReloadInterval {
		t.Fatal("cluster client escaped the bounded topology reload interval")
	}
	recorder, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	if _, ok := recorder.client.(*redis.ClusterClient); !ok {
		t.Fatalf("cluster mode must construct a ClusterClient, got %T", recorder.client)
	}
}

func TestClusterTLSEnabled(t *testing.T) {
	cfg := clusterConfig()
	cfg.Cluster.TLSEnabled = true
	cfg.Cluster.Addrs = []string{"redis-1.example:6379"}
	co, err := clusterOptions(cfg)
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
	withSentinel.Sentinel = SentinelConfig{MasterName: "m", Addrs: []string{"127.0.0.1:26379"}}
	if _, err := clusterOptions(withSentinel); err == nil {
		t.Fatal("cluster options must reject coexisting Sentinel discovery")
	}
}
