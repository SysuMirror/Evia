// Package config parses server-owned configuration without logging credentials.
package config

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

type Kafka struct {
	Brokers                                   []string
	Topic, DeadLetterTopic, Group, ProbeTopic string
	TLSEnabled                                bool
	Username, Password                        string
}
type ClickHouse struct{ URL, Database, Username, Password string }
type Config struct {
	DatabaseURL, Listen, Origin string
	// MetricsListen optionally exposes /metrics and /healthz on a dedicated
	// administrative listener. Empty disables it; worker processes have no other
	// HTTP server, so this is their only observability and liveness endpoint.
	MetricsListen                                                                  string
	CursorKey                                                                      []byte
	SecureCookies                                                                  bool
	SourceSecretKey                                                                []byte
	CollectionEnabled, AnalysisEnabled, DeliveryEnabled, AllowInsecureDependencies bool
	Budget                                                                         budget.Config
	Kafka                                                                          Kafka
	ClickHouse                                                                     ClickHouse
	QueryWorkers                                                                   int
	// RetentionEnforcement gates destructive age-based deletion. The default
	// false makes the retention worker a safe dry-run that only counts expired
	// facts; an operator must explicitly opt in to actually delete them.
	RetentionEnforcement      bool
	RetentionSweepInterval    time.Duration
	IdentityRetentionDays     int
	IdentityRetentionMinCount int
	// IngestLagInterval controls consumer lag sampling on the ingest worker.
	// The default 30s keeps scrape noise low; an explicit zero disables the
	// lag monitor. Other commands leave the field unused.
	IngestLagInterval time.Duration
	// ExportDir holds materialized export artifacts; required for export-worker.
	ExportDir string
	// ArchiveEnabled turns on archive-before-delete in the retention worker.
	// ArchiveDir is required when enabled and holds the JSONL fact archives.
	ArchiveEnabled bool
	ArchiveDir     string
	InstanceID     string
	// ShutdownDelay is the interval between failing readiness (so the load
	// balancer drains the replica) and closing the listener. Bounded to keep
	// a wedged shutdown from hanging the supervisor; zero keeps the legacy
	// immediate-shutdown behavior for non-serve processes.
	ShutdownDelay time.Duration
	// PropertyCatalogPath optionally points to a versioned hot-key catalog
	// JSON file. Empty disables dedicated property columns and leaves every
	// read on the authoritative document. PropertyCatalog holds the parsed,
	// immutable catalog (never nil after a successful Read) so runtime code
	// needs no nil check.
	PropertyCatalogPath string
	PropertyCatalog     *propertycatalog.Catalog
	// PropertyCatalogStrict asserts the operator completed the property-slot
	// backfill; fully covered reads then prune the document fallback.
	PropertyCatalogStrict bool
	// Content encryption publishes a browser-safe public JWK. Private keys are
	// loaded only by content-worker from a secret-mounted file.
	ContentKeyID          string
	ContentPublicJWKPath  string
	ContentPublicJWK      map[string]any
	ContentPrivateJWKPath string
	ContentKMSURL         string
	ContentKMSTokenPath   string
	ContentDir            string
	TelemetrySigningKey   []byte
	ConfigSigningKeyID    string
	ConfigSigningJWKPath  string
}

var (
	topicPattern          = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)
	identifierPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	instancePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sentinelMasterPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

const maxDatabaseHosts = 8

// PostgreSQL libpq TLS modes accepted by pgx, kept as an explicit closed set.
var databaseSSLModes = map[string]struct{}{
	"disable":     {},
	"allow":       {},
	"prefer":      {},
	"require":     {},
	"verify-ca":   {},
	"verify-full": {},
}

func Read(get func(string) string) (Config, error) { return ReadFor("serve", get) }

