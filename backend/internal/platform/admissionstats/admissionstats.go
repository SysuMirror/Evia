// Package admissionstats keeps durable, replica-shared collection rejection
// counters in Redis so authorized diagnostics can explain admission failures.
//
// Counters use fixed hourly windows keyed by project and environment. Every
// key is hash-tagged for cluster-slot locality and expires after a bounded
// retention period, so reads never need SCAN and memory cannot grow without
// limit. Write failures must never change an admission decision: Record is
// fail-open and only Read surfaces Redis unavailability to diagnostics.
package admissionstats

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
)

const (
	// DefaultKeyPrefix is the independent namespace for rejection windows.
	DefaultKeyPrefix = "evia:admission"
	// DefaultWindows is the diagnostic lookback in one-hour buckets.
	DefaultWindows   = 24
	maxWindows       = 48
	maxPoolSize      = 64
	maxSentinelAddrs = 8
	maxClusterAddrs  = 8

	clusterStateReloadInterval = 3 * time.Second
	// windowRetention keeps one full day plus a grace hour for late reads.
	windowRetention = 25 * time.Hour
	hourWindow      = time.Hour
)

var (
	prefixPattern       = regexp.MustCompile(`^[a-z][a-z0-9:_-]{0,63}$`)
	projectPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sentinelNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// Reasons is the bounded rejection vocabulary. Codes outside this set are
	// dropped on write so Redis hash cardinality stays controlled.
	Reasons = []string{
		"COLLECTION_BUDGET_EXCEEDED",
		"EVENT_TOO_LARGE",
		"INVALID_EVENT",
		"EVENT_TOO_OLD",
		"EVENT_TIME_IN_FUTURE",
		"SCHEMA_NOT_PUBLISHED",
		"SOURCE_KIND_FORBIDDEN",
		"PROPERTY_SCHEMA_MISMATCH",
		"INVALID_EVENT_CONTEXT",
		"SCHEMA_NOT_READY",
		"EVENT_PLAN_UNAVAILABLE",
		"EVENT_PLAN_INVALID",
		"EVENT_PLAN_BUSY",
		"EVENT_PLAN_SCOPE_MISMATCH",
		"EVENT_PLAN_NOT_READY",
	}
	knownReasons = func() map[string]struct{} {
		out := make(map[string]struct{}, len(Reasons))
		for _, reason := range Reasons {
			out[reason] = struct{}{}
		}
		return out
	}()
	environments = env.Environments
)

// Config configures the independent Redis connection pool for rejection stats.
type Config struct {
	URL                string
	KeyPrefix          string
	Timeout            time.Duration
	PoolSize           int
	AllowInsecureLocal bool
	// Sentinel optionally enables Redis Sentinel discovery. The zero value
	// keeps the single-endpoint URL path; when set, URL must stay empty.
	Sentinel SentinelConfig
	// Cluster optionally enables sharded Redis Cluster routing. When set,
	// both URL and Sentinel must stay empty.
	Cluster ClusterConfig
}

// ClusterConfig configures Redis Cluster slot routing from a seed list. The
// recorder discovers the slot map and follows redirects and promotions itself.
type ClusterConfig struct {
	Addrs      []string
	Password   string
	TLSEnabled bool
}

// SentinelConfig configures discovery of the current Redis master through a
// Sentinel quorum. The recorder follows promoted replicas automatically.
type SentinelConfig struct {
	MasterName       string
	Addrs            []string
	Password         string
	SentinelPassword string
	TLSEnabled       bool
}

// DefaultConfig returns explicit operating bounds for local development.
func DefaultConfig(endpoint string) Config {
	return Config{URL: endpoint, KeyPrefix: DefaultKeyPrefix, Timeout: 200 * time.Millisecond, PoolSize: 16}
}

// ReasonCount is one aggregated counter value.
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// Window holds the non-zero counters of one fixed hourly bucket.
type Window struct {
	BucketAt time.Time     `json:"bucket_at"`
	Reasons  []ReasonCount `json:"reasons"`
}

// EnvironmentWindows orders windows from oldest to newest for one environment.
type EnvironmentWindows struct {
	Environment string   `json:"environment"`
	Windows     []Window `json:"windows"`
}

// ProjectRead is the bounded diagnostic view across every known environment.
type ProjectRead struct {
	ProjectID    string               `json:"project_id"`
	Environments []EnvironmentWindows `json:"environments"`
	Totals       []ReasonCount        `json:"totals"`
}

// Recorder is the fail-open rejection counter for one Redis namespace.
type Recorder struct {
	client  redis.UniversalClient
	prefix  string
	timeout time.Duration
	now     func() time.Time
	dropped atomic.Int64
}

