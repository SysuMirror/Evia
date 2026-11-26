package analysis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// ReadEventLastSeen returns the latest warehouse received_at timestamp per event_key
// for the requested project (and optional environment). It is a bounded read-only
// observability helper used by the catalog listing; callers degrade gracefully on error.
func (c *ClickHouse) ReadEventLastSeen(ctx context.Context, projectID, environment string, eventKeys []string) (map[string]time.Time, error) {
	if len(eventKeys) == 0 {
		return map[string]time.Time{}, nil
	}
	if len(eventKeys) > 200 {
		eventKeys = eventKeys[:200]
	}
	table := "`" + c.database + "`.`" + c.table + "`"
	params := map[string]string{"project": projectID}
	placeholders := make([]string, 0, len(eventKeys))
	for i, key := range eventKeys {
		name := fmt.Sprintf("k%d", i)
		params[name] = key
		placeholders = append(placeholders, "{"+name+":String}")
	}
	where := "project_id={project:String} AND event_key IN (" + strings.Join(placeholders, ",") + ")"
	if environment != "" {
		params["environment"] = environment
		where += " AND environment={environment:String}"
	}
	sql := "SELECT event_key, maxOrNull(toUnixTimestamp64Nano(received_at)) AS last_seen_ns FROM " +
		table + " WHERE " + where + " GROUP BY event_key FORMAT JSONEachRow"

	executionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	queryID := "evia-last-seen-" + ids.New()
	response, err := c.request(executionCtx, sql, queryID, params, true)
	if err != nil {
		if executionCtx.Err() != nil {
			c.kill(queryID)
		}
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		return nil, fmt.Errorf("read last seen failed")
	}
	if response.StatusCode != http.StatusOK {
		return nil, clickHouseFailure(body)
	}
	out := make(map[string]time.Time, len(eventKeys))
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 128*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			continue
		}
		key, ok := row["event_key"].(string)
		if !ok || key == "" {
			continue
		}
		if ns, ok := integer(row["last_seen_ns"]); ok && ns > 0 {
			out[key] = time.Unix(0, ns).UTC()
		}
	}
	return out, nil
}