// ReadFor separates worker dependency settings from browser session settings.
// Administrative migration/bootstrap commands deliberately use only DatabaseURL.
func ReadFor(command string, get func(string) string) (Config, error) {
	c := Config{DatabaseURL: get("EVIA_DATABASE_URL"), SecureCookies: true, QueryWorkers: 2, InstanceID: "evia"}
	if command != "serve" && command != "ingest-worker" && command != "query-worker" && command != "erasure-worker" && command != "identity-worker" && command != "content-worker" && command != "retention-worker" && command != "export-worker" && command != "backfill-worker" && command != "property-slots" {
		return c, errors.New("unknown runtime command")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("EVIA_DATABASE_URL is required")
	}
	if err := ValidateDatabaseURL(c.DatabaseURL); err != nil {
		return c, err
	}
	var err error
	if c.CollectionEnabled, err = boolean(get, "EVIA_COLLECTION_ENABLED", false); err != nil {
		return c, err
	}
	if c.AnalysisEnabled, err = boolean(get, "EVIA_ANALYSIS_ENABLED", false); err != nil {
		return c, err
	}
	if c.AllowInsecureDependencies, err = boolean(get, "EVIA_ALLOW_INSECURE_DEPENDENCIES", false); err != nil {
		return c, err
	}
	if value := get("EVIA_INSTANCE_ID"); value != "" {
		c.InstanceID = value
	}
	if !instancePattern.MatchString(c.InstanceID) {
		return c, errors.New("EVIA_INSTANCE_ID must be a bounded ASCII instance name")
	}
	c.MetricsListen = get("EVIA_METRICS_LISTEN")
	if c.MetricsListen != "" {
		if _, port, err := net.SplitHostPort(c.MetricsListen); err != nil || !validPort(port) {
			return c, errors.New("EVIA_METRICS_LISTEN must be host:port with a valid TCP port or empty to disable")
		}
	}
	c.PropertyCatalogPath = get("EVIA_PROPERTY_CATALOG_PATH")
	if c.PropertyCatalogStrict, err = boolean(get, "EVIA_PROPERTY_SLOTS_STRICT", false); err != nil {
		return c, err
	}
	if c.PropertyCatalogPath == "" {
		// Default-off: no file means the disabled catalog, so deployments
		// without dedicated columns keep their exact current behavior.
		c.PropertyCatalog = propertycatalog.Disabled()
	} else {
		// Fail closed: an unreadable or invalid catalog must stop startup
		// instead of silently downgrading every hot-key read to the document.
		catalog, err := propertycatalog.LoadFile(c.PropertyCatalogPath)
		if err != nil {
			return c, err
		}
		c.PropertyCatalog = catalog
	}
	c.ContentKeyID = get("EVIA_CONTENT_KEY_ID")
	c.ContentPublicJWKPath = get("EVIA_CONTENT_PUBLIC_JWK_PATH")
	if (c.ContentKeyID == "") != (c.ContentPublicJWKPath == "") {
		return c, errors.New("EVIA_CONTENT_KEY_ID and EVIA_CONTENT_PUBLIC_JWK_PATH must be configured together")
	}
	if c.ContentKeyID != "" {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(c.ContentKeyID) {
			return c, errors.New("EVIA_CONTENT_KEY_ID must be a bounded ASCII key id")
		}
		raw, readErr := os.ReadFile(c.ContentPublicJWKPath)
		if readErr != nil || len(raw) == 0 || len(raw) > 16*1024 {
			return c, errors.New("EVIA_CONTENT_PUBLIC_JWK_PATH must contain a bounded public JWK")
		}
		var jwk map[string]any
		if jsonErr := json.Unmarshal(raw, &jwk); jsonErr != nil || jwk["kty"] != "RSA" || jwk["n"] == nil || jwk["e"] == nil || jwk["d"] != nil {
			return c, errors.New("EVIA_CONTENT_PUBLIC_JWK_PATH must contain an RSA public JWK without private parameters")
		}
		c.ContentPublicJWK = jwk
	}
	if raw := get("EVIA_TELEMETRY_SIGNING_KEY"); raw != "" {
		c.TelemetrySigningKey, err = hex.DecodeString(raw)
		if err != nil || len(c.TelemetrySigningKey) != 32 {
			return c, errors.New("EVIA_TELEMETRY_SIGNING_KEY must be 32 random bytes encoded as 64 hexadecimal characters")
		}
	}
	c.ConfigSigningKeyID = get("EVIA_CONFIG_SIGNING_KEY_ID")
	c.ConfigSigningJWKPath = get("EVIA_CONFIG_SIGNING_JWK_PATH")
	if (c.ConfigSigningKeyID == "") != (c.ConfigSigningJWKPath == "") {
		return c, errors.New("EVIA_CONFIG_SIGNING_KEY_ID and EVIA_CONFIG_SIGNING_JWK_PATH must be configured together")
	}
	c.ContentPrivateJWKPath = get("EVIA_CONTENT_PRIVATE_JWK_PATH")
	c.ContentKMSURL = get("EVIA_CONTENT_KMS_URL")
	c.ContentKMSTokenPath = get("EVIA_CONTENT_KMS_TOKEN_PATH")
	c.ContentDir = get("EVIA_CONTENT_DIR")
	if command == "content-worker" {
		localKey := c.ContentPrivateJWKPath != ""
		kmsKey := c.ContentKMSURL != "" && c.ContentKMSTokenPath != ""
		if c.ContentKeyID == "" || c.ContentDir == "" || localKey == kmsKey {
			return c, errors.New("content-worker requires content key/public JWK/content dir and exactly one private JWK or KMS endpoint+token")
		}
	}
	if command == "serve" {
		if err = c.readHTTP(get); err != nil {
			return c, err
		}
		if raw := get("EVIA_SOURCE_SECRET_KEY"); raw != "" {
			c.SourceSecretKey, err = hex.DecodeString(raw)
			if err != nil || len(c.SourceSecretKey) != 32 {
				return c, errors.New("EVIA_SOURCE_SECRET_KEY must be 32 random bytes encoded as 64 hexadecimal characters")
			}
		}
	}
	if command == "ingest-worker" && !c.CollectionEnabled {
		return c, errors.New("ingest-worker requires EVIA_COLLECTION_ENABLED=true")
	}
	if command == "query-worker" && !c.AnalysisEnabled {
		return c, errors.New("query-worker requires EVIA_ANALYSIS_ENABLED=true")
	}
	if c.CollectionEnabled && command == "serve" {
		if err = c.readBudget(get); err != nil {
			return c, err
		}
	}
	if c.CollectionEnabled && command == "serve" || command == "ingest-worker" {
		if err = c.readKafka(get, command == "ingest-worker"); err != nil {
			return c, err
		}
	}
	c.DeliveryEnabled = c.CollectionEnabled && c.Kafka.ProbeTopic != ""
	// The erasure and retention workers only need PostgreSQL plus the
	// ClickHouse executor; they deliberately load neither Kafka nor the Redis
	// admission budget.
	if (c.AnalysisEnabled || c.DeliveryEnabled) && command == "serve" || command == "ingest-worker" || command == "query-worker" || command == "erasure-worker" || command == "identity-worker" || command == "content-worker" || command == "property-slots" || command == "retention-worker" || command == "export-worker" || command == "backfill-worker" {
		if err = c.readClickHouse(get); err != nil {
			return c, err
		}
	}
	if command == "retention-worker" {
		if c.RetentionEnforcement, err = boolean(get, "EVIA_RETENTION_ENFORCEMENT_ENABLED", false); err != nil {
			return c, err
		}
		c.RetentionSweepInterval = 6 * time.Hour
		if raw := get("EVIA_RETENTION_SWEEP_INTERVAL"); raw != "" {
			if c.RetentionSweepInterval, err = time.ParseDuration(raw); err != nil || c.RetentionSweepInterval < time.Minute || c.RetentionSweepInterval > 24*time.Hour {
				return c, errors.New("EVIA_RETENTION_SWEEP_INTERVAL must be between 1m and 24h")
			}
		}
		if c.ArchiveEnabled, err = boolean(get, "EVIA_ARCHIVE_ENABLED", false); err != nil {
			return c, err
		}
		if c.ArchiveEnabled {
			c.ArchiveDir = get("EVIA_ARCHIVE_DIR")
			if c.ArchiveDir == "" {
				return c, errors.New("retention-worker with EVIA_ARCHIVE_ENABLED requires EVIA_ARCHIVE_DIR")
			}
		}
	}
	if command == "backfill-worker" {
		c.ArchiveDir = get("EVIA_ARCHIVE_DIR")
		if c.ArchiveDir == "" {
			return c, errors.New("backfill-worker requires EVIA_ARCHIVE_DIR")
		}
	}
	if command == "query-worker" {
		if c.QueryWorkers, err = integer(get, "EVIA_QUERY_WORKERS", 2, 1, 16); err != nil {
			return c, err
		}
	}
	if command == "identity-worker" {
		if c.IdentityRetentionDays, err = integer(get, "EVIA_IDENTITY_RETENTION_DAYS", 90, 1, 3650); err != nil {
			return c, err
		}
		if c.IdentityRetentionMinCount, err = integer(get, "EVIA_IDENTITY_RETENTION_MIN_REVISIONS", 1000, 1, 100000); err != nil {
			return c, err
		}
	}
	if command == "ingest-worker" {
		c.IngestLagInterval = 30 * time.Second
		if raw := get("EVIA_INGEST_LAG_INTERVAL"); raw != "" {
			if c.IngestLagInterval, err = time.ParseDuration(raw); err != nil ||
				(c.IngestLagInterval != 0 && (c.IngestLagInterval < 5*time.Second || c.IngestLagInterval > 10*time.Minute)) {
				return c, errors.New("EVIA_INGEST_LAG_INTERVAL must be zero or between 5s and 10m")
			}
		}
	}
	if command == "export-worker" {
		c.ExportDir = get("EVIA_EXPORT_DIR")
		if c.ExportDir == "" {
			return c, errors.New("export-worker requires EVIA_EXPORT_DIR")
		}
	}
	if command == "serve" {
		// Optional: when unset, export job APIs still enqueue work but the
		// capability download route is not registered.
		c.ExportDir = get("EVIA_EXPORT_DIR")
	}
	return c, nil
}