// New validates the configuration and snapshots the client without network I/O.
func New(c Config) (*Recorder, error) {
	if !prefixPattern.MatchString(c.KeyPrefix) {
		return nil, errors.New("admission stats prefix must be 1 to 64 lowercase namespaced characters")
	}
	if c.Timeout <= 0 || c.Timeout > 5*time.Second {
		return nil, errors.New("admission stats timeout must be positive and no greater than 5 seconds")
	}
	if c.PoolSize < 1 || c.PoolSize > maxPoolSize {
		return nil, errors.New("admission stats pool size must be between 1 and 64")
	}
	var client redis.UniversalClient
	if c.Sentinel.MasterName != "" {
		failover, err := failoverOptions(c)
		if err != nil {
			return nil, err
		}
		client = redis.NewFailoverClient(failover)
	} else if len(c.Cluster.Addrs) > 0 {
		clusterOpts, err := clusterOptions(c)
		if err != nil {
			return nil, err
		}
		client = redis.NewClusterClient(clusterOpts)
	} else {
		opts, err := clientOptions(c)
		if err != nil {
			return nil, err
		}
		client = redis.NewClient(opts)
	}
	return &Recorder{client: client, prefix: c.KeyPrefix, timeout: c.Timeout, now: func() time.Time { return time.Now().UTC() }}, nil
}

func clientOptions(c Config) (*redis.Options, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "redis" && u.Scheme != "rediss") {
		return nil, errors.New("admission stats URL must be redis(s) with a host and without query parameters or fragments")
	}
	if u.Scheme == "redis" {
		ip := net.ParseIP(u.Hostname())
		if !c.AllowInsecureLocal || u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("plaintext admission stats connections require explicit loopback development configuration")
		}
	}
	opts, err := redis.ParseURL(c.URL)
	if err != nil {
		return nil, errors.New("admission stats URL is invalid")
	}
	_, port, err := net.SplitHostPort(opts.Addr)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || opts.DB < 0 {
		return nil, errors.New("admission stats URL requires a valid TCP port and a nonnegative database")
	}
	if u.Scheme == "rediss" {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	}
	opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout, opts.PoolTimeout = c.Timeout, c.Timeout, c.Timeout, c.Timeout
	opts.ContextTimeoutEnabled = true
	opts.MaxRetries = 0
	opts.PoolSize, opts.MaxActiveConns, opts.MaxIdleConns = c.PoolSize, c.PoolSize, c.PoolSize
	opts.MinIdleConns = 0
	opts.ConnMaxIdleTime, opts.ConnMaxLifetime = time.Minute, 5*time.Minute
	return opts, nil
}

