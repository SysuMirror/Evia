// Package budget enforces project and environment collection admission in Redis.
package budget

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

const (
	// ContractMaxBatchEvents matches the authoritative collect-request schema.
	ContractMaxBatchEvents = 50
	maxPoolSize            = 256
	maxPolicyValue         = 1_000_000
	tokenUnits             = int64(1_000_000)
	maxIdleMilliseconds    = int64((24 * time.Hour) / time.Millisecond)
)

var (
	prefixPattern  = regexp.MustCompile(`^[a-z][a-z0-9:_-]{0,63}$`)
	projectPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reserveScript  = redis.NewScript(reserveLua)
)

// Policy is an event rate per second and an event burst, shared by all replicas.
// Rates are rounded down to increments of 0.001 events/second, never up.
type Policy struct {
	Rate  float64
	Burst int
}

// Scope is trusted server-resolved identity, never a browser-supplied quota key.
type Scope struct{ ProjectID, Environment string }

type Config struct {
	URL            string
	KeyPrefix      string
	Default        Policy
	Overrides      map[Scope]Policy
	MaxBatchEvents int
	Timeout        time.Duration
	PoolSize       int
	// AllowInsecureLocal permits redis:// only for localhost or a loopback IP.
	// Non-local connections always require rediss:// with certificate validation.
	AllowInsecureLocal bool
}

// DefaultConfig returns explicit initial operating limits, not measured capacity.
// Callers must opt in to plaintext local development separately.
func DefaultConfig(endpoint string) Config {
	return Config{URL: endpoint, KeyPrefix: "evia:collection", Default: Policy{Rate: 1000, Burst: 2000}, MaxBatchEvents: ContractMaxBatchEvents, Timeout: 200 * time.Millisecond, PoolSize: 32}
}

type policy struct{ capacity, refillPerMillisecond, idleMilliseconds int64 }

type Limiter struct {
	client        *redis.Client
	prefix        string
	defaultPolicy policy
	overrides     map[Scope]policy
	maxBatch      int
	timeout       time.Duration
}

func validScope(scope Scope) bool {
	return projectPattern.MatchString(scope.ProjectID) && (scope.Environment == "development" || scope.Environment == "staging" || scope.Environment == "production")
}

func normalize(p Policy, maxBatch int) (policy, error) {
	if math.IsNaN(p.Rate) || math.IsInf(p.Rate, 0) || p.Rate < 0.001 || p.Rate > maxPolicyValue || p.Burst < maxBatch || p.Burst > maxPolicyValue {
		return policy{}, errors.New("collection budget rate must be finite and between 0.001 and 1000000, and burst must cover the batch limit without exceeding 1000000")
	}
	// All Lua arithmetic is bounded exact integer arithmetic below 2^53.
	// Multiplying a float and then flooring can round a value just below a
	// boundary upward. Normalize its exact rational value once at startup.
	scaled := new(big.Rat).Mul(new(big.Rat).SetFloat64(p.Rate), big.NewRat(1000, 1))
	refill := new(big.Int).Quo(scaled.Num(), scaled.Denom()).Int64()
	capacity := int64(p.Burst) * tokenUnits
	idle := (capacity + refill - 1) / refill
	if idle > maxIdleMilliseconds {
		return policy{}, errors.New("collection budget full refill must take no longer than 24 hours")
	}
	return policy{capacity: capacity, refillPerMillisecond: refill, idleMilliseconds: idle}, nil
}