func (c *Config) readHTTP(get func(string) string) error {
	c.Listen = get("EVIA_LISTEN")
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if _, port, err := net.SplitHostPort(c.Listen); err != nil || !validPort(port) {
		return errors.New("EVIA_LISTEN must be host:port with a valid TCP port")
	}
	var err error
	c.Origin, err = canonicalOrigin(get("EVIA_PUBLIC_ORIGIN"))
	if err != nil {
		return err
	}
	allowHTTP, err := boolean(get, "EVIA_ALLOW_INSECURE_HTTP", false)
	if err != nil {
		return err
	}
	if strings.HasPrefix(c.Origin, "http://") {
		if !allowHTTP {
			return errors.New("HTTP development requires EVIA_ALLOW_INSECURE_HTTP=true")
		}
		c.SecureCookies = false
	}
	c.CursorKey, err = hex.DecodeString(get("EVIA_CURSOR_KEY"))
	if err != nil || len(c.CursorKey) != 32 {
		return errors.New("EVIA_CURSOR_KEY must be 32 random bytes encoded as 64 hexadecimal characters")
	}
	c.ShutdownDelay = 5 * time.Second
	if raw := get("EVIA_SHUTDOWN_DELAY"); raw != "" {
		if c.ShutdownDelay, err = time.ParseDuration(raw); err != nil || c.ShutdownDelay < 0 || c.ShutdownDelay > 30*time.Second {
			return errors.New("EVIA_SHUTDOWN_DELAY must be a duration between 0 and 30s")
		}
	}
	return nil
}

