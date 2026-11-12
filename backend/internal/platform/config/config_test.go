package config

import (
	"strings"
	"testing"
	"time"
)

func TestSecureConfiguration(t *testing.T) {
	values := map[string]string{"EVIA_DATABASE_URL": "postgres://example", "EVIA_PUBLIC_ORIGIN": "https://evia.example", "EVIA_CURSOR_KEY": strings.Repeat("ab", 32)}
	get := func(k string) string { return values[k] }
	if c, e := Read(get); e != nil || !c.SecureCookies {
		t.Fatal(c, e)
	}
	values["EVIA_PUBLIC_ORIGIN"] = "http://localhost:5173"
	if _, e := Read(get); e == nil {
		t.Fatal("insecure default")
	}
	values["EVIA_ALLOW_INSECURE_HTTP"] = "true"
	if c, e := Read(get); e != nil || c.SecureCookies {
		t.Fatal(c, e)
	}
	values["EVIA_PUBLIC_ORIGIN"] = "https://user:password@example.test"
	if _, e := Read(get); e == nil {
		t.Fatal("credential origin accepted")
	}
}

func runtimeValues() map[string]string {
	return map[string]string{
		"EVIA_DATABASE_URL":            "postgres://example",
		"EVIA_PUBLIC_ORIGIN":           "https://evia.example",
		"EVIA_CURSOR_KEY":              strings.Repeat("ab", 32),
		"EVIA_COLLECTION_ENABLED":      "true",
		"EVIA_ANALYSIS_ENABLED":        "true",
		"EVIA_REDIS_URL":               "rediss://redis.example:6379/0",
		"EVIA_KAFKA_BROKERS":           "kafka.example:9093",
		"EVIA_KAFKA_TOPIC":             "evia.events",
		"EVIA_KAFKA_DEAD_LETTER_TOPIC": "evia.quarantine",
		"EVIA_KAFKA_GROUP":             "evia-materialize",
		"EVIA_KAFKA_PROBE_TOPIC":       "evia.readiness",
		"EVIA_CLICKHOUSE_URL":          "https://clickhouse.example:8443",
		"EVIA_CLICKHOUSE_DATABASE":     "evia",
	}
}

func TestFeatureOptInAndWorkerIndependence(t *testing.T) {
	values := runtimeValues()
	delete(values, "EVIA_COLLECTION_ENABLED")
	delete(values, "EVIA_ANALYSIS_ENABLED")
	values["EVIA_REDIS_URL"] = "ignored-when-disabled"
	if c, err := Read(func(k string) string { return values[k] }); err != nil || c.CollectionEnabled || c.AnalysisEnabled || len(c.SourceSecretKey) != 0 {
		t.Fatal("control-only baseline depends on optional adapters", err)
	}
	values["EVIA_SOURCE_SECRET_KEY"] = strings.Repeat("cd", 32)
	if c, err := Read(func(k string) string { return values[k] }); err != nil || len(c.SourceSecretKey) != 32 {
		t.Fatal("source CRUD cannot be enabled independently", err)
	}
	for _, command := range []string{"ingest-worker", "query-worker"} {
		values := runtimeValues()
		delete(values, "EVIA_PUBLIC_ORIGIN")
		delete(values, "EVIA_CURSOR_KEY")
		if command == "query-worker" {
			delete(values, "EVIA_KAFKA_BROKERS")
			delete(values, "EVIA_REDIS_URL")
		}
		c, err := ReadFor(command, func(k string) string { return values[k] })
		if err != nil || len(c.CursorKey) != 0 || len(c.SourceSecretKey) != 0 || c.QueryWorkers != 2 {
			t.Fatalf("worker %s requires unrelated browser configuration: %v", command, err)
		}
	}
}

func TestErasureWorkerOnlyNeedsPostgresAndClickHouse(t *testing.T) {
	values := runtimeValues()
	// Browser-only and streaming settings must not gate the erasure process.
	delete(values, "EVIA_PUBLIC_ORIGIN")
	delete(values, "EVIA_CURSOR_KEY")
	delete(values, "EVIA_KAFKA_BROKERS")
	delete(values, "EVIA_REDIS_URL")
	delete(values, "EVIA_COLLECTION_ENABLED")
	delete(values, "EVIA_ANALYSIS_ENABLED")
	c, err := ReadFor("erasure-worker", func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("erasure worker rejected a minimal configuration: %v", err)
	}
	if c.CollectionEnabled || c.AnalysisEnabled || len(c.CursorKey) != 0 || len(c.SourceSecretKey) != 0 {
		t.Fatal("erasure worker pulled in unrelated runtime configuration")
	}
	if c.ClickHouse.URL == "" || c.ClickHouse.Database == "" {
		t.Fatal("erasure worker lost its ClickHouse executor configuration")
	}
}

