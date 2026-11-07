package budget

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const testProject = "a3b09cf1-4ec3-4d54-a9c8-a18570deba40"

func localConfig() Config {
	c := DefaultConfig("redis://127.0.0.1:1/0")
	c.AllowInsecureLocal = true
	return c
}

func TestConfigurationValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"empty URL", func(c *Config) { c.URL = "" }},
		{"invalid URL", func(c *Config) { c.URL = "redis://%broken" }},
		{"unsupported scheme", func(c *Config) { c.URL = "http://localhost:6379" }},
		{"missing host", func(c *Config) { c.URL = "redis:///0" }},
		{"credential without host", func(c *Config) { c.URL = "rediss://secret@/0" }},
		{"URL options", func(c *Config) { c.URL += "?read_timeout=-1" }},
		{"URL fragment", func(c *Config) { c.URL += "#unsafe" }},
		{"negative database", func(c *Config) { c.URL = "redis://localhost:6379/-1" }},
		{"zero port", func(c *Config) { c.URL = "redis://localhost:0" }},
		{"oversized port", func(c *Config) { c.URL = "redis://localhost:65536" }},
		{"plaintext opt in required", func(c *Config) { c.AllowInsecureLocal = false }},
		{"plaintext nonlocal", func(c *Config) { c.URL = "redis://10.0.0.1:6379/0" }},
		{"misleading localhost", func(c *Config) { c.URL = "redis://localhost.example:6379" }},
		{"prefix empty", func(c *Config) { c.KeyPrefix = "" }},
		{"prefix cluster injection", func(c *Config) { c.KeyPrefix = "evia:{foreign}" }},
		{"prefix wildcard", func(c *Config) { c.KeyPrefix = "evia:*" }},
		{"prefix too long", func(c *Config) { c.KeyPrefix = strings.Repeat("a", 65) }},
		{"zero rate", func(c *Config) { c.Default.Rate = 0 }},
		{"negative rate", func(c *Config) { c.Default.Rate = -1 }},
		{"NaN rate", func(c *Config) { c.Default.Rate = math.NaN() }},
		{"infinite rate", func(c *Config) { c.Default.Rate = math.Inf(1) }},
		{"rate precision", func(c *Config) { c.Default.Rate = 0.0001 }},
		{"rate ceiling", func(c *Config) { c.Default.Rate = 1_000_001 }},
		{"negative burst", func(c *Config) { c.Default.Burst = -1 }},
		{"burst cannot cover batch", func(c *Config) { c.Default.Burst = 49 }},
		{"burst ceiling", func(c *Config) { c.Default.Burst = 1_000_001 }},
		{"unbounded lifetime", func(c *Config) { c.Default = Policy{Rate: 0.001, Burst: 1000} }},
		{"zero batch", func(c *Config) { c.MaxBatchEvents = 0 }},
		{"negative batch", func(c *Config) { c.MaxBatchEvents = -1 }},
		{"batch exceeds contract", func(c *Config) { c.MaxBatchEvents = 51 }},
		{"zero timeout", func(c *Config) { c.Timeout = 0 }},
		{"negative timeout", func(c *Config) { c.Timeout = -time.Second }},
		{"excessive timeout", func(c *Config) { c.Timeout = 6 * time.Second }},
		{"zero pool", func(c *Config) { c.PoolSize = 0 }},
		{"excessive pool", func(c *Config) { c.PoolSize = 257 }},
		{"override project", func(c *Config) {
			c.Overrides = map[Scope]Policy{{ProjectID: "other", Environment: "production"}: c.Default}
		}},
		{"override environment", func(c *Config) {
			c.Overrides = map[Scope]Policy{{ProjectID: testProject, Environment: "other"}: c.Default}
		}},
		{"override policy", func(c *Config) {
			c.Overrides = map[Scope]Policy{{ProjectID: testProject, Environment: "production"}: {Rate: math.NaN(), Burst: 50}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := localConfig()
			tt.change(&c)
			limiter, err := New(c)
			if err == nil {
				limiter.Close()
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestClientBoundsAndTLS(t *testing.T) {
	for _, endpoint := range []string{"rediss://budget.example:6379/0", "redis://localhost:6379/0", "redis://[::1]:6379/0"} {
		t.Run(endpoint, func(t *testing.T) {
			c := localConfig()
			c.URL = endpoint
			c.PoolSize = 7
			c.Timeout = 130 * time.Millisecond
			limiter, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer limiter.Close()
			o := limiter.client.Options()
			// go-redis normalizes the explicit -1 retry setting to zero retries.
			if o.MaxRetries != 0 || o.DialerRetries != 1 || o.PipelinePoolSize != -1 || o.PoolSize != 7 || o.MaxActiveConns != 7 || o.MaxIdleConns != 7 || o.PoolTimeout != c.Timeout || o.ReadTimeout != c.Timeout || o.WriteTimeout != c.Timeout || o.DialTimeout != c.Timeout || !o.ContextTimeoutEnabled {
				t.Fatal("client escaped configured retry, connection or timeout bounds")
			}
			if strings.HasPrefix(endpoint, "rediss:") && (o.TLSConfig == nil || o.TLSConfig.InsecureSkipVerify || o.TLSConfig.MinVersion < tls.VersionTLS12 || o.TLSConfig.ServerName != "budget.example") {
				t.Fatal("TLS identity validation missing")
			}
		})
	}
}

func TestConfigurationSnapshotsOverridesAndRoundsDown(t *testing.T) {
	c := localConfig()
	scope := Scope{ProjectID: testProject, Environment: "production"}
	c.Overrides = map[Scope]Policy{scope: {Rate: 1.2349, Burst: 100}}
	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	delete(c.Overrides, scope)
	p, ok := limiter.overrides[scope]
	if !ok || p.refillPerMillisecond != 1234 {
		t.Fatal("override was not snapshotted or rounded conservatively")
	}
	belowBoundary := math.Nextafter(0.117, 0)
	normalized, err := normalize(Policy{Rate: belowBoundary, Burst: 50}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.refillPerMillisecond != 116 {
		t.Fatal("floating-point multiplication rounded a configured rate upward")
	}
}

func TestInvalidCallsFailBeforeDependencyAccess(t *testing.T) {
	limiter, err := New(localConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	for _, tt := range []struct {
		project, environment string
		count                int
	}{
		{testProject, "development", -1}, {testProject, "development", 0}, {testProject, "development", 51},
		{"", "development", 1}, {testProject + ":foreign", "development", 1},
		{strings.ToUpper(testProject), "development", 1}, {testProject, "Development", 1}, {testProject, "prod", 1}, {testProject, "{development}", 1},
	} {
		var problem *fault.Error
		if err := limiter.ReserveCollection(context.Background(), tt.project, tt.environment, tt.count); !errors.As(err, &problem) || problem.Kind != fault.Invalid {
			t.Fatalf("invalid call did not fail locally: %v", err)
		}
	}
}