func (c *Config) readBudget(get func(string) string) error {
	rawURL := get("EVIA_REDIS_URL")
	sentinelAddrs := get("EVIA_REDIS_SENTINEL_ADDRS")
	masterName := get("EVIA_REDIS_SENTINEL_MASTER")
	clusterAddrs := get("EVIA_REDIS_CLUSTER_ADDRS")
	c.Budget = budget.DefaultConfig(rawURL)
	c.Budget.AllowInsecureLocal = c.AllowInsecureDependencies
	switch {
	case clusterAddrs != "":
		if rawURL != "" || sentinelAddrs != "" || masterName != "" {
			return errors.New("EVIA_REDIS_CLUSTER_ADDRS is mutually exclusive with EVIA_REDIS_URL and the Sentinel variables")
		}
		addrs, tlsEnabled, err := readRedisCluster(get, clusterAddrs, c.AllowInsecureDependencies)
		if err != nil {
			return err
		}
		c.Budget.Cluster = budget.ClusterConfig{Addrs: addrs, Password: get("EVIA_REDIS_PASSWORD"), TLSEnabled: tlsEnabled}
		c.Budget.URL = ""
	case sentinelAddrs != "" || masterName != "":
		if rawURL != "" {
			return errors.New("EVIA_REDIS_URL and EVIA_REDIS_SENTINEL_ADDRS are mutually exclusive")
		}
		if sentinelAddrs == "" || masterName == "" {
			return errors.New("EVIA_REDIS_SENTINEL_ADDRS and EVIA_REDIS_SENTINEL_MASTER must be configured together")
		}
		sentinel, err := readRedisSentinel(get, sentinelAddrs, masterName, c.AllowInsecureDependencies)
		if err != nil {
			return err
		}
		c.Budget.Sentinel = sentinel
		c.Budget.URL = ""
	default:
		if err := dependencyURL(c.Budget.URL, "redis", "rediss", true, c.AllowInsecureDependencies); err != nil {
			return errors.New("EVIA_REDIS_URL must be a secure Redis URL or explicitly allowed loopback development URL")
		}
	}
	if value := get("EVIA_REDIS_KEY_PREFIX"); value != "" {
		c.Budget.KeyPrefix = value
	}
	var err error
	if c.Budget.Default.Rate, err = number(get, "EVIA_COLLECTION_RATE", 1000, 0.001, 1_000_000); err != nil {
		return err
	}
	if c.Budget.Default.Burst, err = integer(get, "EVIA_COLLECTION_BURST", 2000, 50, 1_000_000); err != nil {
		return err
	}
	if c.Budget.PoolSize, err = integer(get, "EVIA_REDIS_POOL_SIZE", 32, 1, 256); err != nil {
		return err
	}
	if raw := get("EVIA_REDIS_TIMEOUT"); raw != "" {
		c.Budget.Timeout, err = time.ParseDuration(raw)
		if err != nil || c.Budget.Timeout <= 0 || c.Budget.Timeout > 5*time.Second {
			return errors.New("EVIA_REDIS_TIMEOUT must be positive and at most 5s")
		}
	}
	return nil
}