func TestRetentionWorkerDefaultsToSafeDryRun(t *testing.T) {
	values := runtimeValues()
	delete(values, "EVIA_PUBLIC_ORIGIN")
	delete(values, "EVIA_CURSOR_KEY")
	delete(values, "EVIA_KAFKA_BROKERS")
	delete(values, "EVIA_REDIS_URL")
	delete(values, "EVIA_COLLECTION_ENABLED")
	delete(values, "EVIA_ANALYSIS_ENABLED")

	// Default: no enforcement flag means dry-run and a bounded interval.
	c, err := ReadFor("retention-worker", func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("retention worker rejected minimal config: %v", err)
	}
	if c.RetentionEnforcement {
		t.Fatal("retention worker must default to non-destructive dry-run")
	}
	if c.RetentionSweepInterval != 6*time.Hour || c.ClickHouse.URL == "" {
		t.Fatalf("retention worker defaults wrong: interval=%s ch=%q", c.RetentionSweepInterval, c.ClickHouse.URL)
	}

	// Explicit opt-in enables destructive enforcement.
	values["EVIA_RETENTION_ENFORCEMENT_ENABLED"] = "true"
	values["EVIA_RETENTION_SWEEP_INTERVAL"] = "2h"
	enforced, err := ReadFor("retention-worker", func(k string) string { return values[k] })
	if err != nil || !enforced.RetentionEnforcement || enforced.RetentionSweepInterval != 2*time.Hour {
		t.Fatalf("enforcement opt-in not honored: %+v err=%v", enforced, err)
	}

	// Non-boolean flags and out-of-range intervals are rejected.
	values["EVIA_RETENTION_ENFORCEMENT_ENABLED"] = "yes"
	if _, err := ReadFor("retention-worker", func(k string) string { return values[k] }); err == nil {
		t.Fatal("non-boolean enforcement flag accepted")
	}
	values["EVIA_RETENTION_ENFORCEMENT_ENABLED"] = "true"
	values["EVIA_RETENTION_SWEEP_INTERVAL"] = "500ms"
	if _, err := ReadFor("retention-worker", func(k string) string { return values[k] }); err == nil {
		t.Fatal("sub-minute sweep interval accepted")
	}
	delete(values, "EVIA_RETENTION_SWEEP_INTERVAL")

	// Archive opt-in requires an artifact directory.
	values["EVIA_ARCHIVE_ENABLED"] = "true"
	if _, err := ReadFor("retention-worker", func(k string) string { return values[k] }); err == nil {
		t.Fatal("archive enabled without directory accepted")
	}
	values["EVIA_ARCHIVE_DIR"] = "/var/lib/evia/archives"
	archived, err := ReadFor("retention-worker", func(k string) string { return values[k] })
	if err != nil || !archived.ArchiveEnabled || archived.ArchiveDir != "/var/lib/evia/archives" {
		t.Fatalf("archive opt-in not honored: %+v err=%v", archived, err)
	}
	values["EVIA_ARCHIVE_ENABLED"] = "maybe"
	if _, err := ReadFor("retention-worker", func(k string) string { return values[k] }); err == nil {
		t.Fatal("non-boolean archive flag accepted")
	}
}

func TestBackfillWorkerRequiresArchiveDirectory(t *testing.T) {
	values := runtimeValues()
	if _, err := ReadFor("backfill-worker", func(k string) string { return values[k] }); err == nil {
		t.Fatal("backfill worker without archive directory accepted")
	}
	values["EVIA_ARCHIVE_DIR"] = "/var/lib/evia/archives"
	c, err := ReadFor("backfill-worker", func(k string) string { return values[k] })
	if err != nil || c.ArchiveDir != "/var/lib/evia/archives" || c.ClickHouse.URL == "" {
		t.Fatalf("backfill worker config wrong: %+v err=%v", c, err)
	}
}

