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
)

const (
	// DefaultKeyPrefix is the independent namespace for rejection windows.
	DefaultKeyPrefix = "evia:admission"
	// DefaultWindows is the diagnostic lookback in one-hour buckets.
	DefaultWindows = 24
	maxWindows     = 48
	maxPoolSize    = 64
	// windowRetention keeps one full day plus a grace hour for late reads.
	windowRetention = 25 * time.Hour
	hourWindow      = time.Hour
)

var (
	prefixPattern  = regexp.MustCompile(`^[a-z][a-z0-9:_-]{0,63}$`)
	projectPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
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
	environments = []string{"development", "staging", "production"}
)

// Config configures the independent Redis connection pool for rejection stats.
type Config struct {
	URL                string
	KeyPrefix          string
	Timeout            time.Duration
	PoolSize           int
	AllowInsecureLocal bool
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
	client  *redis.Client
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
	opts, err := clientOptions(c)
	if err != nil {
		return nil, err
	}
	return &Recorder{client: redis.NewClient(opts), prefix: c.KeyPrefix, timeout: c.Timeout, now: func() time.Time { return time.Now().UTC() }}, nil
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
		(environment == "development" || environment == "staging" || environment == "production")
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
