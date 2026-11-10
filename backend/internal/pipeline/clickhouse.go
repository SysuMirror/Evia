package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

type ClickHouseConfig struct {
	URL      string
	Database string
	Username string
	Password string
	Client   *http.Client
}
type ClickHouse struct {
	endpoint                     *url.URL
	database, username, password string
	client                       *http.Client
}

var databasePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
var ErrSinkUnavailable = errors.New("event fact persistence unavailable")

func NewClickHouse(c ClickHouseConfig) (*ClickHouse, error) {
	endpoint, e := url.Parse(c.URL)
	if e != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || !databasePattern.MatchString(c.Database) {
		return nil, errors.New("invalid ClickHouse endpoint or database")
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Do not forward database credentials across HTTP redirects.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ClickHouse{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: &copyClient}, nil
}

type factRow struct {
	SourceKind         string  `json:"source_kind"`
	ProjectID          string  `json:"project_id"`
	Environment        string  `json:"environment"`
	SourceID           string  `json:"source_id"`
	EventID            string  `json:"event_id"`
	EventKey           string  `json:"event_key"`
	SchemaVersion      int64   `json:"schema_version"`
	ReleaseRevision    int64   `json:"release_revision"`
	OccurredAt         string  `json:"occurred_at"`
	EventTime          string  `json:"event_time"`
	OccurredAtPrecise  string  `json:"occurred_at_precise"`
	EventTimePrecise   string  `json:"event_time_precise"`
	ReceivedAt         string  `json:"received_at"`
	CanonicalHash      string  `json:"canonical_hash"`
	UID                *string `json:"uid"`
	AnonymousID        *string `json:"anonymous_id"`
	SessionID          *string `json:"session_id"`
	TabID              *string `json:"tab_id"`
	Sequence           *int64  `json:"sequence"`
	PageViewID         *string `json:"page_view_id"`
	SourcePageViewID   *string `json:"source_page_view_id"`
	NavigationID       *string `json:"navigation_id"`
	OperationID        *string `json:"operation_id"`
	RouteName          *string `json:"route_name"`
	RouteTemplate      *string `json:"route_template"`
	SourceModule       *string `json:"source_module"`
	NavigationType     *string `json:"navigation_type"`
	PreviousPageViewID *string `json:"previous_page_view_id"`
	SDKName            string  `json:"sdk_name"`
	SDKVersion         string  `json:"sdk_version"`
	ContextJSON        string  `json:"context_json"`
	PropertiesJSON     string  `json:"properties_json"`
	// Layout version 1 means PropertyTokens was derived losslessly from
	// PropertiesJSON and both payloads commit in this one insert. The map
	// values are the exact JSON tokens; never re-serialize them numerically.
	PropertyLayoutVersion uint8             `json:"property_layout_version"`
	PropertyTokens        map[string]string `json:"property_tokens"`
}

func present(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func rawPresent(context map[string]json.RawMessage, key string) *string {
	value, ok := context[key]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(value, &s) != nil {
		return nil
	}
	return &s
}
func rowFor(record ingestion.Record) (factRow, error) {
	r, e := normalizeRecord(record)
	if e != nil {
		return factRow{}, e
	}
	hash, e := canonicalHash(r)
	if e != nil {
		return factRow{}, e
	}
	var exact map[string]json.RawMessage
	if e = json.Unmarshal(r.ContextJSON, &exact); e != nil {
		return factRow{}, e
	}
	occurred := r.OccurredAt.Format("2006-01-02 15:04:05.000000000")
	// Derive the read layout from the normalized document. normalizeRecord
	// guarantees a JSON object, so a derivation failure means an internal
	// invariant broke: fail closed instead of persisting a row whose layout
	// would be silently missing. Tokens keep their exact lexical text.
	derived, e := propertylayout.DeriveMap(r.PropertiesJSON)
	if e != nil {
		return factRow{}, e
	}
	propertyTokens := make(map[string]string, len(derived))
	for encoded, token := range derived {
		propertyTokens[encoded] = string(token)
	}
	return factRow{
		SourceKind: r.SourceKind, ProjectID: r.ProjectID, Environment: r.Environment, SourceID: r.SourceID, EventID: r.EventID, EventKey: r.EventKey, SchemaVersion: r.SchemaVersion, ReleaseRevision: r.ReleaseRevision,
		OccurredAt: occurred, EventTime: occurred, OccurredAtPrecise: occurred, EventTimePrecise: occurred, ReceivedAt: r.ReceivedAt.Format("2006-01-02 15:04:05.000000000"), CanonicalHash: hash,
		UID: present(r.UID), AnonymousID: present(r.AnonymousID), SessionID: present(r.Context.SessionID), TabID: present(r.Context.TabID), Sequence: r.Context.Sequence,
		PageViewID: present(r.Context.PageViewID), SourcePageViewID: present(r.Context.SourcePageViewID), NavigationID: present(r.Context.NavigationID), OperationID: present(r.Context.OperationID),
		RouteName: rawPresent(exact, "route_name"), RouteTemplate: rawPresent(exact, "route_template"), SourceModule: rawPresent(exact, "source_module"), NavigationType: present(r.Context.NavigationType), PreviousPageViewID: present(r.Context.PreviousPageViewID),
		SDKName: r.SDKName, SDKVersion: r.SDKVersion, ContextJSON: string(r.ContextJSON), PropertiesJSON: string(r.PropertiesJSON),
		PropertyLayoutVersion: 1, PropertyTokens: propertyTokens,
	}, nil
}

// Write uses one synchronous INSERT into the explicitly migrated raw table. A
// successful response is the persistence boundary; HTTP/broker retries may replay
// rows, and the exact read view removes those repetitions before any aggregation.
func (c *ClickHouse) Write(ctx context.Context, records []ingestion.Record) error {
	if len(records) < 1 || len(records) > MaxBatchRecords {
		return errors.New("invalid sink batch size")
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for _, record := range records {
		row, e := rowFor(record)
		if e != nil {
			return e
		}
		if e = encoder.Encode(row); e != nil {
			return e
		}
		if body.Len() > 16*1024*1024 {
			return errors.New("sink batch exceeds byte limit")
		}
	}
	endpoint := *c.endpoint
	query := endpoint.Query()
	query.Set("database", c.database)
	query.Set("query", "INSERT INTO event_facts_raw FORMAT JSONEachRow")
	query.Set("async_insert", "0")
	query.Set("wait_end_of_query", "1")
	query.Set("date_time_input_format", "best_effort")
	// A missing operator migration must fail closed instead of silently ignoring
	// the precision columns under a server profile that skips unknown fields.
	query.Set("input_format_skip_unknown_fields", "0")
	endpoint.RawQuery = query.Encode()
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), &body)
	if e != nil {
		return ErrSinkUnavailable
	}
	request.Header.Set("Content-Type", "application/x-ndjson")
	if c.username != "" || c.password != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	response, e := c.client.Do(request)
	if e != nil {
		return ErrSinkUnavailable
	}
	defer response.Body.Close()
	result, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(result) > 65536 || response.StatusCode != http.StatusOK || strings.TrimSpace(string(result)) != "" {
		return fmt.Errorf("%w (HTTP %d)", ErrSinkUnavailable, response.StatusCode)
	}
	return nil
}

var _ Sink = (*ClickHouse)(nil)