func TestIngestWorkerLagInterval(t *testing.T) {
	values := runtimeValues()
	c, err := ReadFor("ingest-worker", func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("ingest worker rejected baseline config: %v", err)
	}
	if c.IngestLagInterval != 30*time.Second {
		t.Fatalf("default lag interval = %s, want 30s", c.IngestLagInterval)
	}

	values["EVIA_INGEST_LAG_INTERVAL"] = "2m"
	custom, err := ReadFor("ingest-worker", func(k string) string { return values[k] })
	if err != nil || custom.IngestLagInterval != 2*time.Minute {
		t.Fatalf("custom lag interval not honored: %s err=%v", custom.IngestLagInterval, err)
	}

	// Zero explicitly disables the lag monitor.
	values["EVIA_INGEST_LAG_INTERVAL"] = "0s"
	disabled, err := ReadFor("ingest-worker", func(k string) string { return values[k] })
	if err != nil || disabled.IngestLagInterval != 0 {
		t.Fatalf("zero lag interval should disable sampling: %s err=%v", disabled.IngestLagInterval, err)
	}

	for _, raw := range []string{"1s", "11m", "soon"} {
		values["EVIA_INGEST_LAG_INTERVAL"] = raw
		if _, err := ReadFor("ingest-worker", func(k string) string { return values[k] }); err == nil {
			t.Fatalf("lag interval %q must be rejected", raw)
		}
	}

	// The setting is ingest-worker specific; other commands keep the zero value.
	serve, err := ReadFor("serve", func(k string) string { return runtimeValues()[k] })
	if err != nil || serve.IngestLagInterval != 0 {
		t.Fatalf("serve must not inherit ingest lag interval: %s err=%v", serve.IngestLagInterval, err)
	}
}

func TestExportWorkerRequiresArtifactDirectory(t *testing.T) {
	values := runtimeValues()
	values["EVIA_EXPORT_DIR"] = "/var/lib/evia/exports"
	c, err := ReadFor("export-worker", func(k string) string { return values[k] })
	if err != nil {
		t.Fatalf("export worker rejected valid config: %v", err)
	}
	if c.ExportDir != "/var/lib/evia/exports" {
		t.Fatalf("export dir = %q", c.ExportDir)
	}
	delete(values, "EVIA_EXPORT_DIR")
	if _, err := ReadFor("export-worker", func(k string) string { return values[k] }); err == nil {
		t.Fatal("export worker without EVIA_EXPORT_DIR must be rejected")
	}
	// Other commands must not inherit the export directory requirement.
	serve, err := ReadFor("serve", func(k string) string { return runtimeValues()[k] })
	if err != nil || serve.ExportDir != "" {
		t.Fatalf("serve must not require export dir: %q err=%v", serve.ExportDir, err)
	}
}