func clientOptions(c Config) (*redis.Options, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "redis" && u.Scheme != "rediss") {
		return nil, errors.New("collection budget URL must be redis(s) with a host and without query parameters or fragments")
	}
	if u.Scheme == "redis" {
		ip := net.ParseIP(u.Hostname())
		if !c.AllowInsecureLocal || u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("plaintext collection budget connections require explicit loopback development configuration")
		}
	}
	opts, err := redis.ParseURL(c.URL)
	if err != nil {
		return nil, errors.New("collection budget URL is invalid")
	}
	_, port, err := net.SplitHostPort(opts.Addr)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || opts.DB < 0 {
		return nil, errors.New("collection budget URL requires a valid TCP port and a nonnegative database")
	}
	if u.Scheme == "rediss" {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	}
	opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout, opts.PoolTimeout = c.Timeout, c.Timeout, c.Timeout, c.Timeout
	opts.ContextTimeoutEnabled = true
	opts.MaxRetries = -1 // An ambiguous debit must not be automatically replayed.
	opts.DialerRetries = 1
	opts.PipelinePoolSize = -1
	opts.Protocol = 2
	opts.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	opts.PoolSize, opts.MaxActiveConns, opts.MaxIdleConns = c.PoolSize, c.PoolSize, c.PoolSize
	opts.MinIdleConns = 0
	opts.ConnMaxIdleTime, opts.ConnMaxLifetime = time.Minute, 5*time.Minute
	return opts, nil
}

// New validates and snapshots configuration without performing network I/O.
// Policy disagreements for an existing bucket fail closed rather than resetting it.
func New(c Config) (*Limiter, error) {
	if !prefixPattern.MatchString(c.KeyPrefix) {
		return nil, errors.New("collection budget prefix must be 1 to 64 lowercase namespaced characters")
	}
	if c.MaxBatchEvents < 1 || c.MaxBatchEvents > ContractMaxBatchEvents {
		return nil, errors.New("collection budget batch limit must be between 1 and 50")
	}
	if c.Timeout <= 0 || c.Timeout > 5*time.Second {
		return nil, errors.New("collection budget timeout must be positive and no greater than 5 seconds")
	}
	if c.PoolSize < 1 || c.PoolSize > maxPoolSize {
		return nil, errors.New("collection budget pool size must be between 1 and 256")
	}
	p, err := normalize(c.Default, c.MaxBatchEvents)
	if err != nil {
		return nil, err
	}
	overrides := make(map[Scope]policy, len(c.Overrides))
	for scope, candidate := range c.Overrides {
		if !validScope(scope) {
			return nil, errors.New("collection budget override has an invalid project or environment")
		}
		normalized, e := normalize(candidate, c.MaxBatchEvents)
		if e != nil {
			return nil, e
		}
		overrides[scope] = normalized
	}
	opts, err := clientOptions(c)
	if err != nil {
		return nil, err
	}
	return &Limiter{client: redis.NewClient(opts), prefix: c.KeyPrefix, defaultPolicy: p, overrides: overrides, maxBatch: c.MaxBatchEvents, timeout: c.Timeout}, nil
}

func (l *Limiter) key(scope Scope) string {
	// A single hash-tagged key per project/environment is cluster-slot compatible.
	return l.prefix + ":{" + scope.ProjectID + ":" + scope.Environment + "}"
}

func unavailable(err error) error {
	return &fault.Error{Kind: fault.Unavailable, Code: "COLLECTION_BUDGET_UNAVAILABLE", Message: "采集预算服务暂时不可用，请稍后重试", Cause: err}
}

// ReserveCollection atomically charges the entire authenticated SDK batch once.
// Nothing is refunded on invalid event items, queue failure or uncertain responses.
func (l *Limiter) ReserveCollection(ctx context.Context, projectID, environment string, eventCount int) error {
	scope := Scope{ProjectID: projectID, Environment: environment}
	if !validScope(scope) || eventCount < 1 || eventCount > l.maxBatch {
		return fault.New(fault.Invalid, "INVALID_COLLECTION_BUDGET", "采集预算范围或事件数量无效")
	}
	p := l.defaultPolicy
	if configured, ok := l.overrides[scope]; ok {
		p = configured
	}
	bounded, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	allowed, err := reserveScript.Run(bounded, l.client, []string{l.key(scope)}, p.capacity, p.refillPerMillisecond, int64(eventCount)*tokenUnits, p.idleMilliseconds, maxIdleMilliseconds).Int64()
	if err != nil {
		return unavailable(err)
	}
	if allowed == 0 {
		return fault.New(fault.Limited, "COLLECTION_BUDGET_EXCEEDED", "项目当前采集额度已用尽，请稍后重试")
	}
	if allowed != 1 {
		return unavailable(errors.New("unexpected collection budget result"))
	}
	return nil
}

func (l *Limiter) Close() error { return l.client.Close() }
