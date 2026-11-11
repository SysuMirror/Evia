package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
)

// ErasureExecutor performs synchronous identity deletes against the raw fact
// table. The table carries identity projections (migration 005), and
// ClickHouse 26.8 rejects lightweight DELETE on projected tables, so erasure
// uses a heavyweight ALTER DELETE mutation with mutations_sync=1; the call
// returns only after parts and projections are rebuilt, making the delete
// immediately verifiable through the view.
type ErasureExecutor struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
}

// NewErasureExecutor validates the target like the fact sink and keeps the
// same credential profile rather than introducing a second configuration.
func NewErasureExecutor(c ClickHouseConfig, client *http.Client) (*ErasureExecutor, error) {
	endpoint, e := url.Parse(c.URL)
	if e != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
		return nil, errors.New("invalid clickhouse endpoint or database")
	}
	if !databasePattern.MatchString(c.Database) {
		return nil, errors.New("invalid clickhouse database configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &ErasureExecutor{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client}, nil
}

func identityColumn(kind string) (string, error) {
	switch kind {
	case erasure.SubjectUID:
		return "uid", nil
	case erasure.SubjectAnonymousID:
		return "anonymous_id", nil
	default:
		return "", errors.New("unsupported erasure subject kind")
	}
}

func (x *ErasureExecutor) predicate(column string) string {
	return "project_id={project:String} AND environment={environment:String} AND " +
		column + "={subject:String}"
}

func (x *ErasureExecutor) request(ctx context.Context, query string, parameters map[string]string) ([]byte, error) {
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

// DeleteIdentity issues a synchronous ALTER DELETE mutation that also rebuilds
// the table's projections before returning.
func (x *ErasureExecutor) DeleteIdentity(ctx context.Context, project, environment, subjectKind, subjectValue string) error {
	column, e := identityColumn(subjectKind)
	if e != nil {
		return e
	}
	query := "ALTER TABLE `" + x.database + "`.`event_facts_raw` DELETE WHERE " + x.predicate(column) + " SETTINGS mutations_sync=1"
	_, e = x.request(ctx, query, map[string]string{
		"project": project, "environment": environment, "subject": subjectValue,
	})
	return e
}

// RemainingIdentityRows counts rows still visible for the identity. The
// deduplicated view inherits the delete, so zero here means zero served rows.
func (x *ErasureExecutor) RemainingIdentityRows(ctx context.Context, project, environment, subjectKind, subjectValue string) (uint64, error) {
	column, e := identityColumn(subjectKind)
	if e != nil {
		return 0, e
	}
	query := "SELECT count() FROM `" + x.database + "`.`event_facts_raw` WHERE " + x.predicate(column) + " FORMAT TSV"
	body, e := x.request(ctx, query, map[string]string{
		"project": project, "environment": environment, "subject": subjectValue,
	})
	if e != nil {
		return 0, e
	}
	count, e := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if e != nil {
		return 0, ErrSinkUnavailable
	}
	return count, nil
}
