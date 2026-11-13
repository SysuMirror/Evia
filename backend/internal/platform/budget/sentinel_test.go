package budget

import (
	"crypto/tls"
	"strings"
	"testing"
	"time"
)

func sentinelConfig() Config {
	c := localConfig()
	c.URL = ""
	c.Sentinel = SentinelConfig{
		MasterName:       "evia-redis",
		Addrs:            []string{"127.0.0.1:26379", "127.0.0.1:26380", "127.0.0.1:26381"},
		Password:         "data-secret",
		SentinelPassword: "sentinel-secret",
	}
	return c
}

func TestSentinelValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"url and sentinel both set", func(c *Config) { c.URL = "redis://127.0.0.1:6379/0" }},
		{"empty master name", func(c *Config) { c.Sentinel.MasterName = "" }},
		{"illegal master name", func(c *Config) { c.Sentinel.MasterName = "bad name!" }},
		{"master name too long", func(c *Config) { c.Sentinel.MasterName = strings.Repeat("a", 65) }},
		{"no sentinel endpoints", func(c *Config) { c.Sentinel.Addrs = nil }},
		{"too many sentinel endpoints", func(c *Config) {
			c.Sentinel.Addrs = make([]string, 9)
			for i := range c.Sentinel.Addrs {
				c.Sentinel.Addrs[i] = "127.0.0.1:26379"
			}
		}},
		{"endpoint without port", func(c *Config) { c.Sentinel.Addrs = []string{"127.0.0.1"} }},
		{"endpoint zero port", func(c *Config) { c.Sentinel.Addrs = []string{"127.0.0.1:0"} }},
		{"endpoint oversized port", func(c *Config) { c.Sentinel.Addrs = []string{"127.0.0.1:65536"} }},
		{"endpoint empty host", func(c *Config) { c.Sentinel.Addrs = []string{":26379"} }},
		{"plaintext requires loopback opt in", func(c *Config) { c.AllowInsecureLocal = false }},
		{"plaintext rejects non loopback", func(c *Config) {
			c.Sentinel.Addrs = []string{"10.0.0.1:26379"}
		}},
		{"tls flag still required for non loopback plaintext", func(c *Config) {
			c.AllowInsecureLocal = true
			c.Sentinel.Addrs = []string{"10.0.0.1:26379"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := sentinelConfig()
			tt.change(&c)
			limiter, err := New(c)
			if err == nil {
				limiter.Close()
				t.Fatal("invalid sentinel configuration accepted")
			}
		})
	}
}

func TestSentinelFailoverOptions(t *testing.T) {
	c := sentinelConfig()
	c.PoolSize = 7
	c.Timeout = 130 * time.Millisecond
	fo, err := failoverOptions(c)
	if err != nil {
		t.Fatal(err)
	}
	if fo.MasterName != "evia-redis" || len(fo.SentinelAddrs) != 3 || fo.SentinelAddrs[0] != "127.0.0.1:26379" {
		t.Fatal("sentinel discovery fields not mapped")
	}
	if fo.Password != "data-secret" || fo.SentinelPassword != "sentinel-secret" {
		t.Fatal("sentinel credentials not mapped")
	}
	// The ambiguous-debit no-replay invariant must survive discovery mode.
	if fo.MaxRetries != -1 || fo.DialerRetries != 1 || fo.PipelinePoolSize != -1 || fo.Protocol != 2 || !fo.ContextTimeoutEnabled {
		t.Fatal("failover client escaped retry or protocol safety bounds")
	}
	if fo.PoolSize != 7 || fo.MaxActiveConns != 7 || fo.MaxIdleConns != 7 || fo.MinIdleConns != 0 {
		t.Fatal("failover client pool bounds changed")
	}
	if fo.DialTimeout != c.Timeout || fo.ReadTimeout != c.Timeout || fo.WriteTimeout != c.Timeout || fo.PoolTimeout != c.Timeout {
		t.Fatal("failover client timeout bounds changed")
	}
	if fo.ConnMaxIdleTime != time.Minute || fo.ConnMaxLifetime != 5*time.Minute {
		t.Fatal("failover client connection lifetime bounds changed")
	}
	if fo.TLSConfig != nil {
		t.Fatal("plaintext loopback mode must not enable TLS")
	}

	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = limiter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSentinelTLSEnabled(t *testing.T) {
	c := sentinelConfig()
	c.Sentinel.TLSEnabled = true
	c.Sentinel.Addrs = []string{"sentinel-1.redis.example:26379", "sentinel-2.redis.example:26379"}
	fo, err := failoverOptions(c)
	if err != nil {
		t.Fatal(err)
	}
	if fo.TLSConfig == nil || fo.TLSConfig.MinVersion < tls.VersionTLS12 || fo.TLSConfig.ServerName != "" {
		t.Fatal("TLS must require version 1.2 and verify each dialed endpoint hostname")
	}
	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = limiter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSentinelAndURLModesAreMutuallyExclusive(t *testing.T) {
	c := sentinelConfig()
	if _, err := failoverOptions(c); err != nil {
		t.Fatalf("valid sentinel config rejected: %v", err)
	}
	c.URL = "redis://127.0.0.1:6379/0"
	if _, err := failoverOptions(c); err == nil {
		t.Fatal("mixed URL and sentinel configuration accepted")
	}
}