func TestRuntimeConfigurationRejectsUnsafeSettings(t *testing.T) {
	tests := []struct{ name, command, key, value string }{
		{"collection flag", "serve", "EVIA_COLLECTION_ENABLED", "yes"},
		{"analysis flag", "serve", "EVIA_ANALYSIS_ENABLED", "1"},
		{"dependency flag", "serve", "EVIA_ALLOW_INSECURE_DEPENDENCIES", "enabled"},
		{"invalid source key", "serve", "EVIA_SOURCE_SECRET_KEY", "not-a-secret-key"},
		{"short source key", "serve", "EVIA_SOURCE_SECRET_KEY", "ab"},
		{"listen port", "serve", "EVIA_LISTEN", "127.0.0.1:65536"},
		{"metrics listen malformed", "retention-worker", "EVIA_METRICS_LISTEN", "127.0.0.1"},
		{"metrics listen bad port", "retention-worker", "EVIA_METRICS_LISTEN", "127.0.0.1:70000"},
		{"origin port", "serve", "EVIA_PUBLIC_ORIGIN", "https://evia.example:65536"},
		{"redis plaintext default", "serve", "EVIA_REDIS_URL", "redis://127.0.0.1:6379"},
		{"redis query override", "serve", "EVIA_REDIS_URL", "rediss://redis.example?read_timeout=-1"},
		{"redis fragment", "serve", "EVIA_REDIS_URL", "rediss://redis.example#secret"},
		{"zero collection rate", "serve", "EVIA_COLLECTION_RATE", "0"},
		{"NaN collection rate", "serve", "EVIA_COLLECTION_RATE", "NaN"},
		{"infinite collection rate", "serve", "EVIA_COLLECTION_RATE", "+Inf"},
		{"small burst", "serve", "EVIA_COLLECTION_BURST", "49"},
		{"unbounded redis pool", "serve", "EVIA_REDIS_POOL_SIZE", "100000"},
		{"unbounded redis timeout", "serve", "EVIA_REDIS_TIMEOUT", "1h"},
		{"Kafka broker missing", "serve", "EVIA_KAFKA_BROKERS", ""},
		{"Kafka broker no port", "serve", "EVIA_KAFKA_BROKERS", "kafka.example"},
		{"Kafka broker invalid port", "serve", "EVIA_KAFKA_BROKERS", "kafka.example:0"},
		{"Kafka broker credentials", "serve", "EVIA_KAFKA_BROKERS", "user:password@kafka.example:9093"},
		{"Kafka empty broker", "serve", "EVIA_KAFKA_BROKERS", "kafka.example:9093,"},
		{"Kafka broker bound", "serve", "EVIA_KAFKA_BROKERS", strings.Repeat("kafka.example:9093,", 16) + "last.example:9093"},
		{"Kafka TLS flag", "serve", "EVIA_KAFKA_TLS_ENABLED", "no"},
		{"Kafka plaintext remote", "serve", "EVIA_KAFKA_TLS_ENABLED", "false"},
		{"Kafka topic missing", "serve", "EVIA_KAFKA_TOPIC", ""},
		{"Kafka topic wildcard", "serve", "EVIA_KAFKA_TOPIC", "evia.*"},
		{"Kafka topic dot", "serve", "EVIA_KAFKA_TOPIC", "."},
		{"Kafka same DLQ", "serve", "EVIA_KAFKA_DEAD_LETTER_TOPIC", "evia.events"},
		{"Kafka same probe", "serve", "EVIA_KAFKA_PROBE_TOPIC", "evia.events"},
		{"Kafka partial auth", "serve", "EVIA_KAFKA_USERNAME", "synthetic"},
		{"worker needs group", "ingest-worker", "EVIA_KAFKA_GROUP", ""},
		{"collection worker opt in", "ingest-worker", "EVIA_COLLECTION_ENABLED", "false"},
		{"query worker opt in", "query-worker", "EVIA_ANALYSIS_ENABLED", "false"},
		{"query workers zero", "query-worker", "EVIA_QUERY_WORKERS", "0"},
		{"query workers unbounded", "query-worker", "EVIA_QUERY_WORKERS", "17"},
		{"instance ID unbounded", "query-worker", "EVIA_INSTANCE_ID", strings.Repeat("x", 65)},
		{"ClickHouse plaintext default", "serve", "EVIA_CLICKHOUSE_URL", "http://127.0.0.1:8123"},
		{"ClickHouse URL auth", "serve", "EVIA_CLICKHOUSE_URL", "https://user:password@clickhouse.example"},
		{"ClickHouse URL query", "serve", "EVIA_CLICKHOUSE_URL", "https://clickhouse.example?query=unsafe"},
		{"ClickHouse URL path", "serve", "EVIA_CLICKHOUSE_URL", "https://clickhouse.example/unsafe"},
		{"ClickHouse database", "serve", "EVIA_CLICKHOUSE_DATABASE", "evia;DROP TABLE"},
		{"ClickHouse partial auth", "serve", "EVIA_CLICKHOUSE_USERNAME", "synthetic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := runtimeValues()
			values[tt.key] = tt.value
			if _, err := ReadFor(tt.command, func(k string) string { return values[k] }); err == nil {
				t.Fatal("unsafe runtime setting accepted")
			}
		})
	}
}

func TestMetricsListenIsOptionalAndValidated(t *testing.T) {
	// Unset means disabled, so existing worker deployments keep no HTTP listener.
	values := runtimeValues()
	if c, err := ReadFor("retention-worker", func(k string) string { return values[k] }); err != nil || c.MetricsListen != "" {
		t.Fatalf("default metrics listener should be disabled: %+v err=%v", c, err)
	}
	// A well-formed loopback address is accepted for every runtime command.
	values["EVIA_METRICS_LISTEN"] = "127.0.0.1:19090"
	for _, command := range []string{"serve", "ingest-worker", "query-worker", "erasure-worker", "retention-worker"} {
		c, err := ReadFor(command, func(k string) string { return values[k] })
		if err != nil || c.MetricsListen != "127.0.0.1:19090" {
			t.Fatalf("metrics listener not accepted for %s: %+v err=%v", command, c, err)
		}
	}
}