// readRedisSentinel validates the closed Sentinel configuration set. The
// default TLS posture matches Kafka: TLS on unless every endpoint is loopback
// and insecure dependencies are explicitly enabled for local development.
// readRedisCluster validates the closed Redis Cluster seed set and its TLS
// posture, sharing the same host bounds and loopback-only plaintext rule as
// the Sentinel path. It performs no network I/O.
func readRedisCluster(get func(string) string, rawAddrs string, allowLocal bool) ([]string, bool, error) {
	parts := strings.Split(rawAddrs, ",")
	if len(parts) < 1 || len(parts) > 8 {
		return nil, false, errors.New("EVIA_REDIS_CLUSTER_ADDRS supports between 1 and 8 endpoints")
	}
	tlsEnabled, err := boolean(get, "EVIA_REDIS_CLUSTER_TLS_ENABLED", true)
	if err != nil {
		return nil, false, err
	}
	addrs := make([]string, 0, len(parts))
	for _, raw := range parts {
		addr := strings.TrimSpace(raw)
		host, port, err := net.SplitHostPort(addr)
		if err != nil || host == "" || len(host) > 253 || strings.ContainsAny(host, " /?#@\r\n\t") || !validPort(port) {
			return nil, false, errors.New("EVIA_REDIS_CLUSTER_ADDRS contains an invalid TCP endpoint")
		}
		if !tlsEnabled && (!allowLocal || !loopback(host)) {
			return nil, false, errors.New("plaintext Redis Cluster requires EVIA_ALLOW_INSECURE_DEPENDENCIES=true and loopback endpoints")
		}
		addrs = append(addrs, addr)
	}
	return addrs, tlsEnabled, nil
}

