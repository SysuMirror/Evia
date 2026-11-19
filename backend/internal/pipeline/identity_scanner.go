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

	"github.com/howtomakeaname/Evia/backend/internal/identity"
)

// identityScanResponseBound caps one fold scan batch at 32 MiB. The pair
// batch limit bounds rows; this bounds bytes so a hostile or corrupted
// warehouse answer cannot grow worker memory without limit.
const identityScanResponseBound = 32 << 20

// IdentityLinkScanner reads deduplicated sys.identity_link pairs from the
// event warehouse for the identity fold. Pairs are aggregated per scan
// window: the same uid/anonymous pair observed by many events yields one row
// with its earliest and latest reception times, so repeated scans converge
// instead of amplifying.
type IdentityLinkScanner struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
}

// NewIdentityLinkScanner validates the target like the fact sink and reuses
// the same credential profile rather than introducing a second configuration.
func NewIdentityLinkScanner(c ClickHouseConfig, client *http.Client) (*IdentityLinkScanner, error) {
	endpoint, e := url.Parse(c.URL)
	if e != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
		return nil, errors.New("invalid clickhouse endpoint or database")
	}
	if !databasePattern.MatchString(c.Database) {
		return nil, errors.New("invalid clickhouse database configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &IdentityLinkScanner{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client}, nil
}

// ScanLinks returns identity-link pairs received strictly after the instant,
// ordered by latest reception so batches partition a scan window. The boolean
// reports exhaustion: fewer rows than the limit means the window ended.
func (s *IdentityLinkScanner) ScanLinks(ctx context.Context, scope identity.Scope, after time.Time, limit int) ([]identity.LinkScan, bool, error) {
	if limit <= 0 || limit > identity.DefaultScanLimit {
		limit = identity.DefaultScanLimit
	}
	query := "SELECT uid, anonymous_id, min(received_at), max(received_at) FROM `" + s.database +
		"`.event_facts WHERE project_id={project:String} AND environment={environment:String}" +
		" AND event_key='sys.identity_link' AND received_at > fromUnixTimestamp64Nano({after:Int64})" +
		" AND uid IS NOT NULL AND anonymous_id IS NOT NULL AND uid != '' AND anonymous_id != ''" +
		" GROUP BY uid, anonymous_id ORDER BY max(received_at), uid, anonymous_id" +
		" LIMIT {limit:UInt64} FORMAT TabSeparatedWithNames"
	body, e := s.request(ctx, query, map[string]string{
		"project":     scope.ProjectID,
		"environment": scope.Environment,
		"after":       strconv.FormatInt(after.UnixNano(), 10),
		"limit":       strconv.Itoa(limit),
	})
	if e != nil {
		return nil, false, e
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil, true, nil
	}
	// Drop the header row emitted by TabSeparatedWithNames.
	lines = lines[1:]
	links := make([]identity.LinkScan, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, false, fmt.Errorf("%w: malformed identity link row", ErrSinkUnavailable)
		}
		first, e := parseWarehouseTime(fields[2])
		if e != nil {
			return nil, false, fmt.Errorf("%w: malformed identity link timestamp", ErrSinkUnavailable)
		}
		last, e := parseWarehouseTime(fields[3])
		if e != nil {
			return nil, false, fmt.Errorf("%w: malformed identity link timestamp", ErrSinkUnavailable)
		}
		links = append(links, identity.LinkScan{
			UID:         fields[0],
			AnonymousID: fields[1],
			FirstSeenAt: first,
			LastSeenAt:  last,
		})
	}
	return links, len(links) < limit, nil
}

func parseWarehouseTime(value string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05.999999999", strings.TrimSpace(value))
}

func (s *IdentityLinkScanner) request(ctx context.Context, query string, parameters map[string]string) ([]byte, error) {
	endpoint := *s.endpoint
	values := endpoint.Query()
	values.Set("database", s.database)
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
	if s.username != "" || s.password != "" {
		request.SetBasicAuth(s.username, s.password)
	}
	response, e := s.client.Do(request)
	if e != nil {
		return nil, ErrSinkUnavailable
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, identityScanResponseBound+1))
	if e != nil || len(body) > identityScanResponseBound {
		return nil, ErrSinkUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrSinkUnavailable, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
