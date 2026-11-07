//go:build integration

package budget

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func realBudget(t *testing.T, change func(*Config)) (*Limiter, Config) {
	t.Helper()
	endpoint := os.Getenv("EVIA_TEST_REDIS_URL")
	if endpoint == "" {
		t.Fatal("EVIA_TEST_REDIS_URL is required for budget integration tests")
	}
	c := DefaultConfig(endpoint)
	c.AllowInsecureLocal = true
	c.KeyPrefix = "evia:test:budget:" + strings.ReplaceAll(ids.New(), "-", "")
	c.Default = Policy{Rate: 0.001, Burst: 50}
	c.Timeout, c.PoolSize = time.Second, 16
	if change != nil {
		change(&c)
	}
	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := limiter.client.Ping(ctx).Err(); err != nil {
		limiter.Close()
		t.Fatalf("real Redis is required: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var cursor uint64
		for {
			keys, next, err := limiter.client.Scan(ctx, cursor, c.KeyPrefix+":*", 100).Result()
			if err != nil {
				t.Error(err)
				break
			}
			if len(keys) > 0 {
				if err := limiter.client.Del(ctx, keys...).Err(); err != nil {
					t.Error(err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		limiter.Close()
	})
	return limiter, c
}

func isKind(err error, kind fault.Kind) bool {
	var problem *fault.Error
	return errors.As(err, &problem) && problem.Kind == kind
}
func allowed(t *testing.T, limiter *Limiter, project, env string, count int) {
	t.Helper()
	if err := limiter.ReserveCollection(context.Background(), project, env, count); err != nil {
		t.Fatal(err)
	}
}

func denied(t *testing.T, limiter *Limiter, project, env string, count int, want fault.Kind) {
	t.Helper()
	if err := limiter.ReserveCollection(context.Background(), project, env, count); !isKind(err, want) {
		t.Fatalf("unexpected admission result: %v", err)
	}
}

func TestRedisAtomicAcrossIndependentReplicas(t *testing.T) {
	first, cfg := realBudget(t, func(c *Config) { c.Default = Policy{Rate: 0.01, Burst: 100}; c.PoolSize = 8 })
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	project := ids.New()
	var accepted, limited, failed atomic.Int64
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := range 100 {
		workers.Go(func() {
			<-start
			limiter := first
			if i%2 != 0 {
				limiter = second
			}
			err := limiter.ReserveCollection(context.Background(), project, "production", 2)
			if err == nil {
				accepted.Add(2)
			} else if isKind(err, fault.Limited) {
				limited.Add(1)
			} else {
				failed.Add(1)
			}
		})
	}
	close(start)
	workers.Wait()
	if accepted.Load() != 100 || limited.Load() != 50 || failed.Load() != 0 {
		t.Fatalf("global admission events=%d limited batches=%d unexpected failures=%d", accepted.Load(), limited.Load(), failed.Load())
	}
	denied(t, second, project, "production", 1, fault.Limited)
	if first.client.PoolStats().TotalConns > 8 || second.client.PoolStats().TotalConns > 8 {
		t.Fatal("client connection ceiling exceeded")
	}
}

func TestRedisScopeIsolationAndServerConfiguredOverride(t *testing.T) {
	project := ids.New()
	limiter, _ := realBudget(t, func(c *Config) {
		c.Overrides = map[Scope]Policy{{ProjectID: project, Environment: "production"}: {Rate: 0.01, Burst: 100}}
	})
	allowed(t, limiter, project, "development", 50)
	denied(t, limiter, project, "development", 1, fault.Limited)
	allowed(t, limiter, project, "staging", 50)
	allowed(t, limiter, ids.New(), "development", 50)
	allowed(t, limiter, project, "production", 50)
	allowed(t, limiter, project, "production", 50)
	denied(t, limiter, project, "production", 1, fault.Limited)
	var keys []string
	var cursor uint64
	for {
		page, next, err := limiter.client.Scan(context.Background(), cursor, limiter.prefix+":*", 100).Result()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, page...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(keys) != 4 {
		t.Fatalf("scope cardinality not bounded to used identities: keys=%d", len(keys))
	}
}

func TestRedisDeniesWholeBatchWithoutPartialDebit(t *testing.T) {
	limiter, _ := realBudget(t, nil)
	project := ids.New()
	allowed(t, limiter, project, "development", 49)
	denied(t, limiter, project, "development", 2, fault.Limited)
	allowed(t, limiter, project, "development", 1)
	denied(t, limiter, project, "development", 1, fault.Limited)
}

func TestRedisRefillsUsingServerTime(t *testing.T) {
	limiter, _ := realBudget(t, func(c *Config) { c.Default = Policy{Rate: 100, Burst: 50} })
	project := ids.New()
	allowed(t, limiter, project, "development", 50)
	start, err := limiter.client.HGet(context.Background(), limiter.key(Scope{ProjectID: project, Environment: "development"}), "at").Int64()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(850 * time.Millisecond)
	for {
		err := limiter.ReserveCollection(context.Background(), project, "development", 50)
		if err == nil {
			end, err := limiter.client.HGet(context.Background(), limiter.key(Scope{ProjectID: project, Environment: "development"}), "at").Int64()
			if err != nil {
				t.Fatal(err)
			}
			if end-start < 500 {
				t.Fatalf("full burst replenished prematurely after %d milliseconds", end-start)
			}
			return
		}
		if !isKind(err, fault.Limited) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("burst did not refill within bounded real-time window")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRedisKeysExpireAfterFullRefill(t *testing.T) {
	limiter, _ := realBudget(t, func(c *Config) { c.Default = Policy{Rate: 1000, Burst: 50} })
	project := ids.New()
	scope := Scope{ProjectID: project, Environment: "development"}
	allowed(t, limiter, project, scope.Environment, 50)
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		exists, err := limiter.client.Exists(context.Background(), limiter.key(scope)).Result()
		if err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			break
		}
		ttl, err := limiter.client.PTTL(context.Background(), limiter.key(scope)).Result()
		if err != nil {
			t.Fatal(err)
		}
		if ttl > 50*time.Millisecond {
			t.Fatalf("unexpectedly extended idle lifetime: %v", ttl)
		}
		if time.Now().After(deadline) {
			t.Fatal("idle budget key did not expire")
		}
		time.Sleep(5 * time.Millisecond)
	}
	allowed(t, limiter, project, scope.Environment, 50)
}

func TestRedisDependencyFailureFailsClosed(t *testing.T) {
	limiter, _ := realBudget(t, nil)
	closed, err := New(localConfig())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	denied(t, closed, ids.New(), "development", 1, fault.Unavailable)
	unreachable, err := New(localConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer unreachable.Close()
	started := time.Now()
	denied(t, unreachable, ids.New(), "development", 1, fault.Unavailable)
	if time.Since(started) > time.Second {
		t.Fatal("dependency failure exceeded bounded call timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = limiter.ReserveCollection(ctx, ids.New(), "development", 1)
	if !isKind(err, fault.Unavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not fail-closed: %v", err)
	}
	project := ids.New()
	key := limiter.key(Scope{ProjectID: project, Environment: "development"})
	if err := limiter.client.Set(context.Background(), key, "corrupt", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	denied(t, limiter, project, "development", 1, fault.Unavailable)
}

func TestRedisPolicyDisagreementCannotResetCredits(t *testing.T) {
	limiter, cfg := realBudget(t, nil)
	project := ids.New()
	allowed(t, limiter, project, "development", 50)
	cfg.Default = Policy{Rate: 0.01, Burst: 100}
	other, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	denied(t, other, project, "development", 50, fault.Unavailable)
	denied(t, limiter, project, "development", 1, fault.Limited)
}

func TestRedisBackwardClockStateCannotMintCredits(t *testing.T) {
	limiter, _ := realBudget(t, nil)
	project := ids.New()
	key := limiter.key(Scope{ProjectID: project, Environment: "development"})
	allowed(t, limiter, project, "development", 50)
	serverTime, err := limiter.client.Time(context.Background()).Result()
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.client.HSet(context.Background(), key, "at", serverTime.Add(time.Second).UnixMilli()).Err(); err != nil {
		t.Fatal(err)
	}
	denied(t, limiter, project, "development", 1, fault.Limited)
	credits, err := limiter.client.HGet(context.Background(), key, "tokens").Int64()
	if err != nil || credits != 0 {
		t.Fatalf("clock rollback created credits: %d %v", credits, err)
	}
}
