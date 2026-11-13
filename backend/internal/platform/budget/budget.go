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
	"sync/atomic"
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
	maxSentinelAddrs       = 8
)

var (
	prefixPattern       = regexp.MustCompile(`^[a-z][a-z0-9:_-]{0,63}$`)
	projectPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sentinelNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reserveScript       = redis.NewScript(reserveLua)
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
	// Sentinel optionally enables Redis Sentinel discovery. The zero value
	// keeps the single-endpoint URL path; when set, URL must stay empty.
	Sentinel SentinelConfig
}

// SentinelConfig configures discovery of the current Redis master through a
// Sentinel quorum. The client follows promoted replicas automatically, so a
// failover needs no process restart.
type SentinelConfig struct {
	MasterName string
	Addrs      []string
	// Password authenticates against Redis data nodes.
	Password string
	// SentinelPassword authenticates against the Sentinel nodes themselves.
	SentinelPassword string
	// TLSEnabled must be true for every non-loopback deployment. Plaintext is
	// accepted only together with AllowInsecureLocal and loopback endpoints.
	TLSEnabled bool
}

// DefaultConfig returns explicit initial operating limits, not measured capacity.
// Callers must opt in to plaintext local development separately.
func DefaultConfig(endpoint string) Config {
	return Config{URL: endpoint, KeyPrefix: "evia:collection", Default: Policy{Rate: 1000, Burst: 2000}, MaxBatchEvents: ContractMaxBatchEvents, Timeout: 200 * time.Millisecond, PoolSize: 32}
}

type policy struct{ capacity, refillPerMillisecond, idleMilliseconds int64 }

// budgetState is an immutable snapshot published atomically. Maps are never
// mutated after publication, so hot-path reservations can read without locks.
type budgetState struct {
	defaultPolicy policy
	overrides     map[Scope]policy
	// Public policy values are retained so diagnostics can report the exact
	// configured rates without reversing the integer token normalization.
	defaultCfg   Policy
	overrideCfgs map[Scope]Policy
}

type Limiter struct {
	client   *redis.Client
	prefix   string
	maxBatch int
	timeout  time.Duration
	state    atomic.Pointer[budgetState]
}

