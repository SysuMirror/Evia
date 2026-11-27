//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func readinessConfig(t *testing.T) ClickHouseConfig {
	t.Helper()
	endpoint := os.Getenv("EVIA_TEST_CLICKHOUSE_URL")
	if endpoint == "" {
		t.Fatal("EVIA_TEST_CLICKHOUSE_URL is required for readiness reader integration tests")
	}
	suffix := strings.ReplaceAll(ids.New(), "-", "")
	cfg := ClickHouseConfig{
		URL:      endpoint,
		Database: "evia_readiness_" + suffix,
		Username: os.Getenv("EVIA_TEST_CLICKHOUSE_USERNAME"),
		Password: os.Getenv("EVIA_TEST_CLICKHOUSE_PASSWORD"),
		Client:   &http.Client{Timeout: 15 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := clickhouseQuery(ctx, cfg, "CREATE DATABASE "+cfg.Database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := clickhouseQuery(cleanup, cfg, "DROP DATABASE "+cfg.Database); err != nil {
			t.Error(err)
		}
	})
	names, err := filepath.Glob("../../../deploy/clickhouse/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		applyFactMigration(t, ctx, cfg, name)
	}
	return cfg
}

func insertReadinessRows(t *testing.T, ctx context.Context, cfg ClickHouseConfig, rows []string) {
	t.Helper()
	endpoint, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	values := endpoint.Query()
	values.Set("database", cfg.Database)
	values.Set("query", "INSERT INTO pipeline_readiness FORMAT JSONEachRow")
	values.Set("wait_end_of_query", "1")
	values.Set("date_time_input_format", "best_effort")
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader([]byte(strings.Join(rows, "\n"))))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "" {
		request.SetBasicAuth(cfg.Username, cfg.Password)
	}
	response, err := cfg.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		buf := make([]byte, 2048)
		n, _ := response.Body.Read(buf)
		t.Fatalf("readiness insert failed: %s %s", response.Status, string(buf[:n]))
	}
}

func readinessRow(project, environment, release, observed string) string {
	return `{"project_id":"` + project + `","environment":"` + environment + `","release_id":"` + release +
		`","revision":1,"manifest_hash":"` + strings.Repeat("a", 64) + `","nonce":"` + ids.New() +
		`","issued_at":"` + observed + `","expires_at":"2126-01-01 00:00:00.000000000","capability":"ready","observed_at":"` + observed + `"}`
}

func TestReadinessReaderGroupsAndScopesProofs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := readinessConfig(t)
	reader, err := NewReadinessReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	projectA := "61111111-1111-4111-8111-111111111111"
	projectB := "62222222-2222-4222-8222-222222222222"
	releaseA := "71111111-1111-4111-8111-111111111111"
	releaseB := "72222222-2222-4222-8222-222222222222"
	// pipeline_readiness expires rows 24 hours after observed_at, so anchor
	// fixtures relative to now rather than a fixed calendar date.
	base := time.Now().UTC().Truncate(time.Second).Add(-4 * time.Hour)
	stamp := func(offset time.Duration) string {
		return base.Add(offset).Format("2006-01-02 15:04:05.000000000")
	}
	insertReadinessRows(t, ctx, cfg, []string{
		readinessRow(projectA, "development", releaseA, stamp(0)),
		readinessRow(projectA, "development", releaseA, stamp(time.Hour)),
		readinessRow(projectA, "production", releaseB, stamp(2*time.Hour)),
		readinessRow(projectB, "staging", releaseA, stamp(3*time.Hour)),
	})

	proofs, err := reader.LatestProofs(ctx, projectA, MaxReadinessProofs)
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 2 {
		t.Fatalf("expected two grouped proofs for project A, got %+v", proofs)
	}
	if proofs[0].Environment != "production" || proofs[0].ProofCount != 1 || !proofs[0].LastObserved.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("unexpected newest proof: %+v", proofs[0])
	}
	if proofs[1].Environment != "development" || proofs[1].ProofCount != 2 || !proofs[1].LastObserved.Equal(base.Add(time.Hour)) {
		t.Fatalf("development proofs must aggregate to the latest observation: %+v", proofs[1])
	}
	scoped, err := reader.LatestProofs(ctx, projectB, 50)
	if err != nil || len(scoped) != 1 || scoped[0].Environment != "staging" {
		t.Fatalf("project scope isolation failed: %+v %v", scoped, err)
	}
	empty, err := reader.LatestProofs(ctx, "63333333-3333-4333-8333-333333333333", 1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown project must return an empty slice: %+v %v", empty, err)
	}
	if _, err := reader.LatestProofs(ctx, "not-a-uuid", 10); err == nil {
		t.Fatal("invalid project id must be rejected")
	}
	if _, err := reader.LatestProofs(ctx, projectA, 0); err != nil {
		t.Fatalf("out-of-range limit must clamp, got %v", err)
	}
}
