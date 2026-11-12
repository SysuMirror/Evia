package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

// ArchiveExecutor reads the exact expired slice the retention executor would
// delete and streams it as JSONEachRow into the archive artifact. It shares
// the bound-parameter scope predicate so archive and deletion can never diverge
// at the cutoff boundary.
type ArchiveExecutor struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
}

// NewArchiveExecutor validates the target with the same rules as the other
// ClickHouse adapters and gives streaming a longer timeout than control calls.
func NewArchiveExecutor(c ClickHouseConfig, client *http.Client) (*ArchiveExecutor, error) {
	endpoint, e := url.Parse(c.URL)
	if e != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
		return nil, errors.New("invalid clickhouse endpoint or database")
	}
	if !databasePattern.MatchString(c.Database) {
		return nil, errors.New("invalid clickhouse database configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &ArchiveExecutor{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client}, nil
}

func (x *ArchiveExecutor) scopeParams(project, environment string, cutoff time.Time) map[string]string {
	return map[string]string{
		"project":     project,
		"environment": environment,
		"cutoff":      clickhouseDateTime(cutoff),
	}
}

func (x *ArchiveExecutor) scopePredicate() string {
	return "project_id={project:String} AND environment={environment:String} AND " +
		"event_time < {cutoff:DateTime64(3)}"
}

func (x *ArchiveExecutor) queryURL(parameters map[string]string) string {
	endpoint := *x.endpoint
	values := endpoint.Query()
	values.Set("database", x.database)
	values.Set("wait_end_of_query", "1")
	for key, value := range parameters {
		values.Set("param_"+key, value)
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String()
}

func (x *ArchiveExecutor) newRequest(ctx context.Context, query string, parameters map[string]string) (*http.Request, error) {
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, x.queryURL(parameters), strings.NewReader(query))
	if e != nil {
		return nil, ErrSinkUnavailable
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if x.username != "" || x.password != "" {
		request.SetBasicAuth(x.username, x.password)
	}
	return request, nil
}

// CountExpired mirrors retention.FactSweeper counting so the archive service
// can bound the scope before opening an artifact.
func (x *ArchiveExecutor) CountExpired(ctx context.Context, project, environment string, cutoff time.Time) (int64, error) {
	params := x.scopeParams(project, environment, cutoff)
	query := "SELECT count() FROM `" + x.database + "`.`event_facts_raw` WHERE " + x.scopePredicate() + " FORMAT TSV"
	request, e := x.newRequest(ctx, query, params)
	if e != nil {
		return 0, e
	}
	response, e := x.client.Do(request)
	if e != nil {
		return 0, ErrSinkUnavailable
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(body) > 65536 {
		return 0, ErrSinkUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w (HTTP %d): %s", ErrSinkUnavailable, response.StatusCode, strings.TrimSpace(string(body)))
	}
	count, e := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if e != nil {
		return 0, ErrSinkUnavailable
	}
	return count, nil
}

// lineCounter counts JSONEachRow records as they pass through to the sink.
type lineCounter struct {
	inner io.Writer
	rows  int64
}

func (c *lineCounter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			c.rows++
		}
	}
	return c.inner.Write(p)
}

// StreamExpired dumps every expired row as JSONEachRow into sink and returns
// the streamed row count plus the observed event_time coverage.
func (x *ArchiveExecutor) StreamExpired(ctx context.Context, sink io.Writer, scope archiving.Scope) (archiving.FactWindow, error) {
	params := x.scopeParams(scope.ProjectID, scope.Environment, scope.Cutoff)
	coverageQuery := "SELECT min(event_time), max(event_time) FROM `" + x.database + "`.`event_facts_raw` WHERE " +
		x.scopePredicate() + " FORMAT TSV"
	coverageRequest, e := x.newRequest(ctx, coverageQuery, params)
	if e != nil {
		return archiving.FactWindow{}, e
	}
	coverageResponse, e := x.client.Do(coverageRequest)
	if e != nil {
		return archiving.FactWindow{}, ErrSinkUnavailable
	}
	coverageBody, e := io.ReadAll(io.LimitReader(coverageResponse.Body, 65537))
	coverageResponse.Body.Close()
	if e != nil || len(coverageBody) > 65536 || coverageResponse.StatusCode != http.StatusOK {
		return archiving.FactWindow{}, ErrSinkUnavailable
	}
	fields := strings.Split(strings.TrimSpace(string(coverageBody)), "\t")
	if len(fields) != 2 || fields[0] == "" || fields[0] == "0000-00-00 00:00:00.000" {
		return archiving.FactWindow{}, errors.New("archive coverage query returned no window")
	}
	start, e := time.Parse("2006-01-02 15:04:05.000", fields[0])
	if e != nil {
		return archiving.FactWindow{}, ErrSinkUnavailable
	}
	end, e := time.Parse("2006-01-02 15:04:05.000", fields[1])
	if e != nil {
		return archiving.FactWindow{}, ErrSinkUnavailable
	}

	params["limit"] = strconv.FormatInt(archiving.MaxArchiveRows, 10)
	streamQuery := "SELECT * FROM `" + x.database + "`.`event_facts_raw` WHERE " + x.scopePredicate() +
		" ORDER BY event_time,event_id LIMIT {limit:UInt64} FORMAT JSONEachRow"
	streamRequest, e := x.newRequest(ctx, streamQuery, params)
	if e != nil {
		return archiving.FactWindow{}, e
	}
	response, e := x.client.Do(streamRequest)
	if e != nil {
		return archiving.FactWindow{}, ErrSinkUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return archiving.FactWindow{}, fmt.Errorf("%w (HTTP %d): %s", ErrSinkUnavailable, response.StatusCode, strings.TrimSpace(string(body)))
	}
	counter := &lineCounter{inner: sink}
	// Buffer so a slow artifact disk never holds ClickHouse connections without
	// backpressure; the artifact writer enforces the byte ceiling.
	if _, e = io.Copy(counter, bufio.NewReaderSize(response.Body, 64*1024)); e != nil {
		return archiving.FactWindow{}, e
	}
	return archiving.FactWindow{Rows: counter.rows, WindowStart: start.UTC(), WindowEnd: end.UTC()}, nil
}

var _ archiving.FactReader = (*ArchiveExecutor)(nil)