func readRedisSentinel(get func(string) string, rawAddrs, masterName string, allowLocal bool) (budget.SentinelConfig, error) {
	if !sentinelMasterPattern.MatchString(masterName) {
		return budget.SentinelConfig{}, errors.New("EVIA_REDIS_SENTINEL_MASTER must be 1 to 64 letters, digits, dots, underscores or hyphens")
	}
	parts := strings.Split(rawAddrs, ",")
	if len(parts) < 1 || len(parts) > 8 {
		return budget.SentinelConfig{}, errors.New("EVIA_REDIS_SENTINEL_ADDRS supports between 1 and 8 endpoints")
	}
	addrs := make([]string, 0, len(parts))
	for _, raw := range parts {
		addr := strings.TrimSpace(raw)
		host, port, err := net.SplitHostPort(addr)
		if err != nil || host == "" || len(host) > 253 || strings.ContainsAny(host, " /?#@\r\n\t") || !validPort(port) {
			return budget.SentinelConfig{}, errors.New("EVIA_REDIS_SENTINEL_ADDRS contains an invalid TCP endpoint")
		}
		addrs = append(addrs, addr)
	}
	tlsEnabled, err := boolean(get, "EVIA_REDIS_SENTINEL_TLS_ENABLED", true)
	if err != nil {
		return budget.SentinelConfig{}, err
	}
	if !tlsEnabled {
		if !allowLocal {
			return budget.SentinelConfig{}, errors.New("plaintext Redis Sentinel requires EVIA_ALLOW_INSECURE_DEPENDENCIES=true")
		}
		for _, addr := range addrs {
			host, _, _ := net.SplitHostPort(addr)
			if !loopback(host) {
				return budget.SentinelConfig{}, errors.New("plaintext Redis Sentinel endpoints must all be loopback addresses")
			}
		}
	}
	return budget.SentinelConfig{
		MasterName:       masterName,
		Addrs:            addrs,
		Password:         get("EVIA_REDIS_PASSWORD"),
		SentinelPassword: get("EVIA_REDIS_SENTINEL_PASSWORD"),
		TLSEnabled:       tlsEnabled,
	}, nil
}