func TestPlaintextDependenciesRequireOnlyLoopback(t *testing.T) {
	values := runtimeValues()
	values["EVIA_ALLOW_INSECURE_DEPENDENCIES"] = "true"
	values["EVIA_KAFKA_TLS_ENABLED"] = "false"
	values["EVIA_KAFKA_BROKERS"] = "127.0.0.1:19092, [::1]:19092"
	values["EVIA_REDIS_URL"] = "redis://localhost:16379/0"
	values["EVIA_CLICKHOUSE_URL"] = "http://127.0.0.1:18123"
	get := func(k string) string { return values[k] }
	if c, err := Read(get); err != nil || c.Kafka.TLSEnabled || !c.Budget.AllowInsecureLocal {
		t.Fatal("explicit loopback configuration rejected", err)
	}
	for key, value := range map[string]string{"EVIA_REDIS_URL": "redis://10.0.0.1:6379", "EVIA_CLICKHOUSE_URL": "http://10.0.0.1:8123", "EVIA_KAFKA_BROKERS": "10.0.0.1:9092"} {
		old := values[key]
		values[key] = value
		if _, err := Read(get); err == nil {
			t.Fatalf("plaintext nonlocal dependency accepted: %s", key)
		}
		values[key] = old
	}
}

func TestConfigurationErrorsDoNotEchoSecrets(t *testing.T) {
	values := runtimeValues()
	values["EVIA_CLICKHOUSE_URL"] = "https://user:synthetic-sensitive-value@clickhouse.example"
	_, err := Read(func(k string) string { return values[k] })
	if err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") {
		t.Fatal("configuration error exposed a credential")
	}
}

func TestDeliveryConfigurationRequiresActualReadbackDependency(t *testing.T) {
	values := runtimeValues()
	values["EVIA_ANALYSIS_ENABLED"] = "false"
	get := func(key string) string { return values[key] }
	if c, err := Read(get); err != nil || !c.DeliveryEnabled || c.AnalysisEnabled {
		t.Fatal("delivery cannot be enabled independently of query HTTP", err)
	}
	delete(values, "EVIA_CLICKHOUSE_URL")
	if _, err := Read(get); err == nil {
		t.Fatal("delivery omitted its actual ClickHouse readback dependency")
	}
	delete(values, "EVIA_KAFKA_PROBE_TOPIC")
	if c, err := Read(get); err != nil || c.DeliveryEnabled {
		t.Fatal("collection-only runtime unexpectedly requires delivery storage", err)
	}
	values["EVIA_COLLECTION_ENABLED"] = "false"
	values["EVIA_KAFKA_PROBE_TOPIC"] = "ignored-while-collection-disabled"
	if c, err := Read(get); err != nil || c.DeliveryEnabled {
		t.Fatal("a topic name alone enabled release delivery", err)
	}
}

func TestShutdownDelayBounds(t *testing.T) {
	values := runtimeValues()
	// Default applies when the variable is unset.
	c, err := ReadFor("serve", func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.ShutdownDelay != 5*time.Second {
		t.Fatalf("default shutdown delay = %v, want 5s", c.ShutdownDelay)
	}

	values["EVIA_SHUTDOWN_DELAY"] = "0s"
	if c, err = ReadFor("serve", func(k string) string { return values[k] }); err != nil || c.ShutdownDelay != 0 {
		t.Fatal("zero delay should be accepted for immediate shutdown", c.ShutdownDelay, err)
	}

	values["EVIA_SHUTDOWN_DELAY"] = "30s"
	if c, err = ReadFor("serve", func(k string) string { return values[k] }); err != nil || c.ShutdownDelay != 30*time.Second {
		t.Fatal("upper bound delay rejected", c.ShutdownDelay, err)
	}

	for _, raw := range []string{"31s", "-1s", "not-a-duration"} {
		values["EVIA_SHUTDOWN_DELAY"] = raw
		if _, err = ReadFor("serve", func(k string) string { return values[k] }); err == nil {
			t.Fatalf("invalid EVIA_SHUTDOWN_DELAY %q accepted", raw)
		}
	}

	// Non-serve commands ignore the HTTP-only setting.
	delete(values, "EVIA_SHUTDOWN_DELAY")
	if c, err = ReadFor("ingest-worker", func(k string) string { return values[k] }); err != nil || c.ShutdownDelay != 0 {
		t.Fatal("worker must not inherit the serve shutdown delay", c.ShutdownDelay, err)
	}
}
