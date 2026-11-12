package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// MaxReadinessProofs bounds a single diagnostics read regardless of history.
const MaxReadinessProofs = 50

// ProofReceipt is the newest observed readiness probe for one release scope.
type ProofReceipt struct {
	Environment  string
	ReleaseID    string
	Revision     int64
	LastObserved time.Time
	ProofCount   int64
}

// ReadinessReader is the read-only diagnostics port over pipeline_readiness.
// It never touches event fact tables and cannot mutate probe receipts.
type ReadinessReader struct {
	ch *ClickHouse
}

// NewReadinessReader reuses the validated sink configuration conventions.
func NewReadinessReader(cfg ClickHouseConfig) (*ReadinessReader, error) {
	ch, err := NewClickHouse(cfg)
	if err != nil {
		return nil, err
	}
	return &ReadinessReader{ch: ch}, nil
}

func (r *ReadinessReader) query(ctx context.Context, sql string, params map[string]string) ([]byte, error) {
	endpoint := *r.ch.endpoint
	values := endpoint.Query()
	values.Set("database", r.ch.database)
	values.Set("query", sql)
	values.Set("wait_end_of_query", "1")
	for key, value := range params {
		values.Set("param_"+key, value)
	}
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return nil, ErrSinkUnavailable
	}
	if r.ch.username != "" {
		request.SetBasicAuth(r.ch.username, r.ch.password)
	}
	response, err := r.ch.client.Do(request)
	if err != nil {
		return nil, ErrSinkUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 || response.StatusCode != http.StatusOK {
		return nil, ErrSinkUnavailable
	}
	return raw, nil
}

// LatestProofs returns the newest receipt per environment/release/revision
// for one project, newest first. An empty slice means no probe has ever been
// observed within the table retention window.
func (r *ReadinessReader) LatestProofs(ctx context.Context, projectID string, limit int) ([]ProofReceipt, error) {
	if !uuidPattern.MatchString(projectID) {
		return nil, errors.New("invalid project id for readiness read")
	}
	if limit < 1 || limit > MaxReadinessProofs {
		limit = MaxReadinessProofs
	}
	sql := `SELECT environment,
       release_id,
       revision,
       toString(max(observed_at)) AS last_observed,
       count() AS proof_count
FROM pipeline_readiness
WHERE project_id = {project:String}
GROUP BY environment, release_id, revision
ORDER BY last_observed DESC
LIMIT {limit:UInt64}
FORMAT JSONEachRow`
	raw, err := r.query(ctx, sql, map[string]string{"project": projectID, "limit": strconv.Itoa(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ProofReceipt, 0)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var row struct {
			Environment  string `json:"environment"`
			ReleaseID    string `json:"release_id"`
			Revision     int64  `json:"revision"`
			LastObserved string `json:"last_observed"`
			ProofCount   int64  `json:"proof_count"`
		}
		if err = decoder.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			return nil, ErrSinkUnavailable
		}
		observed, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", row.LastObserved, time.UTC)
		if err != nil {
			return nil, ErrSinkUnavailable
		}
		out = append(out, ProofReceipt{
			Environment:  row.Environment,
			ReleaseID:    row.ReleaseID,
			Revision:     row.Revision,
			LastObserved: observed,
			ProofCount:   row.ProofCount,
		})
	}
	return out, nil
}
