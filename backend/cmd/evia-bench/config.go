package main

import (
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
)

// Config is read from a private file. It must never be serialized into evidence.
type Config struct {
	ContractVersion int       `json:"contract_version"`
	APIURL          string    `json:"api_url"`
	ProjectID       string    `json:"project_id"`
	SourceID        string    `json:"source_id,omitempty"`
	Environment     string    `json:"environment"`
	RunID           string    `json:"run_id"`
	SourceKind      string    `json:"source_kind"`
	WriteKey        string    `json:"write_key"`
	ServerSecret    string    `json:"server_secret,omitempty"`
	Origin          string    `json:"origin,omitempty"`
	EventKey        string    `json:"event_key"`
	SchemaVersion   int64     `json:"schema_version"`
	ReleaseRevision int64     `json:"release_revision"`
	StartedAt       time.Time `json:"started_at"`
	UIDCardinality  int       `json:"uid_cardinality"`
}

type Options struct {
	Events, BatchSize, Concurrency int
	Rate                           float64
	ReplayPercent                  int
	Out                            string
}

var uuidPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func readConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("cannot open private configuration")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 16384 {
		return c, errors.New("configuration must be a regular mode-0600 file below 16KiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || !decodeStrict(raw, &c) {
		return c, errors.New("invalid configuration JSON")
	}
	if c.UIDCardinality == 0 {
		c.UIDCardinality = 100000
	}
	return c, c.validate()
}

func (c Config) validate() error {
	u, err := url.Parse(c.APIURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("api_url must be a loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	// Literal loopback addresses avoid DNS rebinding and proxy routing.
	if ip == nil || !ip.IsLoopback() {
		return errors.New("api_url requires a literal loopback address")
	}
	if c.ContractVersion != 1 || !uuidPattern.MatchString(c.RunID) || !uuidPattern.MatchString(c.ProjectID) || (c.SourceID != "" && !uuidPattern.MatchString(c.SourceID)) || c.Environment != env.Development || !keyPattern.MatchString(c.EventKey) {
		return errors.New("invalid benchmark scope or contract version")
	}
	if c.SchemaVersion < 1 || c.SchemaVersion > canonical.MaxInteger || c.ReleaseRevision < 1 || c.ReleaseRevision > canonical.MaxInteger || c.UIDCardinality < 1 || c.UIDCardinality > 10000000 {
		return errors.New("configuration numeric bounds exceeded")
	}
	if c.WriteKey == "" || len(c.WriteKey) > 128 || c.StartedAt.IsZero() || c.StartedAt.Location() != time.UTC {
		return errors.New("configuration requires write_key and UTC started_at")
	}
	if c.SourceKind == ingestion.SourceKindServer && c.ServerSecret != "" && len(c.ServerSecret) <= 4096 && !strings.ContainsAny(c.ServerSecret, "\r\n") {
		return nil
	}
	if c.SourceKind == ingestion.SourceKindBrowser && c.ServerSecret == "" {
		origin, e := url.Parse(c.Origin)
		if e == nil && (origin.Scheme == "https" || origin.Scheme == "http") && origin.Host != "" && origin.User == nil && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" {
			return nil
		}
	}
	return errors.New("invalid source authentication configuration")
}

func (o Options) validate() error {
	if o.Events < 1 || o.Events > 10000000 || o.BatchSize < 1 || o.BatchSize > 50 || o.Concurrency < 1 || o.Concurrency > 128 || o.ReplayPercent < 0 || o.ReplayPercent > 100 || math.IsNaN(o.Rate) || math.IsInf(o.Rate, 0) || o.Rate < 0 || o.Rate > 1000000 || o.Out == "" {
		return errors.New("require events 1..10000000, batch-size 1..50, concurrency 1..128, replay-percent 0..100, rate 0..1000000 and out")
	}
	return nil
}