func (c *Config) readKafka(get func(string) string, worker bool) error {
	c.Kafka = Kafka{Topic: get("EVIA_KAFKA_TOPIC"), DeadLetterTopic: get("EVIA_KAFKA_DEAD_LETTER_TOPIC"), Group: get("EVIA_KAFKA_GROUP"), ProbeTopic: get("EVIA_KAFKA_PROBE_TOPIC"), Username: get("EVIA_KAFKA_USERNAME"), Password: get("EVIA_KAFKA_PASSWORD")}
	var err error
	if c.Kafka.TLSEnabled, err = boolean(get, "EVIA_KAFKA_TLS_ENABLED", true); err != nil {
		return err
	}
	if get("EVIA_KAFKA_BROKERS") == "" {
		return errors.New("EVIA_KAFKA_BROKERS is required when collection is enabled")
	}
	c.Kafka.Brokers = strings.Split(get("EVIA_KAFKA_BROKERS"), ",")
	if len(c.Kafka.Brokers) > 16 {
		return errors.New("EVIA_KAFKA_BROKERS supports at most 16 endpoints")
	}
	for i, raw := range c.Kafka.Brokers {
		broker := strings.TrimSpace(raw)
		host, port, err := net.SplitHostPort(broker)
		if err != nil || host == "" || len(host) > 253 || strings.ContainsAny(host, " /?#@\r\n\t") || !validPort(port) {
			return errors.New("EVIA_KAFKA_BROKERS contains an invalid TCP endpoint")
		}
		if !c.Kafka.TLSEnabled && (!c.AllowInsecureDependencies || !loopback(host)) {
			return errors.New("plaintext Kafka requires explicit loopback development configuration")
		}
		c.Kafka.Brokers[i] = broker
	}
	if !validTopic(c.Kafka.Topic) || !validTopic(c.Kafka.DeadLetterTopic) || c.Kafka.Topic == c.Kafka.DeadLetterTopic {
		return errors.New("distinct valid EVIA_KAFKA_TOPIC and EVIA_KAFKA_DEAD_LETTER_TOPIC are required")
	}
	if c.Kafka.ProbeTopic != "" && (!validTopic(c.Kafka.ProbeTopic) || c.Kafka.ProbeTopic == c.Kafka.Topic || c.Kafka.ProbeTopic == c.Kafka.DeadLetterTopic) {
		return errors.New("EVIA_KAFKA_PROBE_TOPIC must be a separate valid topic")
	}
	if worker && (!instancePattern.MatchString(c.Kafka.Group) || len(c.Kafka.Group) > 64) {
		return errors.New("EVIA_KAFKA_GROUP must be a bounded consumer group name")
	}
	if (c.Kafka.Username == "") != (c.Kafka.Password == "") {
		return errors.New("Kafka authentication requires both EVIA_KAFKA_USERNAME and EVIA_KAFKA_PASSWORD")
	}
	if c.Kafka.Username != "" && !c.Kafka.TLSEnabled {
		return errors.New("Kafka password authentication requires TLS")
	}
	return nil
}

func (c *Config) readClickHouse(get func(string) string) error {
	c.ClickHouse = ClickHouse{URL: get("EVIA_CLICKHOUSE_URL"), Database: get("EVIA_CLICKHOUSE_DATABASE"), Username: get("EVIA_CLICKHOUSE_USERNAME"), Password: get("EVIA_CLICKHOUSE_PASSWORD")}
	if dependencyURL(c.ClickHouse.URL, "http", "https", false, c.AllowInsecureDependencies) != nil {
		return errors.New("EVIA_CLICKHOUSE_URL must use HTTPS or explicitly allowed loopback HTTP")
	}
	if !identifierPattern.MatchString(c.ClickHouse.Database) {
		return errors.New("EVIA_CLICKHOUSE_DATABASE must be a valid database identifier")
	}
	if (c.ClickHouse.Username == "") != (c.ClickHouse.Password == "") {
		return errors.New("ClickHouse authentication requires both EVIA_CLICKHOUSE_USERNAME and EVIA_CLICKHOUSE_PASSWORD")
	}
	return nil
}

func dependencyURL(raw, plain, secure string, credentials, allowLocal bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Scheme != plain && u.Scheme != secure) || !credentials && u.User != nil || u.Port() != "" && !validPort(u.Port()) {
		return errors.New("invalid dependency URL")
	}
	if !credentials && u.Path != "" && u.Path != "/" {
		return errors.New("invalid dependency path")
	}
	if u.Scheme == plain && (!allowLocal || !loopback(u.Hostname())) {
		return errors.New("insecure nonlocal dependency")
	}
	return nil
}

