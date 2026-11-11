package config

import (
	"strings"
	"testing"
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

func TestRuntimeConfigurationRejectsUnsafeSettings(t *testing.T) {
	tests := []struct{ name, command, key, value string }{
		{"collection flag", "serve", "EVIA_COLLECTION_ENABLED", "yes"},
		{"analysis flag", "serve", "EVIA_ANALYSIS_ENABLED", "1"},
		{"dependency flag", "serve", "EVIA_ALLOW_INSECURE_DEPENDENCIES", "enabled"},
		{"invalid source key", "serve", "EVIA_SOURCE_SECRET_KEY", "not-a-secret-key"},
		{"short source key", "serve", "EVIA_SOURCE_SECRET_KEY", "ab"},
		{"listen port", "serve", "EVIA_LISTEN", "127.0.0.1:65536"},
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
