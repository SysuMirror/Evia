// Package config parses server-owned configuration without logging credentials.
package config

import (
	"encoding/hex"
	"errors"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
)

type Kafka struct {
	Brokers                                   []string
	Topic, DeadLetterTopic, Group, ProbeTopic string
	TLSEnabled                                bool
	Username, Password                        string
}
type ClickHouse struct{ URL, Database, Username, Password string }
type Config struct {
	DatabaseURL, Listen, Origin                                                    string
	CursorKey                                                                      []byte
	SecureCookies                                                                  bool
	SourceSecretKey                                                                []byte
	CollectionEnabled, AnalysisEnabled, DeliveryEnabled, AllowInsecureDependencies bool
	Budget                                                                         budget.Config
	Kafka                                                                          Kafka
	ClickHouse                                                                     ClickHouse
	QueryWorkers                                                                   int
	InstanceID                                                                     string
}

var (
	topicPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	instancePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func Read(get func(string) string) (Config, error) { return ReadFor("serve", get) }

// ReadFor separates worker dependency settings from browser session settings.
// Administrative migration/bootstrap commands deliberately use only DatabaseURL.
func ReadFor(command string, get func(string) string) (Config, error) {
	c := Config{DatabaseURL: get("EVIA_DATABASE_URL"), SecureCookies: true, QueryWorkers: 2, InstanceID: "evia"}
	if command != "serve" && command != "ingest-worker" && command != "query-worker" {
		return c, errors.New("unknown runtime command")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("EVIA_DATABASE_URL is required")
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
	if (c.AnalysisEnabled || c.DeliveryEnabled) && command == "serve" || command == "ingest-worker" || command == "query-worker" {
		if err = c.readClickHouse(get); err != nil {
			return c, err
		}
	}
	if command == "query-worker" {
		if c.QueryWorkers, err = integer(get, "EVIA_QUERY_WORKERS", 2, 1, 16); err != nil {
			return c, err
		}
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
	return nil
}

func (c *Config) readBudget(get func(string) string) error {
	c.Budget = budget.DefaultConfig(get("EVIA_REDIS_URL"))
	c.Budget.AllowInsecureLocal = c.AllowInsecureDependencies
	if err := dependencyURL(c.Budget.URL, "redis", "rediss", true, c.AllowInsecureDependencies); err != nil {
		return errors.New("EVIA_REDIS_URL must be a secure Redis URL or explicitly allowed loopback development URL")
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