// ValidateDatabaseURL enforces the closed Evia contract for PostgreSQL
// connection URIs. Single-host URIs keep their existing shape, including
// sslmode=disable for local development; the only accepted extension is a
// comma-separated host list together with target_session_attrs=read-write,
// which lets pgx dial each host in order and reconnect to a promoted primary
// without an application restart. Unknown query parameters fail closed.
func ValidateDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return errors.New("EVIA_DATABASE_URL must be a postgres:// or postgresql:// connection URI")
	}
	if u.Fragment != "" || u.RawPath != "" {
		return errors.New("EVIA_DATABASE_URL must not contain a fragment or an escaped path")
	}
	if u.Host == "" {
		return errors.New("EVIA_DATABASE_URL requires at least one host")
	}
	entries := strings.Split(u.Host, ",")
	if len(entries) > maxDatabaseHosts {
		return errors.New("EVIA_DATABASE_URL supports at most 8 hosts")
	}
	for _, entry := range entries {
		host, port, splitErr := net.SplitHostPort(entry)
		if splitErr != nil {
			// A bare entry is allowed only without a colon, so unbracketed
			// IPv6 literals cannot slip through; pgx then defaults to 5432.
			if strings.Contains(entry, ":") {
				return errors.New("EVIA_DATABASE_URL host must use host:port or a bracketed IPv6 literal")
			}
			host, port = entry, ""
		}
		if host == "" || len(host) > 253 || strings.ContainsAny(host, " /?#@\r\n\t") {
			return errors.New("EVIA_DATABASE_URL contains an invalid host")
		}
		if port != "" && !validPort(port) {
			return errors.New("EVIA_DATABASE_URL contains an invalid TCP port")
		}
	}
	if u.Path != "" && u.Path != "/" {
		database := strings.TrimPrefix(u.Path, "/")
		if database == "" || strings.Contains(database, "/") {
			return errors.New("EVIA_DATABASE_URL database name must be a single path segment")
		}
	}
	if u.RawQuery != "" {
		seen := make(map[string]string)
		for _, pair := range strings.Split(u.RawQuery, "&") {
			key, value, ok := strings.Cut(pair, "=")
			if !ok || key == "" {
				return errors.New("EVIA_DATABASE_URL contains an invalid query parameter")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("EVIA_DATABASE_URL query parameters must not repeat")
			}
			decoded, decodeErr := url.QueryUnescape(value)
			if decodeErr != nil {
				return errors.New("EVIA_DATABASE_URL contains an invalid query parameter")
			}
			switch key {
			case "sslmode":
				if _, allowed := databaseSSLModes[decoded]; !allowed {
					return errors.New("EVIA_DATABASE_URL sslmode is outside the supported PostgreSQL TLS modes")
				}
			case "target_session_attrs":
				// Evia writes on every connection, so read-only and standby
				// sessions cannot satisfy any runtime role.
				if decoded != "read-write" {
					return errors.New("EVIA_DATABASE_URL target_session_attrs must be read-write")
				}
			default:
				return errors.New("EVIA_DATABASE_URL contains an unsupported query parameter")
			}
			seen[key] = decoded
		}
	}
	return nil
}

func validTopic(s string) bool { return topicPattern.MatchString(s) && s != "." && s != ".." }
func validPort(s string) bool  { n, err := strconv.Atoi(s); return err == nil && n >= 1 && n <= 65535 }
func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
func boolean(get func(string) string, name string, fallback bool) (bool, error) {
	value := get(name)
	if value == "" {
		return fallback, nil
	}
	if value != "true" && value != "false" {
		return false, errors.New(name + " must be true or false")
	}
	return value == "true", nil
}
func integer(get func(string) string, name string, fallback, min, max int) (int, error) {
	value := get(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		return 0, errors.New(name + " is outside its supported integer bounds")
	}
	return n, nil
}
func number(get func(string) string, name string, fallback, min, max float64) (float64, error) {
	value := get(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < min || n > max {
		return 0, errors.New(name + " is outside its supported finite numeric bounds")
	}
	return n, nil
}
