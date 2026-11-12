package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/backfilling"
)

// BackfillInserter re-ingests archived JSONEachRow facts into the raw table.
// The event_facts view deduplicates by event identity, so reinserted rows do
// not change query results; the inserter still counts every streamed line so
// the worker can verify the artifact was consumed in full.
type BackfillInserter struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
}

func NewBackfillInserter(c ClickHouseConfig, client *http.Client) (*BackfillInserter, error) {
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
	return &BackfillInserter{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client}, nil
}

// lineCountReader counts JSONEachRow records as they flow to ClickHouse.
type lineCountReader struct {
	inner io.Reader
	rows  int64
}

func (r *lineCountReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	for _, b := range p[:n] {
		if b == '\n' {
			r.rows++
		}
	}
	return n, err
}

// InsertFacts streams reader into event_facts_raw and returns the line count.
func (x *BackfillInserter) InsertFacts(ctx context.Context, _, _ string, reader io.Reader) (int64, error) {
	endpoint := *x.endpoint
	values := endpoint.Query()
	values.Set("database", x.database)
	values.Set("wait_end_of_query", "1")
	values.Set("query", "INSERT INTO event_facts_raw FORMAT JSONEachRow")
	endpoint.RawQuery = values.Encode()

	counter := &lineCountReader{inner: reader}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), counter)
	if e != nil {
		return 0, ErrSinkUnavailable
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if x.username != "" || x.password != "" {
		request.SetBasicAuth(x.username, x.password)
	}
	response, e := x.client.Do(request)
	if e != nil {
		return 0, ErrSinkUnavailable
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 65536))
	if response.StatusCode != http.StatusOK {
		return 0, errors.New("backfill insert rejected: " + strings.TrimSpace(string(body)))
	}
	if counter.rows == 0 {
		return 0, errors.New("backfill stream contained no rows")
	}
	return counter.rows, nil
}

var _ backfilling.FactSink = (*BackfillInserter)(nil)