// ScopePolicy is the externally visible effective policy for one environment.
type ScopePolicy struct {
	ProjectID   string
	Environment string
	Rate        float64
	Burst       int
	Override    bool
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

// failoverOptions mirrors every safety invariant of clientOptions for the
// Sentinel discovery path: bounded timeouts, the same pool caps, RESP2 and the
// no-replay retry policy. It performs no network I/O.
func failoverOptions(c Config) (*redis.FailoverOptions, error) {
	if c.URL != "" {
		return nil, errors.New("collection budget Redis URL and Sentinel discovery are mutually exclusive")
	}
	if !sentinelNamePattern.MatchString(c.Sentinel.MasterName) {
		return nil, errors.New("collection budget Sentinel master name must be 1 to 64 letters, digits, dots, underscores or hyphens")
	}
	if len(c.Sentinel.Addrs) < 1 || len(c.Sentinel.Addrs) > maxSentinelAddrs {
		return nil, errors.New("collection budget requires between 1 and 8 Sentinel endpoints")
	}
	addrs := make([]string, len(c.Sentinel.Addrs))
	for i, raw := range c.Sentinel.Addrs {
		host, port, err := net.SplitHostPort(raw)
		if err != nil || host == "" || len(host) > 253 || !validTCPPort(port) {
			return nil, errors.New("collection budget Sentinel endpoint must be a valid TCP address")
		}
		if !c.Sentinel.TLSEnabled && (!c.AllowInsecureLocal || !loopbackHost(host)) {
			return nil, errors.New("plaintext Sentinel connections require explicit loopback development configuration")
		}
		addrs[i] = net.JoinHostPort(host, port)
	}
	fo := &redis.FailoverOptions{
		MasterName:            c.Sentinel.MasterName,
		SentinelAddrs:         addrs,
		Password:              c.Sentinel.Password,
		SentinelPassword:      c.Sentinel.SentinelPassword,
		Protocol:              2,
		DialTimeout:           c.Timeout,
		ReadTimeout:           c.Timeout,
		WriteTimeout:          c.Timeout,
		PoolTimeout:           c.Timeout,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1, // An ambiguous debit must not be automatically replayed.
		DialerRetries:         1,
		PipelinePoolSize:      -1,
		PoolSize:              c.PoolSize,
		MaxActiveConns:        c.PoolSize,
		MaxIdleConns:          c.PoolSize,
		MinIdleConns:          0,
		ConnMaxIdleTime:       time.Minute,
		ConnMaxLifetime:       5 * time.Minute,
	}
	if c.Sentinel.TLSEnabled {
		// Endpoints are learned at runtime, so leave ServerName empty:
		// crypto/tls then verifies each dialed connection against the hostname
		// it actually connects to instead of a single static name.
		fo.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return fo, nil
}

func validTCPPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
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
	var client *redis.Client
	if c.Sentinel.MasterName != "" {
		failover, err := failoverOptions(c)
		if err != nil {
			return nil, err
		}
		client = redis.NewFailoverClient(failover)
	} else {
		opts, err := clientOptions(c)
		if err != nil {
			return nil, err
		}
		client = redis.NewClient(opts)
	}
	overrideCfgs := make(map[Scope]Policy, len(c.Overrides))
	for scope, candidate := range c.Overrides {
		overrideCfgs[scope] = candidate
	}
	limiter := &Limiter{
		client:   client,
		prefix:   c.KeyPrefix,
		maxBatch: c.MaxBatchEvents,
		timeout:  c.Timeout,
	}
	limiter.state.Store(&budgetState{
		defaultPolicy: p,
		overrides:     overrides,
		defaultCfg:    c.Default,
		overrideCfgs:  overrideCfgs,
	})
	return limiter, nil
}

// Validate applies the same bounds as construction without mutating the
// limiter, so management services can reject unusable policies before commit.
func (l *Limiter) Validate(candidate Policy) error {
	_, err := normalize(candidate, l.maxBatch)
	return err
}

// ReplacePolicies validates and atomically publishes a new policy snapshot.
// Existing Redis buckets keep the old capacity/rate and fail closed until they
// expire or are removed explicitly through ResetBucket; the limiter itself
// performs no network I/O here.
func (l *Limiter) ReplacePolicies(defaultCfg Policy, overrideCfgs map[Scope]Policy) error {
	defaultPolicy, err := normalize(defaultCfg, l.maxBatch)
	if err != nil {
		return err
	}
	overrides := make(map[Scope]policy, len(overrideCfgs))
	copied := make(map[Scope]Policy, len(overrideCfgs))
	for scope, candidate := range overrideCfgs {
		if !validScope(scope) {
			return errors.New("collection budget override has an invalid project or environment")
		}
		normalized, e := normalize(candidate, l.maxBatch)
		if e != nil {
			return e
		}
		overrides[scope] = normalized
		copied[scope] = candidate
	}
	l.state.Store(&budgetState{
		defaultPolicy: defaultPolicy,
		overrides:     overrides,
		defaultCfg:    defaultCfg,
		overrideCfgs:  copied,
	})
	return nil
}

// ResetBucket removes one project/environment bucket so the next reservation
// reinitializes it with the currently published policy instead of failing on a
// stored policy mismatch.
func (l *Limiter) ResetBucket(ctx context.Context, projectID, environment string) error {
	scope := Scope{ProjectID: projectID, Environment: environment}
	if !validScope(scope) {
		return fault.New(fault.Invalid, "INVALID_COLLECTION_BUDGET", "采集预算范围无效")
	}
	bounded, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if err := l.client.Del(bounded, l.key(scope)).Err(); err != nil {
		return unavailable(err)
	}
	return nil
}

// SnapshotPolicies reports the effective public policy for every supported
// environment of one project, marking explicit overrides. It performs no I/O.
func (l *Limiter) SnapshotPolicies(projectID string) []ScopePolicy {
	if !projectPattern.MatchString(projectID) {
		return nil
	}
	state := l.state.Load()
	out := make([]ScopePolicy, 0, 3)
	for _, environment := range []string{"development", "staging", "production"} {
		scope := Scope{ProjectID: projectID, Environment: environment}
		entry := ScopePolicy{ProjectID: projectID, Environment: environment, Rate: state.defaultCfg.Rate, Burst: state.defaultCfg.Burst}
		if configured, ok := state.overrideCfgs[scope]; ok {
			entry.Rate, entry.Burst, entry.Override = configured.Rate, configured.Burst, true
		}
		out = append(out, entry)
	}
	return out
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
	state := l.state.Load()
	p := state.defaultPolicy
	if configured, ok := state.overrides[scope]; ok {
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

// Ping checks the existing bounded Redis connection pool without consuming quota.
func (l *Limiter) Ping(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if err := l.client.Ping(bounded).Err(); err != nil {
		return unavailable(err)
	}
	return nil
}
