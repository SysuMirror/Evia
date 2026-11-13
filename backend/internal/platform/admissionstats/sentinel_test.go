package admissionstats

import (
	"crypto/tls"
	"testing"
	"time"
)

func sentinelConfig() Config {
	return Config{
		KeyPrefix:          "evia:test:admission",
		Timeout:            time.Second,
		PoolSize:           5,
		AllowInsecureLocal: true,
		Sentinel: SentinelConfig{
			MasterName:       "evia-redis",
			Addrs:            []string{"127.0.0.1:26379", "127.0.0.1:26380", "127.0.0.1:26381"},
			Password:         "data-secret",
			SentinelPassword: "sentinel-secret",
		},
	}
}

func TestSentinelValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"url and sentinel both set": func(c *Config) { c.URL = "redis://127.0.0.1:6379" },
		"empty master name":         func(c *Config) { c.Sentinel.MasterName = "" },
		"illegal master name":       func(c *Config) { c.Sentinel.MasterName = "bad/name" },
		"no endpoints":              func(c *Config) { c.Sentinel.Addrs = nil },
		"too many endpoints": func(c *Config) {
			c.Sentinel.Addrs = make([]string, 9)
			for i := range c.Sentinel.Addrs {
				c.Sentinel.Addrs[i] = "127.0.0.1:26379"
			}
		},
		"malformed endpoint":  func(c *Config) { c.Sentinel.Addrs = []string{"127.0.0.1"} },
		"invalid port":        func(c *Config) { c.Sentinel.Addrs = []string{"127.0.0.1:0"} },
		"plaintext opt in":    func(c *Config) { c.AllowInsecureLocal = false },
		"plaintext non local": func(c *Config) { c.Sentinel.Addrs = []string{"10.0.0.1:26379"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := sentinelConfig()
			change(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("expected sentinel configuration rejection")
			}
		})
	}
}

func TestSentinelFailoverOptions(t *testing.T) {
	cfg := sentinelConfig()
	fo, err := failoverOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fo.MasterName != "evia-redis" || len(fo.SentinelAddrs) != 3 {
		t.Fatal("sentinel discovery fields not mapped")
	}
	if fo.Password != "data-secret" || fo.SentinelPassword != "sentinel-secret" {
		t.Fatal("sentinel credentials not mapped")
	}
	// Diagnostic writes must never replay silently, matching the URL path.
	if fo.MaxRetries != 0 || fo.Protocol != 2 || !fo.ContextTimeoutEnabled {
		t.Fatal("failover client escaped retry or protocol safety bounds")
	}
	if fo.PoolSize != 5 || fo.MaxActiveConns != 5 || fo.MaxIdleConns != 5 || fo.MinIdleConns != 0 {
		t.Fatal("failover client pool bounds changed")
	}
	if fo.DialTimeout != cfg.Timeout || fo.ReadTimeout != cfg.Timeout || fo.WriteTimeout != cfg.Timeout || fo.PoolTimeout != cfg.Timeout {
		t.Fatal("failover client timeout bounds changed")
	}
	if fo.ConnMaxIdleTime != time.Minute || fo.ConnMaxLifetime != 5*time.Minute {
		t.Fatal("failover client connection lifetime bounds changed")
	}
	if fo.TLSConfig != nil {
		t.Fatal("plaintext loopback mode must not enable TLS")
	}
	recorder, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSentinelTLSEnabled(t *testing.T) {
	cfg := sentinelConfig()
	cfg.Sentinel.TLSEnabled = true
	cfg.Sentinel.Addrs = []string{"sentinel-1.redis.example:26379"}
	fo, err := failoverOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fo.TLSConfig == nil || fo.TLSConfig.MinVersion < tls.VersionTLS12 || fo.TLSConfig.ServerName != "" {
		t.Fatal("TLS must require version 1.2 and verify each dialed endpoint hostname")
	}
}
