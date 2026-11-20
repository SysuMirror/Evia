package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

// PropertySlotExecutor applies one reviewed hot-key catalog to the raw fact
// table: the additive slot columns and winner-tuple view recreation, then a
// bounded backfill that materializes token columns for already-stored rows.
// Both steps are idempotent; the authoritative properties_json document is
// never rewritten and old parts keep their structural defaults.
type PropertySlotExecutor struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
	catalog  *propertycatalog.Catalog
}

// NewPropertySlotExecutor validates the target like the fact sink and reuses
// the same credential profile. A disabled catalog is rejected: applying an
// empty revision is an operator error, not a no-op.
func NewPropertySlotExecutor(c ClickHouseConfig, client *http.Client, catalog *propertycatalog.Catalog) (*PropertySlotExecutor, error) {
	if catalog == nil || !catalog.Enabled() {
		return nil, errors.New("property catalog is disabled or missing")
	}
	endpoint, e := url.Parse(c.URL)
	if e != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
		return nil, errors.New("invalid clickhouse endpoint or database")
	}
	if !databasePattern.MatchString(c.Database) {
		return nil, errors.New("invalid clickhouse database configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &PropertySlotExecutor{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client, catalog: catalog}, nil
}

func (x *PropertySlotExecutor) request(ctx context.Context, query string) ([]byte, error) {
	endpoint := *x.endpoint
	values := endpoint.Query()
	values.Set("database", x.database)
	values.Set("wait_end_of_query", "1")
	endpoint.RawQuery = values.Encode()
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(query))
	if e != nil {
		return nil, e
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if x.username != "" || x.password != "" {
		request.SetBasicAuth(x.username, x.password)
	}
	response, e := x.client.Do(request)
	if e != nil {
		return nil, ErrSinkUnavailable
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(body) > 65536 {
		return nil, ErrSinkUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrSinkUnavailable, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// ApplyMigration executes the catalog's idempotent operator migration: slot
// columns first, then the winner-tuple view recreation exposing them.
func (x *PropertySlotExecutor) ApplyMigration(ctx context.Context) error {
	// Comment banners may contain semicolons, so strip whole comment lines
	// before splitting; the remaining SQL never spans a semicolon.
	var stripped []string
	for _, line := range strings.Split(x.catalog.MigrationSQL(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		stripped = append(stripped, line)
	}
	for _, statement := range strings.Split(strings.Join(stripped, "\n"), ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, e := x.request(ctx, statement); e != nil {
			return fmt.Errorf("property slot migration statement failed: %w", e)
		}
	}
	return nil
}

// SlotBackfillReport counts rows per event identity after the backfill.
type SlotBackfillReport struct {
	EventKey      string
	SchemaVersion uint64
	Materialized  uint64
}

// Backfill rewrites stored rows of the catalog's event identities so their
// slot columns carry the exact lexical tokens from property_tokens and the
// row marker names the catalog revision. Rows without a derived token keep
// the empty slot, which the reader treats as "not materialized" and serves
// from the authoritative document. Mutations run synchronously; the returned
// counts are read back from the table after every mutation completes.
func (x *PropertySlotExecutor) Backfill(ctx context.Context) ([]SlotBackfillReport, error) {
	columns := x.catalog.Columns()
	if len(columns) == 0 {
		return nil, errors.New("property catalog has no slot columns")
	}
	identities := x.catalog.Identities()
	assignments := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		if !slotColumnPattern.MatchString(column) {
			return nil, fmt.Errorf("unsafe dedicated slot column name %q", column)
		}
		assignments = append(assignments, fmt.Sprintf(
			"%s=if(mapContains(property_tokens,'%s'),property_tokens['%s'],'')", column, column, column))
	}
	assignments = append(assignments, fmt.Sprintf("dedicated_layout_version=%d", x.catalog.Revision))
	var reports []SlotBackfillReport
	for _, identity := range identities {
		query := fmt.Sprintf(
			"ALTER TABLE `%s`.`event_facts_raw` UPDATE %s WHERE event_key={event:String} AND schema_version={version:UInt64} AND dedicated_layout_version=0 SETTINGS mutations_sync=1",
			x.database, strings.Join(assignments, ", "))
		if _, e := x.requestWithParams(ctx, query, map[string]string{
			"event":   identity.EventKey,
			"version": fmt.Sprintf("%d", identity.SchemaVersion),
		}); e != nil {
			return nil, fmt.Errorf("property slot backfill for %s v%d failed: %w", identity.EventKey, identity.SchemaVersion, e)
		}
		countQuery := fmt.Sprintf(
			"SELECT count() FROM `%s`.`event_facts_raw` WHERE event_key={event:String} AND schema_version={version:UInt64} AND dedicated_layout_version={marker:UInt8}",
			x.database)
		body, e := x.requestWithParams(ctx, countQuery, map[string]string{
			"event":   identity.EventKey,
			"version": fmt.Sprintf("%d", identity.SchemaVersion),
			"marker":  fmt.Sprintf("%d", x.catalog.Revision),
		})
		if e != nil {
			return nil, e
		}
		var materialized uint64
		if _, e = fmt.Sscanf(strings.TrimSpace(string(body)), "%d", &materialized); e != nil {
			return nil, fmt.Errorf("property slot backfill count unreadable: %w", e)
		}
		reports = append(reports, SlotBackfillReport{
			EventKey:      identity.EventKey,
			SchemaVersion: identity.SchemaVersion,
			Materialized:  materialized,
		})
	}
	return reports, nil
}

func (x *PropertySlotExecutor) requestWithParams(ctx context.Context, query string, parameters map[string]string) ([]byte, error) {
	endpoint := *x.endpoint
	values := endpoint.Query()
	values.Set("database", x.database)
	values.Set("wait_end_of_query", "1")
	for key, value := range parameters {
		values.Set("param_"+key, value)
	}
	endpoint.RawQuery = values.Encode()
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(query))
	if e != nil {
		return nil, e
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if x.username != "" || x.password != "" {
		request.SetBasicAuth(x.username, x.password)
	}
	response, e := x.client.Do(request)
	if e != nil {
		return nil, ErrSinkUnavailable
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(body) > 65536 {
		return nil, ErrSinkUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrSinkUnavailable, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
