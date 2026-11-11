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

	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

// RetentionExecutor applies age-based retention to the raw fact table. It is
// the pipeline-side adapter for retention.FactSweeper. Like the erasure
// executor it issues synchronous ALTER DELETE mutations because the table
// carries identity projections that ClickHouse 26.8 will not remove with a
// lightweight DELETE. The cutoff is always a bound parameter, never concatenated.
type RetentionExecutor struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
}

// NewRetentionExecutor validates the target with the same rules and credential
// profile as the other ClickHouse adapters.
func NewRetentionExecutor(c ClickHouseConfig, client *http.Client) (*RetentionExecutor, error) {
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
	return &RetentionExecutor{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client}, nil
}

// clickhouseDateTime formats a UTC instant for a bound DateTime64 parameter.
func clickhouseDateTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000")
}

func (x *RetentionExecutor) scopeParams(project, environment string, cutoff time.Time) map[string]string {
	return map[string]string{
		"project":     project,
		"environment": environment,
		"cutoff":      clickhouseDateTime(cutoff),
	}
}

func (x *RetentionExecutor) request(ctx context.Context, query string, parameters map[string]string) ([]byte, error) {
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
		return nil, ErrSinkUnavailable
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

func (x *RetentionExecutor) scopePredicate() string {
	return "project_id={project:String} AND environment={environment:String} AND " +
		"event_time < {cutoff:DateTime64(3)}"
}

// CountExpired returns the number of facts older than the cutoff in the scope.
func (x *RetentionExecutor) CountExpired(ctx context.Context, project, environment string, cutoff time.Time) (uint64, error) {
	query := "SELECT count() FROM `" + x.database + "`.`event_facts_raw` WHERE " + x.scopePredicate() + " FORMAT TSV"
	body, e := x.request(ctx, query, x.scopeParams(project, environment, cutoff))
	if e != nil {
		return 0, e
	}
	count, e := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if e != nil {
		return 0, ErrSinkUnavailable
	}
	return count, nil
}

// DeleteExpired removes facts older than the cutoff, rebuilding projections
// before returning, and verifies the expired scope is empty. It returns the
// number of rows that were eligible prior to the mutation.
func (x *RetentionExecutor) DeleteExpired(ctx context.Context, project, environment string, cutoff time.Time) (uint64, error) {
	candidates, e := x.CountExpired(ctx, project, environment, cutoff)
	if e != nil {
		return 0, e
	}
	if candidates == 0 {
		return 0, nil
	}
	query := "ALTER TABLE `" + x.database + "`.`event_facts_raw` DELETE WHERE " + x.scopePredicate() +
		" SETTINGS mutations_sync=1"
	if _, e = x.request(ctx, query, x.scopeParams(project, environment, cutoff)); e != nil {
		return 0, e
	}
	remaining, e := x.CountExpired(ctx, project, environment, cutoff)
	if e != nil {
		return 0, e
	}
	if remaining != 0 {
		return 0, fmt.Errorf("%w: retention delete left %d expired rows", ErrSinkUnavailable, remaining)
	}
	return candidates, nil
}

// Compile-time assertion that the executor satisfies the domain port.
var _ retention.FactSweeper = (*RetentionExecutor)(nil)