// failoverOptions mirrors the safety invariants of clientOptions for Sentinel
// discovery: bounded timeouts, the same pool caps and no automatic retries. It
// performs no network I/O.
func failoverOptions(c Config) (*redis.FailoverOptions, error) {
	if c.URL != "" {
		return nil, errors.New("admission stats Redis URL and Sentinel discovery are mutually exclusive")
	}
	if !sentinelNamePattern.MatchString(c.Sentinel.MasterName) {
		return nil, errors.New("admission stats Sentinel master name must be 1 to 64 letters, digits, dots, underscores or hyphens")
	}
	if len(c.Sentinel.Addrs) < 1 || len(c.Sentinel.Addrs) > maxSentinelAddrs {
		return nil, errors.New("admission stats requires between 1 and 8 Sentinel endpoints")
	}
	addrs := make([]string, len(c.Sentinel.Addrs))
	for i, raw := range c.Sentinel.Addrs {
		host, port, err := net.SplitHostPort(raw)
		if err != nil || host == "" || len(host) > 253 || !validTCPPort(port) {
			return nil, errors.New("admission stats Sentinel endpoint must be a valid TCP address")
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
		MaxRetries:            0,
		PoolSize:              c.PoolSize,
		MaxActiveConns:        c.PoolSize,
		MaxIdleConns:          c.PoolSize,
		MinIdleConns:          0,
		ConnMaxIdleTime:       time.Minute,
		ConnMaxLifetime:       5 * time.Minute,
	}
	if c.Sentinel.TLSEnabled {
		// Endpoints are learned at runtime, so leave ServerName empty and let
		// crypto/tls verify each connection against its actual dialed host.
		fo.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return fo, nil
}

// clusterOptions mirrors the safety invariants of clientOptions for the
// sharded Redis Cluster path: bounded timeouts, per-node pool caps, RESP2 and
// MaxRetries=0 so a failover window only drops writes. MOVED/ASK redirects
// route commands that never executed and keep their default behavior.
func clusterOptions(c Config) (*redis.ClusterOptions, error) {
	if c.URL != "" || c.Sentinel.MasterName != "" {
		return nil, errors.New("Redis Cluster routing is mutually exclusive with the URL and Sentinel discovery modes")
	}
	if len(c.Cluster.Addrs) < 1 || len(c.Cluster.Addrs) > maxClusterAddrs {
		return nil, errors.New("admission stats requires between 1 and 8 Redis Cluster seed endpoints")
	}
	addrs := make([]string, len(c.Cluster.Addrs))
	for i, raw := range c.Cluster.Addrs {
		host, port, err := net.SplitHostPort(raw)
		if err != nil || host == "" || len(host) > 253 || !validTCPPort(port) {
			return nil, errors.New("admission stats Redis Cluster endpoint must be a valid TCP address")
		}
		if !c.Cluster.TLSEnabled && (!c.AllowInsecureLocal || !loopbackHost(host)) {
			return nil, errors.New("plaintext Redis Cluster connections require explicit loopback development configuration")
		}
		addrs[i] = net.JoinHostPort(host, port)
	}
	co := &redis.ClusterOptions{
		Addrs:                 addrs,
		Password:              c.Cluster.Password,
		Protocol:              2,
		DialTimeout:           c.Timeout,
		ReadTimeout:           c.Timeout,
		WriteTimeout:          c.Timeout,
		PoolTimeout:           c.Timeout,
		ContextTimeoutEnabled: true,
		MaxRetries:            0,
		PoolSize:              c.PoolSize,
		MaxActiveConns:        c.PoolSize,
		MaxIdleConns:          c.PoolSize,
		MinIdleConns:          0,
		ConnMaxIdleTime:       time.Minute,
		ConnMaxLifetime:       5 * time.Minute,
		// RESP2 has no push notifications; bound slot-map staleness after a
		// failover instead of relying on the 60s default reload interval.
		ClusterStateReloadInterval: clusterStateReloadInterval,
	}
	if c.Cluster.TLSEnabled {
		co.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return co, nil
}

func validTCPPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// Close releases the connection pool.
func (r *Recorder) Close() error { return r.client.Close() }

// Ping checks the pool without mutating counters.
func (r *Recorder) Ping(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.client.Ping(bounded).Err()
}

// DroppedWrites exposes fail-open telemetry for operational tests.
func (r *Recorder) DroppedWrites() int64 { return r.dropped.Load() }

func validScope(projectID, environment string) bool {
	return projectPattern.MatchString(projectID) &&
		env.Valid(environment)
}

func (r *Recorder) key(projectID, environment string, bucket time.Time) string {
	hour := bucket.Truncate(hourWindow).Unix()
	return r.prefix + ":{" + projectID + ":" + environment + "}:" + strconv.FormatInt(hour, 10)
}

// Record adds the aggregated rejection counts to the current hourly window.
// It never returns an error: admission responses must not change because
// diagnostic bookkeeping is unavailable.
func (r *Recorder) Record(ctx context.Context, projectID, environment string, counts map[string]int) {
	if len(counts) == 0 || !validScope(projectID, environment) {
		return
	}
	fields := make(map[string]int64, len(counts))
	for reason, count := range counts {
		if _, ok := knownReasons[reason]; !ok || count <= 0 {
			continue
		}
		fields[reason] += int64(count)
	}
	if len(fields) == 0 {
		return
	}
	// Telemetry uses its own bounded deadline so a canceled request context
	// cannot skip the final accounting of an already-decided rejection.
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.timeout)
	defer cancel()
	key := r.key(projectID, environment, r.now())
	pipe := r.client.Pipeline()
	for reason, count := range fields {
		pipe.HIncrBy(bounded, key, reason, count)
	}
	pipe.Expire(bounded, key, windowRetention)
	if _, err := pipe.Exec(bounded); err != nil {
		r.dropped.Add(1)
	}
}

// RecordRejections satisfies ingestion.RejectionSink so the collector can
// attach the fail-open recorder without a direct dependency on this package.
func (r *Recorder) RecordRejections(ctx context.Context, projectID, environment string, counts map[string]int) {
	r.Record(ctx, projectID, environment, counts)
}

// Read returns the last windows hourly buckets per environment, oldest first.
// Empty buckets are omitted; totals aggregate every returned window.
func (r *Recorder) Read(ctx context.Context, projectID string, windows int) (ProjectRead, error) {
	if !projectPattern.MatchString(projectID) {
		return ProjectRead{}, errors.New("admission stats project id is invalid")
	}
	if windows < 1 || windows > maxWindows {
		windows = DefaultWindows
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	current := r.now().Truncate(hourWindow)
	out := ProjectRead{ProjectID: projectID, Environments: make([]EnvironmentWindows, 0, len(environments)), Totals: []ReasonCount{}}
	totals := make(map[string]int64)
	for _, environment := range environments {
		entry := EnvironmentWindows{Environment: environment, Windows: []Window{}}
		for offset := windows - 1; offset >= 0; offset-- {
			bucket := current.Add(-time.Duration(offset) * hourWindow)
			raw, err := r.client.HGetAll(bounded, r.key(projectID, environment, bucket)).Result()
			if err != nil {
				return ProjectRead{}, err
			}
			if len(raw) == 0 {
				continue
			}
			window := Window{BucketAt: bucket, Reasons: make([]ReasonCount, 0, len(raw))}
			for _, reason := range Reasons {
				value, ok := raw[reason]
				if !ok {
					continue
				}
				count, err := strconv.ParseInt(value, 10, 64)
				if err != nil || count <= 0 {
					continue
				}
				window.Reasons = append(window.Reasons, ReasonCount{Reason: reason, Count: count})
				totals[reason] += count
			}
			if len(window.Reasons) > 0 {
				entry.Windows = append(entry.Windows, window)
			}
		}
		out.Environments = append(out.Environments, entry)
	}
	for _, reason := range Reasons {
		if count := totals[reason]; count > 0 {
			out.Totals = append(out.Totals, ReasonCount{Reason: reason, Count: count})
		}
	}
	return out, nil
}
