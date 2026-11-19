//go:build integration

package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func clickHouseDatabase(t *testing.T) *ClickHouse {
	t.Helper()
	endpoint := os.Getenv("EVIA_TEST_CLICKHOUSE_URL")
	if endpoint == "" {
		t.Fatal("EVIA_TEST_CLICKHOUSE_URL is required for ClickHouse integration tests")
	}
	username := os.Getenv("EVIA_TEST_CLICKHOUSE_USERNAME")
	password := os.Getenv("EVIA_TEST_CLICKHOUSE_PASSWORD")
	if username == "" || password == "" {
		t.Fatal("explicit ClickHouse test credentials are required")
	}
	name := "evia_analysis_" + strings.ReplaceAll(ids.New(), "-", "")
	admin, e := NewClickHouse(ClickHouseConfig{URL: endpoint, Username: username, Password: password, Database: "default"})
	if e != nil {
		t.Fatal(e)
	}
	chExec(t, admin, "CREATE DATABASE `"+name+"`")
	t.Cleanup(func() { chExec(t, admin, "DROP DATABASE IF EXISTS `"+name+"` SYNC") })
	client, e := NewClickHouse(ClickHouseConfig{URL: endpoint, Username: username, Password: password, Database: name})
	if e != nil {
		t.Fatal(e)
	}
	migrations, e := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "clickhouse", "*.sql"))
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range migrations {
		ddl, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		lines := strings.Split(string(ddl), "\n")
		clean := make([]string, 0, len(lines))
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				clean = append(clean, line)
			}
		}
		for _, statement := range strings.Split(strings.Join(clean, "\n"), ";") {
			if strings.TrimSpace(statement) != "" {
				chExec(t, client, statement)
			}
		}
	}
	return client
}
func chExec(t *testing.T, client *ClickHouse, query string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, e := client.request(ctx, query, "evia-analysis-test-"+ids.New(), nil, false)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if e != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("ClickHouse fixture command failed: status=%d error=%v body=%s", response.StatusCode, e, data)
	}
	return data
}
func seedRows(t *testing.T, client *ClickHouse, rows []map[string]any) {
	t.Helper()
	var body strings.Builder
	body.WriteString("INSERT INTO event_facts_raw FORMAT JSONEachRow\n")
	for _, row := range rows {
		encoded, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		body.Write(encoded)
		body.WriteByte('\n')
	}
	chExec(t, client, body.String())
}
func fact(id, key, uid, anonymous, source string, seconds int, props map[string]any) map[string]any {
	when := time.Date(2026, 11, 7, 10, 0, seconds, 0, time.UTC)
	properties, _ := json.Marshal(props)
	contextJSON, _ := json.Marshal(map[string]any{"session_id": "session-one", "tab_id": "tab-one", "sequence": seconds + 1, "page_view_id": "page-two", "source_page_view_id": "page-one", "operation_id": "operation-one"})
	var user, anon any
	if uid != "" {
		user = uid
	}
	if anonymous != "" {
		anon = anonymous
	}
	return map[string]any{"project_id": "project-one", "environment": "development", "source_id": source, "source_kind": "browser", "event_id": id, "event_key": key, "schema_version": 1, "release_revision": 1, "occurred_at": when.Format("2006-01-02 15:04:05.000"), "event_time": when.Format("2006-01-02 15:04:05.000"), "received_at": when.Add(time.Second).Format("2006-01-02 15:04:05.000000000"), "canonical_hash": strings.Repeat("a", 64), "uid": user, "anonymous_id": anon, "session_id": "session-one", "tab_id": "tab-one", "sequence": seconds + 1, "page_view_id": "page-two", "operation_id": "operation-one", "sdk_name": "synthetic", "sdk_version": "1", "context_json": string(contextJSON), "properties_json": string(properties)}
}
func aggregateRequest(t *testing.T) Request {
	t.Helper()
	r := request(t, baseRequest)
	r.EventKeys = []string{"sys.page_view"}
	r.Subject = SubjectUID
	return r
}
func property(name, kind string) *Field {
	return &Field{Kind: "property", EventKey: "sys.page_view", SchemaVersion: 1, Key: name, ResolvedType: kind}
}
func execute(t *testing.T, client *ClickHouse, r Request) Result {
	t.Helper()
	result, e := client.Execute(context.Background(), Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	return result
}
func metricNumber(t *testing.T, result Result, column string) float64 {
	t.Helper()
	if len(result.Rows) != 1 {
		t.Fatal("expected one aggregate", result)
	}
	number, ok := result.Rows[0][column].(json.Number)
	if !ok {
		t.Fatal("expected numeric aggregate", result.Rows)
	}
	value, e := number.Float64()
	if e != nil {
		t.Fatal(e)
	}
	return value
}
func TestClickHouseDeduplicatedMetricsAndTypedFilters(t *testing.T) {
	client := clickHouseDatabase(t)
	attack := "O'Hare'; DROP TABLE event_facts_raw --"
	rows := []map[string]any{fact("first", "sys.page_view", "user-one", "anon-one", "source-one", 0, map[string]any{"score": 0, "enabled": false, "label": attack}), fact("second", "sys.page_view", "user-one", "anon-one", "source-one", 1, map[string]any{"score": 5, "enabled": true}), fact("third", "sys.page_view", "user-two", "anon-one", "source-one", 2, map[string]any{"score": 10, "enabled": false}), fact("fourth", "sys.page_view", "", "anon-two", "source-one", 3, map[string]any{"score": nil}), fact("first", "sys.page_view", "user-two", "anon-one", "source-two", 4, map[string]any{"score": 20, "enabled": false}), fact("fifth", "sys.page_view", "", "anon-three", "source-one", 5, map[string]any{})}
	rows[5]["received_at"] = "2026-11-07 10:00:06.000000123"
	for index, tags := range map[int]any{0: []string{"one", "two"}, 1: []string{}, 3: nil} {
		var values map[string]any
		json.Unmarshal([]byte(rows[index]["properties_json"].(string)), &values)
		values["tags"] = tags
		encoded, _ := json.Marshal(values)
		rows[index]["properties_json"] = string(encoded)
	}
	replay := fact("first", "sys.page_view", "wrong-winner", "anon-wrong", "source-one", 0, map[string]any{"score": 999, "enabled": true})
	replay["received_at"] = "2026-11-07 10:00:30.000000000"
	replay["canonical_hash"] = strings.Repeat("b", 64)
	rows = append(rows, replay)
	other := fact("other-project", "sys.page_view", "outsider", "anon-other", "source-one", 2, map[string]any{"score": 999})
	other["project_id"] = "project-two"
	rows = append(rows, other)
	other = fact("other-environment", "sys.page_view", "outsider", "anon-other", "source-one", 2, map[string]any{"score": 999})
	other["environment"] = "production"
	rows = append(rows, other)
	// Physical arrival order does not dictate event-time ordering or dedup winners.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	seedRows(t, client, rows)
	r := aggregateRequest(t)
	r.Metrics = []Metric{{Op: "count", Accuracy: AccuracyExact}, {Op: "unique", Accuracy: AccuracyExact}, {Op: "sum", Field: property("score", "number"), Accuracy: AccuracyExact}, {Op: "avg", Field: property("score", "number"), Accuracy: AccuracyExact}, {Op: "min", Field: property("score", "number"), Accuracy: AccuracyExact}, {Op: "max", Field: property("score", "number"), Accuracy: AccuracyExact}, {Op: "p95", Field: property("score", "number"), Accuracy: AccuracyExact}}
	result := execute(t, client, r)
	for key, want := range map[string]float64{"metric_0": 6, "metric_1": 2, "metric_2": 35, "metric_3": 8.75, "metric_4": 0, "metric_5": 20, "metric_6": 20} {
		if got := metricNumber(t, result, key); got != want {
			t.Fatal(key, got, want)
		}
	}
	if result.Metadata.Accuracy != AccuracyExact || result.Metadata.DataWatermark == nil || result.Metadata.Coverage.State != "provisional" {
		t.Fatal(result.Metadata)
	}
	if result.Metadata.DataWatermark.Format(time.RFC3339Nano) != "2026-11-07T10:00:06.000000123Z" {
		t.Fatal("watermark lost precision or included replay instead of winner", result.Metadata.DataWatermark)
	}
	r.Metrics = []Metric{{Op: "unique", Accuracy: AccuracyExact}}
	r.Subject = SubjectAnonymous
	if got := metricNumber(t, execute(t, client, r), "metric_0"); got != 3 {
		t.Fatal("anonymous identity boundary", got)
	}
	r.Metrics = []Metric{{Op: "count", Accuracy: AccuracyExact}}
	for _, tc := range []struct {
		name, op, kind string
		value          any
		want           float64
	}{
		{"false is not missing", "eq", "boolean", false, 3}, {"zero is not missing", "eq", "number", json.Number("0"), 1}, {"missing excludes null", "is_missing", "number", nil, 1}, {"present includes null", "is_present", "number", nil, 5}, {"dedup precedes filter", "eq", "boolean", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "score"
			if tc.kind == "boolean" {
				key = "enabled"
			}
			r.Filter = &Filter{Op: tc.op, Field: property(key, tc.kind), Value: tc.value}
			if got := metricNumber(t, execute(t, client, r), "metric_0"); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
	r.Filter = &Filter{Op: "eq", Field: property("label", "string"), Value: attack}
	if got := metricNumber(t, execute(t, client, r), "metric_0"); got != 1 {
		t.Fatal("bound string not compared literally", got)
	}
	for _, tc := range []struct {
		op    string
		value any
		want  float64
	}{{"eq", []any{}, 1}, {"eq", []any{"one", "two"}, 1}, {"contains", "one", 1}, {"is_present", nil, 3}, {"is_missing", nil, 3}} {
		r.Filter = &Filter{Op: tc.op, Field: property("tags", "array:string"), Value: tc.value}
		if got := metricNumber(t, execute(t, client, r), "metric_0"); got != tc.want {
			t.Fatal("array predicate", tc.op, got, tc.want)
		}
	}
	r.Filter = nil
	r.EventKeys = []string{"no_events"}
	empty := execute(t, client, r)
	if metricNumber(t, empty, "metric_0") != 0 || empty.Metadata.DataWatermark != nil {
		t.Fatal("empty data fabricated visibility", empty)
	}
	r.EventKeys = []string{"sys.page_view"}
	r.Filter = nil
	r.Interval = ""
	r.GroupBy = []Field{*property("score", "number")}
	groups := execute(t, client, r)
	missing, nulls, zero := false, false, false
	for _, group := range groups.Rows {
		if group["group_0"] == nil {
			if group["group_0_present"] == false {
				missing = true
			}
			if group["group_0_present"] == true {
				nulls = true
			}
		}
		if n, ok := group["group_0"].(json.Number); ok && n == "0" {
			zero = true
		}
	}
	if !missing || !nulls || !zero {
		t.Fatal("grouping collapsed missing/null/zero", groups)
	}
	r.GroupBy = nil
	r.Metrics = []Metric{{Op: "unique", Accuracy: AccuracyApproximate}}
	r.Subject = SubjectUID
	approximate := execute(t, client, r)
	if metricNumber(t, approximate, "metric_0") != 2 || approximate.Metadata.Accuracy != AccuracyApproximate {
		t.Fatal(approximate)
	}
	r.Metrics = []Metric{{Op: "count", Accuracy: AccuracyExact}}
	r.TimeRange.From = time.Date(2026, 11, 7, 10, 0, 0, 500000, time.UTC)
	r.TimeRange.To = time.Date(2026, 11, 7, 10, 0, 1, 500000, time.UTC)
	r.Metrics = []Metric{{Op: "sum", Field: property("score", "number"), Accuracy: AccuracyExact}}
	if got := metricNumber(t, execute(t, client, r), "metric_0"); got != 5 {
		t.Fatal("sub-millisecond half-open boundaries rounded", got)
	}
	r.Metrics = []Metric{{Op: "count", Accuracy: AccuracyExact}}
	r.TimeRange = aggregateRequest(t).TimeRange
	r.Interval = "hour"
	bucket := execute(t, client, r)
	if len(bucket.Rows) != 1 || bucket.Rows[0]["bucket"] != "2026-11-07T10:00:00Z" {
		t.Fatal(bucket)
	}
}
func TestClickHouseJourneyAndOrderedFunnel(t *testing.T) {
	client := clickHouseDatabase(t)
	rows := []map[string]any{fact("anonymous-before", "sys.page_view", "", "device-one", "source-one", 0, map[string]any{}), fact("logged-in", "sys.page_view", "user-one", "device-one", "source-one", 1, map[string]any{}), fact("switched-account", "sys.page_view", "user-two", "device-one", "source-one", 2, map[string]any{})}
	add := func(subject string, start, finish int, item1, item2 string) {
		rows = append(rows, fact(subject+"-start", "open", subject, "device", "source-one", start, map[string]any{"item_id": item1}), fact(subject+"-finish", "finish", subject, "device", "source-one", finish, map[string]any{"item_id": item2}))
	}
	add("complete", 10, 20, "a", "a")
	add("wrong-order", 20, 10, "a", "a")
	add("outside-window", 0, 50, "a", "a")
	add("wrong-entity", 10, 20, "a", "b")
	add("same-time", 30, 30, "a", "a")
	// A later start, not the first start, is the valid window for this subject.
	rows = append(rows, fact("later-first", "open", "later-start", "device", "source-one", 0, map[string]any{"item_id": "a"}), fact("later-second", "open", "later-start", "device", "source-one", 40, map[string]any{"item_id": "a"}), fact("later-finish", "finish", "later-start", "device", "source-one", 50, map[string]any{"item_id": "a"}))
	// Equal-time ordering uses session sequence; without that evidence it cannot
	// advance a funnel even when UUID lexical order would happen to look ordered.
	for _, row := range rows {
		if row["event_id"] == "same-time-finish" {
			row["context_json"] = `{"sequence":100}`
			row["sequence"] = 100
		}
	}
	preciseStart := fact("precise-start", "open", "nanosecond-subject", "device", "source-one", 30, map[string]any{"item_id": "a"})
	preciseEnd := fact("precise-finish", "finish", "nanosecond-subject", "device", "source-one", 30, map[string]any{"item_id": "a"})
	for index, row := range []map[string]any{preciseStart, preciseEnd} {
		row["occurred_at_precise"] = fmt.Sprintf("2026-11-07 10:00:30.000000%d", (index+1)*100)
		row["event_time_precise"] = row["occurred_at_precise"]
		row["session_id"] = nil
		row["tab_id"] = nil
		row["sequence"] = nil
		row["context_json"] = `{}`
	}
	rows = append(rows, preciseEnd, preciseStart)
	seedRows(t, client, rows)
	r := request(t, strings.Replace(baseRequest, `"kind":"trend"`, `"kind":"journey","subject":"uid","identity_filter":{"uid":"user-one"}`, 1))
	timeline := execute(t, client, r)
	if len(timeline.Rows) != 1 || timeline.Rows[0]["event_id"] != "logged-in" || timeline.Rows[0]["original_uid"] != "user-one" {
		t.Fatal("implicit anonymous/account association", timeline)
	}
	contextData := timeline.Rows[0]["context"].(map[string]any)
	if contextData["source_page_view_id"] != "page-one" || contextData["operation_id"] != "operation-one" {
		t.Fatal("journey links lost", contextData)
	}
	funnel := aggregateRequest(t)
	funnel.Kind = "funnel"
	funnel.EventKeys = nil
	funnel.Metrics = nil
	funnel.Funnel = &Funnel{Mode: "ordered_with_interleaving", WindowSeconds: 30, Steps: []Step{{EventKey: "open", SchemaVersion: 1}, {EventKey: "finish", SchemaVersion: 1}}, SameEntity: []EntityField{{StepIndex: 0, Field: Field{Kind: "property", Key: "item_id", EventKey: "open", SchemaVersion: 1, ResolvedType: "string"}}, {StepIndex: 1, Field: Field{Kind: "property", Key: "item_id", EventKey: "finish", SchemaVersion: 1, ResolvedType: "string"}}}}
	result := execute(t, client, funnel)
	if len(result.Rows) != 2 || result.Rows[0]["subjects"] != 7 || result.Rows[1]["subjects"] != 4 {
		t.Fatal("ordered/window/entity funnel mismatch", result)
	}
}
func TestClickHouseCanceledHTTPQueryStopsExecution(t *testing.T) {
	client := clickHouseDatabase(t)
	queryID := "evia-cancel-proof-" + ids.New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		response, e := client.request(ctx, "SELECT sleepEachRow(0.01) FROM numbers(100000) SETTINGS max_block_size=10", queryID, nil, true)
		if e == nil {
			_, e = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		done <- e
	}()
	defer cancel()
	defer client.kill(queryID)
	started := false
	startDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(startDeadline) {
		response, e := client.request(context.Background(), "SELECT count() FROM system.processes WHERE query_id={target:String} FORMAT TabSeparated", "evia-observe-"+ids.New(), map[string]string{"target": queryID}, false)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == 200 && strings.TrimSpace(string(data)) == "1" {
			started = true
			break
		}
		select {
		case e := <-done:
			t.Fatal("query ended before cancellation could be tested", e)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !started {
		t.Fatal("cancellation fixture never entered system.processes")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP cancellation did not interrupt query")
	}
	client.kill(queryID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		endpoint := *client.endpoint
		values := url.Values{"database": {client.database}, "param_target": {queryID}}
		endpoint.RawQuery = values.Encode()
		req, e := http.NewRequest(http.MethodPost, endpoint.String(), strings.NewReader("SELECT count() FROM system.processes WHERE query_id={target:String} FORMAT TabSeparated"))
		if e != nil {
			t.Fatal(e)
		}
		req.SetBasicAuth(client.username, client.password)
		response, e := client.client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == 200 && strings.TrimSpace(string(data)) == "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled ClickHouse query remained active", fmt.Sprint(response.StatusCode))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func retentionFact(id, key, uid string, when time.Time) map[string]any {
	row := fact(id, key, uid, "", "source-one", 0, map[string]any{})
	stamp := when.UTC().Format("2006-01-02 15:04:05.000")
	row["occurred_at"] = stamp
	row["event_time"] = stamp
	row["received_at"] = when.Add(time.Second).UTC().Format("2006-01-02 15:04:05.000000000")
	return row
}

// TestClickHouseRetentionMatrix proves the full chain against the real
// migrated view: bounded bilingual pull through event_facts, UInt8 flag
// decoding, timezone cohort assignment and the Go retention evaluator.
func TestClickHouseRetentionMatrix(t *testing.T) {
	client := clickHouseDatabase(t)
	day := func(dayOfMonth, hour int) time.Time {
		return time.Date(2026, 11, dayOfMonth, hour, 0, 0, 0, time.UTC)
	}
	rows := []map[string]any{
		// Cohort 0 (Nov 7): u1 returns on day 2, u2 returns on day 3.
		retentionFact("u1-start", "sign_up", "u1", day(7, 10)),
		retentionFact("u1-same-day", "login", "u1", day(7, 11)),
		retentionFact("u1-day2", "login", "u1", day(9, 10)),
		retentionFact("u2-start", "sign_up", "u2", day(7, 12)),
		retentionFact("u2-day3", "login", "u2", day(10, 9)),
		// A login before u2's entry must not count as negative-offset retention.
		retentionFact("u2-before", "login", "u2", day(7, 8)),
		// Cohort 1 (Nov 8): u3 returns one period later.
		retentionFact("u3-start", "sign_up", "u3", day(8, 9)),
		retentionFact("u3-return", "login", "u3", day(9, 15)),
		// Cohort 2 (Nov 9): u4 enters with no later return.
		retentionFact("u4-start", "sign_up", "u4", day(9, 16)),
		// Subject with return events only never joins a cohort.
		retentionFact("u5-orphan", "login", "u5", day(7, 9)),
		// Unrelated project/environment facts must be invisible to the scope.
		retentionFact("outsider-start", "sign_up", "outsider", day(7, 10)),
	}
	rows[len(rows)-1]["project_id"] = "project-two"
	seedRows(t, client, rows)

	r := Request{
		Kind: "retention", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day(7, 0), To: day(11, 0)},
		Timezone:  "UTC",
		Retention: &Retention{
			StartEvent:  Step{EventKey: "sign_up", SchemaVersion: 1},
			ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: "day", Periods: 4,
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 5 || result.Metadata.Accuracy != AccuracyExact || result.Metadata.DataWatermark == nil {
		t.Fatalf("retention result contract wrong: %+v metadata=%+v", result.Columns, result.Metadata)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("want three cohorts, got %d: %v", len(result.Rows), result.Rows)
	}
	want := []struct {
		index, size int
		start       string
		retained    []int
	}{
		{0, 2, "2026-11-07T00:00:00Z", []int{2, 0, 1, 1}},
		{1, 1, "2026-11-08T00:00:00Z", []int{1, 1, 0, 0}},
		{2, 1, "2026-11-09T00:00:00Z", []int{1, 0, 0, 0}},
	}
	for i, expected := range want {
		row := result.Rows[i]
		if row["cohort_index"] != expected.index || row["cohort_start"] != expected.start || row["cohort_size"] != expected.size {
			t.Fatalf("cohort %d header mismatch: %v", i, row)
		}
		retained, ok := row["retained"].([]int)
		if !ok || len(retained) != 4 {
			t.Fatalf("cohort %d retained shape wrong: %#v", i, row["retained"])
		}
		for offset, expectedCount := range expected.retained {
			if retained[offset] != expectedCount {
				t.Fatalf("cohort %d offset %d retained=%d want %d", i, offset, retained[offset], expectedCount)
			}
		}
		rates, ok := row["retention_rate"].([]any)
		if !ok || len(rates) != 4 {
			t.Fatalf("cohort %d rate shape wrong: %#v", i, row["retention_rate"])
		}
		first, ok := rates[0].(float64)
		if !ok || first != 1 {
			t.Fatalf("entry-period retention must be 1.0: %v", rates[0])
		}
	}

	// Anonymous subjects use the anonymous_id column and must never read uid.
	anonymousRows := []map[string]any{
		fact("d1-start", "sign_up", "", "device-one", "source-one", 0, map[string]any{}),
		fact("d1-return", "login", "", "device-one", "source-one", 0, map[string]any{}),
		fact("d2-start", "sign_up", "", "device-two", "source-one", 0, map[string]any{}),
		fact("d2-return", "login", "", "device-two", "source-one", 0, map[string]any{}),
	}
	dayStamp := func(offset int) string { return day(7+offset, 9).Format("2006-01-02 15:04:05.000") }
	anonymousRows[0]["event_time"] = dayStamp(0)
	anonymousRows[0]["occurred_at"] = dayStamp(0)
	anonymousRows[1]["event_time"] = dayStamp(1)
	anonymousRows[1]["occurred_at"] = dayStamp(1)
	anonymousRows[2]["event_time"] = dayStamp(0)
	anonymousRows[2]["occurred_at"] = dayStamp(0)
	anonymousRows[3]["event_time"] = dayStamp(2)
	anonymousRows[3]["occurred_at"] = dayStamp(2)
	seedRows(t, client, anonymousRows)

	anonymousRequest := r
	anonymousRequest.Subject = SubjectAnonymous
	anonymousResult := execute(t, client, anonymousRequest)
	if len(anonymousResult.Rows) != 1 {
		t.Fatalf("anonymous subjects form one shared day-0 cohort: %v", anonymousResult.Rows)
	}
	anonymousRow := anonymousResult.Rows[0]
	if anonymousRow["cohort_size"] != 2 {
		t.Fatalf("both devices must enter the anonymous cohort: %v", anonymousRow)
	}
	anonymousRetained, ok := anonymousRow["retained"].([]int)
	if !ok || len(anonymousRetained) != 4 || anonymousRetained[0] != 2 || anonymousRetained[1] != 1 || anonymousRetained[2] != 1 {
		t.Fatalf("anonymous retained matrix wrong: %#v", anonymousRow["retained"])
	}
}

// TestClickHouseRetentionMonthCohorts proves calendar-month cohorts against
// the real migrated view: first-of-month alignment, variable month length and
// cross-year month offsets, with the same bounded-pull and flag decoding as the
// day/week matrices.
func TestClickHouseRetentionMonthCohorts(t *testing.T) {
	client := clickHouseDatabase(t)
	at := func(year int, month time.Month, day, hour int) time.Time {
		return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	}
	rows := []map[string]any{
		retentionFact("m-u1-start", "sign_up", "u1", at(2026, time.November, 5, 9)),
		retentionFact("m-u1-return", "login", "u1", at(2026, time.December, 10, 9)),
		retentionFact("m-u2-start", "sign_up", "u2", at(2026, time.November, 20, 10)),
		retentionFact("m-u2-return", "login", "u2", at(2027, time.January, 15, 9)),
		retentionFact("m-u3-start", "sign_up", "u3", at(2026, time.December, 8, 9)),
		retentionFact("m-u3-return", "login", "u3", at(2027, time.January, 2, 9)),
		retentionFact("m-u4-start", "sign_up", "u4", at(2027, time.January, 10, 9)),
	}
	outsider := retentionFact("m-outsider", "sign_up", "outsider", at(2026, time.November, 7, 9))
	outsider["project_id"] = "project-two"
	seedRows(t, client, append(rows, outsider))

	r := Request{
		Kind: "retention", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: at(2026, time.November, 1, 0), To: at(2027, time.February, 1, 0)},
		Timezone:  "UTC",
		Retention: &Retention{
			StartEvent:  Step{EventKey: "sign_up", SchemaVersion: 1},
			ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: "month", Periods: 3,
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 5 || result.Metadata.Accuracy != AccuracyExact || result.Metadata.DataWatermark == nil {
		t.Fatalf("month retention result contract wrong: %+v metadata=%+v", result.Columns, result.Metadata)
	}
	want := []struct {
		index, size int
		start       string
		retained    []int
	}{
		{0, 2, "2026-11-01T00:00:00Z", []int{2, 1, 1}},
		{1, 1, "2026-12-01T00:00:00Z", []int{1, 1, 0}},
		{2, 1, "2027-01-01T00:00:00Z", []int{1, 0, 0}},
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("want %d month cohorts, got %d: %v", len(want), len(result.Rows), result.Rows)
	}
	for i, expected := range want {
		row := result.Rows[i]
		if row["cohort_index"] != expected.index || row["cohort_start"] != expected.start || row["cohort_size"] != expected.size {
			t.Fatalf("month cohort %d header mismatch: %v", i, row)
		}
		retained, ok := row["retained"].([]int)
		if !ok || len(retained) != 3 {
			t.Fatalf("month cohort %d retained shape wrong: %#v", i, row["retained"])
		}
		for offset, expectedCount := range expected.retained {
			if retained[offset] != expectedCount {
				t.Fatalf("month cohort %d offset %d retained=%d want %d", i, offset, retained[offset], expectedCount)
			}
		}
		rates, ok := row["retention_rate"].([]any)
		if !ok || len(rates) != 3 {
			t.Fatalf("month cohort %d rate shape wrong: %#v", i, row["retention_rate"])
		}
		if first, ok := rates[0].(float64); !ok || first != 1 {
			t.Fatalf("entry-period month retention must be 1.0: %v", rates[0])
		}
	}
}

// TestClickHouseSegmentation proves subject-level segmentation against the
// real migrated view: one group per subject via the earliest event value,
// an explicit missing group, shares, and project scoping.
func TestClickHouseSegmentation(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(hour int) time.Time { return day.Add(time.Duration(hour) * time.Hour) }
	rows := []map[string]any{
		// u1's earliest plan is pro; a later enterprise value must not reassign it.
		fact("u1-first", "sign_up", "u1", "", "source-one", 0, map[string]any{"plan": "pro"}),
		fact("u1-later", "sign_up", "u1", "", "source-one", 0, map[string]any{"plan": "enterprise"}),
		fact("u2", "sign_up", "u2", "", "source-one", 0, map[string]any{"plan": "pro"}),
		fact("u3", "sign_up", "u3", "", "source-one", 0, map[string]any{"plan": "enterprise"}),
		// u4 has no plan property and joins the missing group.
		fact("u4", "sign_up", "u4", "", "source-one", 0, map[string]any{}),
		// Unrelated project must be invisible.
		fact("outsider", "sign_up", "outsider", "", "source-one", 0, map[string]any{"plan": "pro"}),
	}
	rows[0]["event_time"] = at(9).Format("2006-01-02 15:04:05.000")
	rows[0]["occurred_at"] = rows[0]["event_time"]
	rows[1]["event_time"] = at(15).Format("2006-01-02 15:04:05.000")
	rows[1]["occurred_at"] = rows[1]["event_time"]
	rows[2]["event_time"] = at(10).Format("2006-01-02 15:04:05.000")
	rows[2]["occurred_at"] = rows[2]["event_time"]
	rows[3]["event_time"] = at(11).Format("2006-01-02 15:04:05.000")
	rows[3]["occurred_at"] = rows[3]["event_time"]
	rows[4]["event_time"] = at(12).Format("2006-01-02 15:04:05.000")
	rows[4]["occurred_at"] = rows[4]["event_time"]
	rows[5]["project_id"] = "project-two"
	seedRows(t, client, rows)

	r := Request{
		Kind: "segment", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Segment: &Segment{
			Event: Step{EventKey: "sign_up", SchemaVersion: 1},
			Field: Field{Kind: "property", EventKey: "sign_up", SchemaVersion: 1, Key: "plan", ResolvedType: "string"},
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 3 || result.Metadata.Accuracy != AccuracyExact {
		t.Fatalf("segment result contract wrong: %+v", result.Columns)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("want pro, enterprise, missing groups, got %v", result.Rows)
	}
	want := []struct {
		value    string
		subjects int
		share    float64
	}{
		{"pro", 2, 0.5},
		{"__missing__", 1, 0.25},
		{"enterprise", 1, 0.25},
	}
	for i, expected := range want {
		row := result.Rows[i]
		if row["segment_value"] != expected.value || row["subjects"] != expected.subjects {
			t.Fatalf("group %d mismatch: %v", i, row)
		}
		share, ok := row["share"].(float64)
		if !ok || share != expected.share {
			t.Fatalf("group %d share=%v want %v", i, row["share"], expected.share)
		}
	}
}

// TestClickHousePathAggregation proves ordered event-path counting against the
// real migrated view: distinct sequences, subject counts, pinned-start
// behavior and project scoping.
func TestClickHousePathAggregation(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		// u1 and u2 share open -> view -> buy.
		fact("u1-open", "open", "u1", "", "source-one", 0, map[string]any{}),
		fact("u1-view", "view", "u1", "", "source-one", 0, map[string]any{}),
		fact("u1-buy", "buy", "u1", "", "source-one", 0, map[string]any{}),
		fact("u2-open", "open", "u2", "", "source-one", 0, map[string]any{}),
		fact("u2-view", "view", "u2", "", "source-one", 0, map[string]any{}),
		fact("u2-buy", "buy", "u2", "", "source-one", 0, map[string]any{}),
		// u3 follows open -> view -> share.
		fact("u3-open", "open", "u3", "", "source-one", 0, map[string]any{}),
		fact("u3-view", "view", "u3", "", "source-one", 0, map[string]any{}),
		fact("u3-share", "share", "u3", "", "source-one", 0, map[string]any{}),
		// u4 has only two events and cannot fill a depth-3 path.
		fact("u4-open", "open", "u4", "", "source-one", 0, map[string]any{}),
		fact("u4-view", "view", "u4", "", "source-one", 0, map[string]any{}),
		// Unrelated project must be invisible.
		fact("outsider-open", "open", "outsider", "", "source-one", 0, map[string]any{}),
		fact("outsider-view", "view", "outsider", "", "source-one", 0, map[string]any{}),
		fact("outsider-buy", "buy", "outsider", "", "source-one", 0, map[string]any{}),
	}
	seconds := map[string]int{
		"u1-open": 10, "u1-view": 20, "u1-buy": 30,
		"u2-open": 11, "u2-view": 21, "u2-buy": 31,
		"u3-open": 12, "u3-view": 22, "u3-share": 32,
		"u4-open": 13, "u4-view": 23,
		"outsider-open": 14, "outsider-view": 24, "outsider-buy": 34,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	rows[len(rows)-1]["project_id"] = "project-two"
	rows[len(rows)-2]["project_id"] = "project-two"
	rows[len(rows)-3]["project_id"] = "project-two"
	seedRows(t, client, rows)

	r := Request{
		Kind: "path", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Path:      &Path{Depth: 3},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 2 || result.Metadata.Accuracy != AccuracyExact {
		t.Fatalf("path result contract wrong: %+v", result.Columns)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("want two distinct depth-3 paths, got %v", result.Rows)
	}
	want := []struct {
		path     []string
		subjects int
	}{
		{[]string{"open", "view", "buy"}, 2},
		{[]string{"open", "view", "share"}, 1},
	}
	for i, expected := range want {
		steps, ok := result.Rows[i]["path"].([]string)
		if !ok || strings.Join(steps, ",") != strings.Join(expected.path, ",") {
			t.Fatalf("path %d mismatch: %v", i, result.Rows[i])
		}
		if result.Rows[i]["subjects"] != expected.subjects {
			t.Fatalf("path %d subjects=%v want %d", i, result.Rows[i]["subjects"], expected.subjects)
		}
	}

	// A pinned start at view drops the leading open and returns depth-2 tails.
	pinned := r
	pinned.Path = &Path{Depth: 2, StartEvent: &Step{EventKey: "view", SchemaVersion: 1}}
	pinnedResult := execute(t, client, pinned)
	if len(pinnedResult.Rows) != 2 {
		t.Fatalf("pinned paths want two groups, got %v", pinnedResult.Rows)
	}
	firstSteps, _ := pinnedResult.Rows[0]["path"].([]string)
	if firstSteps[0] != "view" {
		t.Fatalf("pinned paths must begin at the view event: %v", pinnedResult.Rows)
	}
}

// TestClickHouseAttribution proves last-touch credit against the real
// migrated view: the nearest in-window touchpoint wins, conversions without a
// touchpoint fall into the direct bucket, non-converters are excluded and
// project scoping is enforced.
func TestClickHouseAttribution(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		// u1: email_open is the last touchpoint before buy.
		fact("u1-ad", "ad_click", "u1", "", "source-one", 10, map[string]any{}),
		fact("u1-email", "email_open", "u1", "", "source-one", 20, map[string]any{}),
		fact("u1-buy", "buy", "u1", "", "source-one", 30, map[string]any{}),
		// u2: only ad_click precedes the conversion.
		fact("u2-ad", "ad_click", "u2", "", "source-one", 15, map[string]any{}),
		fact("u2-buy", "buy", "u2", "", "source-one", 25, map[string]any{}),
		// u3 converts without any touchpoint.
		fact("u3-buy", "buy", "u3", "", "source-one", 40, map[string]any{}),
		// u4 never converts and must be excluded.
		fact("u4-ad", "ad_click", "u4", "", "source-one", 50, map[string]any{}),
		// Unrelated project must stay invisible.
		fact("outsider-ad", "ad_click", "outsider", "", "source-one", 12, map[string]any{}),
		fact("outsider-buy", "buy", "outsider", "", "source-one", 22, map[string]any{}),
	}
	seconds := map[string]int{
		"u1-ad": 10, "u1-email": 20, "u1-buy": 30,
		"u2-ad": 15, "u2-buy": 25,
		"u3-buy": 40, "u4-ad": 50,
		"outsider-ad": 12, "outsider-buy": 22,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	for _, id := range []string{"outsider-ad", "outsider-buy"} {
		for _, row := range rows {
			if row["event_id"] == id {
				row["project_id"] = "project-two"
			}
		}
	}
	seedRows(t, client, rows)

	r := Request{
		Kind: "attribution", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Attribution: &Attribution{
			Conversion:    Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints:   []Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 1}},
			WindowSeconds: 86400,
			Model:         AttributionModelLastTouch,
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 3 || result.Metadata.Accuracy != AccuracyExact {
		t.Fatalf("attribution result contract wrong: %+v", result.Columns)
	}
	want := []struct {
		touchpoint string
		subjects   int
	}{
		{"__direct__", 1},
		{"ad_click", 1},
		{"email_open", 1},
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("want %d credit rows, got %v", len(want), result.Rows)
	}
	for i, expected := range want {
		row := result.Rows[i]
		if row["touchpoint"] != expected.touchpoint || row["subjects"] != expected.subjects {
			t.Fatalf("credit %d mismatch: %v", i, row)
		}
		share, _ := row["share"].(float64)
		if share != 1.0/3.0 {
			t.Fatalf("credit %d share=%v want 1/3", i, share)
		}
	}

	// A five-second lookback removes every touchpoint (all gaps are 10s), so
	// all three converters collapse into the direct bucket.
	tight := r
	tight.Attribution = &Attribution{
		Conversion:    Step{EventKey: "buy", SchemaVersion: 1},
		Touchpoints:   []Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 1}},
		WindowSeconds: 5,
		Model:         AttributionModelLastTouch,
	}
	tightResult := execute(t, client, tight)
	if len(tightResult.Rows) != 1 || tightResult.Rows[0]["touchpoint"] != directAttributionBucket || tightResult.Rows[0]["subjects"] != 3 {
		t.Fatalf("tight window must credit every converter as direct: %v", tightResult.Rows)
	}
}

// TestClickHouseFirstTouchAttribution proves the model enum switches the
// credit direction against the real migrated view: with the same evidence as
// the last-touch test, the earliest in-window touchpoint wins and the later
// email_open bucket disappears.
func TestClickHouseFirstTouchAttribution(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		// u1 sees two touchpoints; first-touch credits the earlier ad_click.
		fact("ft-u1-ad", "ad_click", "u1", "", "source-one", 10, map[string]any{}),
		fact("ft-u1-email", "email_open", "u1", "", "source-one", 20, map[string]any{}),
		fact("ft-u1-buy", "buy", "u1", "", "source-one", 30, map[string]any{}),
		// u2 has one touchpoint, identical under both models.
		fact("ft-u2-ad", "ad_click", "u2", "", "source-one", 15, map[string]any{}),
		fact("ft-u2-buy", "buy", "u2", "", "source-one", 25, map[string]any{}),
		// u3 converts without any touchpoint and stays in the direct bucket.
		fact("ft-u3-buy", "buy", "u3", "", "source-one", 40, map[string]any{}),
	}
	seconds := map[string]int{
		"ft-u1-ad": 10, "ft-u1-email": 20, "ft-u1-buy": 30,
		"ft-u2-ad": 15, "ft-u2-buy": 25, "ft-u3-buy": 40,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	seedRows(t, client, rows)

	r := Request{
		Kind: "attribution", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Attribution: &Attribution{
			Conversion:    Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints:   []Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 1}},
			WindowSeconds: 86400,
			Model:         AttributionModelFirstTouch,
		},
	}
	result := execute(t, client, r)
	want := []struct {
		touchpoint string
		subjects   int
		share      float64
	}{
		{"ad_click", 2, 2.0 / 3.0},
		{"__direct__", 1, 1.0 / 3.0},
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("first-touch wants %d rows (no email_open bucket), got %v", len(want), result.Rows)
	}
	for i, expected := range want {
		row := result.Rows[i]
		if row["touchpoint"] != expected.touchpoint || row["subjects"] != expected.subjects {
			t.Fatalf("first-touch credit %d mismatch: %v", i, row)
		}
		share, _ := row["share"].(float64)
		if share != expected.share {
			t.Fatalf("first-touch credit %d share=%v want %v", i, share, expected.share)
		}
	}
}

// TestClickHouseLinearAttribution proves equal-weight multi-touch credit
// against the real migrated view: each converting subject splits unit weight
// across its distinct in-window touchpoints, and the direct bucket keeps full
// weight for a subject with no touchpoint.
func TestClickHouseLinearAttribution(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		// u1 touches two distinct events -> each gets weight 1/2.
		fact("ln-u1-ad", "ad_click", "u1", "", "source-one", 10, map[string]any{}),
		fact("ln-u1-email", "email_open", "u1", "", "source-one", 20, map[string]any{}),
		fact("ln-u1-buy", "buy", "u1", "", "source-one", 30, map[string]any{}),
		// u2 touches only ad_click -> it carries full weight.
		fact("ln-u2-ad", "ad_click", "u2", "", "source-one", 15, map[string]any{}),
		fact("ln-u2-buy", "buy", "u2", "", "source-one", 25, map[string]any{}),
		// u3 converts without a touchpoint -> direct bucket full weight.
		fact("ln-u3-buy", "buy", "u3", "", "source-one", 40, map[string]any{}),
	}
	seconds := map[string]int{
		"ln-u1-ad": 10, "ln-u1-email": 20, "ln-u1-buy": 30,
		"ln-u2-ad": 15, "ln-u2-buy": 25, "ln-u3-buy": 40,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	seedRows(t, client, rows)

	r := Request{
		Kind: "attribution", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Attribution: &Attribution{
			Conversion:    Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints:   []Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 1}},
			WindowSeconds: 86400,
			Model:         AttributionModelLinear,
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 4 {
		t.Fatalf("linear result must expose four columns including credit: %+v", result.Columns)
	}
	want := map[string]struct {
		subjects int
		credit   float64
	}{
		"ad_click":              {2, 1.5},
		directAttributionBucket: {1, 1.0},
		"email_open":            {1, 0.5},
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("linear wants %d rows, got %v", len(want), result.Rows)
	}
	shareSum := 0.0
	for _, row := range result.Rows {
		key, _ := row["touchpoint"].(string)
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected touchpoint %q: %v", key, row)
		}
		if row["subjects"] != expected.subjects {
			t.Fatalf("%s subjects=%v want %d", key, row["subjects"], expected.subjects)
		}
		credit, _ := row["credit"].(float64)
		if credit != expected.credit {
			t.Fatalf("%s credit=%v want %v", key, credit, expected.credit)
		}
		share, _ := row["share"].(float64)
		shareSum += share
	}
	if diff := shareSum - 1.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("linear shares must sum to 1, got %v", shareSum)
	}
}

// TestClickHouseTimeDecayAttribution proves exponential half-life weighting
// against the real migrated view: closer touchpoints outrank older ones, the
// per-subject weights normalize to one, and a subject without an in-window
// touchpoint stays in the direct bucket.
func TestClickHouseTimeDecayAttribution(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		// u1: two touchpoints; with a 10s half-life the nearer email_open wins.
		fact("td-u1-ad", "ad_click", "u1", "", "source-one", 10, map[string]any{}),
		fact("td-u1-email", "email_open", "u1", "", "source-one", 20, map[string]any{}),
		fact("td-u1-buy", "buy", "u1", "", "source-one", 30, map[string]any{}),
		// u2 converts without a touchpoint and keeps full direct weight.
		fact("td-u2-buy", "buy", "u2", "", "source-one", 40, map[string]any{}),
	}
	seconds := map[string]int{
		"td-u1-ad": 10, "td-u1-email": 20, "td-u1-buy": 30, "td-u2-buy": 40,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	seedRows(t, client, rows)

	r := Request{
		Kind: "attribution", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Attribution: &Attribution{
			Conversion:           Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints:          []Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 1}},
			WindowSeconds:        86400,
			Model:                AttributionModelTimeDecay,
			DecayHalfLifeSeconds: 10,
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 4 {
		t.Fatalf("time_decay result must expose four columns: %+v", result.Columns)
	}
	got := map[string]float64{}
	shareSum := 0.0
	for _, row := range result.Rows {
		key, _ := row["touchpoint"].(string)
		credit, _ := row["credit"].(float64)
		share, _ := row["share"].(float64)
		got[key] = credit
		shareSum += share
	}
	// u1 raw weights 0.25 (ad) and 0.5 (email) normalize to 1/3 and 2/3;
	// u2 adds a full direct weight.
	wantAd, wantEmail, wantDirect := 1.0/3.0, 2.0/3.0, 1.0
	if diff := got["ad_click"] - wantAd; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("ad_click credit=%v want %v", got["ad_click"], wantAd)
	}
	if diff := got["email_open"] - wantEmail; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("email_open credit=%v want %v", got["email_open"], wantEmail)
	}
	if diff := got[directAttributionBucket] - wantDirect; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("direct credit=%v want %v", got[directAttributionBucket], wantDirect)
	}
	if got["email_open"] <= got["ad_click"] {
		t.Fatalf("closer touchpoint must outrank older one: %v", got)
	}
	if diff := shareSum - 1.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("time_decay shares must sum to 1, got %v", shareSum)
	}
}

// TestClickHousePositionBasedAttribution proves U-shaped 0.4/0.2/0.4 weighting
// against the real migrated view: the first and last distinct touchpoints
// anchor each journey, two touchpoints split evenly, a lone touchpoint keeps
// full weight, and a converter without a touchpoint stays in the direct
// bucket. Per-subject weights total one and shares sum to one across rows.
func TestClickHousePositionBasedAttribution(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		// u1 touches three distinct events -> 0.4 / 0.2 / 0.4.
		fact("pb-u1-ad", "ad_click", "u1", "", "source-one", 10, map[string]any{}),
		fact("pb-u1-email", "email_open", "u1", "", "source-one", 20, map[string]any{}),
		fact("pb-u1-social", "social_click", "u1", "", "source-one", 30, map[string]any{}),
		fact("pb-u1-buy", "buy", "u1", "", "source-one", 40, map[string]any{}),
		// u2 touches two distinct events -> 0.5 / 0.5.
		fact("pb-u2-ad", "ad_click", "u2", "", "source-one", 10, map[string]any{}),
		fact("pb-u2-email", "email_open", "u2", "", "source-one", 20, map[string]any{}),
		fact("pb-u2-buy", "buy", "u2", "", "source-one", 30, map[string]any{}),
		// u3 touches one event -> full weight.
		fact("pb-u3-ad", "ad_click", "u3", "", "source-one", 15, map[string]any{}),
		fact("pb-u3-buy", "buy", "u3", "", "source-one", 25, map[string]any{}),
		// u4 converts without a touchpoint -> direct bucket full weight.
		fact("pb-u4-buy", "buy", "u4", "", "source-one", 50, map[string]any{}),
	}
	seconds := map[string]int{
		"pb-u1-ad": 10, "pb-u1-email": 20, "pb-u1-social": 30, "pb-u1-buy": 40,
		"pb-u2-ad": 10, "pb-u2-email": 20, "pb-u2-buy": 30,
		"pb-u3-ad": 15, "pb-u3-buy": 25, "pb-u4-buy": 50,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	seedRows(t, client, rows)

	r := Request{
		Kind: "attribution", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Attribution: &Attribution{
			Conversion: Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints: []Step{
				{EventKey: "ad_click", SchemaVersion: 1},
				{EventKey: "email_open", SchemaVersion: 1},
				{EventKey: "social_click", SchemaVersion: 1},
			},
			WindowSeconds: 86400,
			Model:         AttributionModelPositionBased,
		},
	}
	result := execute(t, client, r)
	if len(result.Columns) != 4 {
		t.Fatalf("position_based result must expose four columns including credit: %+v", result.Columns)
	}
	want := map[string]struct {
		subjects int
		credit   float64
	}{
		"ad_click":              {3, 1.9},
		"email_open":            {2, 0.7},
		"social_click":          {1, 0.4},
		directAttributionBucket: {1, 1.0},
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("position_based wants %d rows, got %v", len(want), result.Rows)
	}
	shareSum := 0.0
	for _, row := range result.Rows {
		key, _ := row["touchpoint"].(string)
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected touchpoint %q: %v", key, row)
		}
		if row["subjects"] != expected.subjects {
			t.Fatalf("%s subjects=%v want %d", key, row["subjects"], expected.subjects)
		}
		credit, _ := row["credit"].(float64)
		if diff := credit - expected.credit; diff > 1e-12 || diff < -1e-12 {
			t.Fatalf("%s credit=%v want %v", key, credit, expected.credit)
		}
		share, _ := row["share"].(float64)
		shareSum += share
	}
	if diff := shareSum - 1.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("position_based shares must sum to 1, got %v", shareSum)
	}
}

// TestClickHousePathCollapseRepeats proves the opt-in loop compression
// against the real migrated view: adjacent identical events fold into one
// node before depth extraction, while the default request stays byte-stable.
func TestClickHousePathCollapseRepeats(t *testing.T) {
	client := clickHouseDatabase(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return day.Add(time.Duration(second) * time.Second) }
	rows := []map[string]any{
		fact("u1-open-1", "open", "u1", "", "source-one", 0, map[string]any{}),
		fact("u1-open-2", "open", "u1", "", "source-one", 0, map[string]any{}),
		fact("u1-view", "view", "u1", "", "source-one", 0, map[string]any{}),
		fact("u1-buy", "buy", "u1", "", "source-one", 0, map[string]any{}),
	}
	seconds := map[string]int{
		"u1-open-1": 10, "u1-open-2": 20, "u1-view": 30, "u1-buy": 40,
	}
	for _, row := range rows {
		id := row["event_id"].(string)
		stamp := at(seconds[id]).Format("2006-01-02 15:04:05.000")
		row["event_time"] = stamp
		row["occurred_at"] = stamp
	}
	seedRows(t, client, rows)

	base := Request{
		Kind: "path", Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: day, To: day.Add(24 * time.Hour)},
		Timezone:  "UTC",
		Path:      &Path{Depth: 3},
	}

	// The default request preserves the adjacent duplicate open.
	raw := execute(t, client, base)
	if len(raw.Rows) != 1 {
		t.Fatalf("raw path wants one group, got %v", raw.Rows)
	}
	rawSteps, _ := raw.Rows[0]["path"].([]string)
	if strings.Join(rawSteps, ",") != "open,open,view" {
		t.Fatalf("raw path must keep repeats: %v", rawSteps)
	}

	// Opt-in folding produces the three distinct consecutive nodes.
	collapsed := base
	collapsed.Path = &Path{Depth: 3, CollapseRepeats: true}
	folded := execute(t, client, collapsed)
	if len(folded.Rows) != 1 {
		t.Fatalf("folded path wants one group, got %v", folded.Rows)
	}
	foldedSteps, _ := folded.Rows[0]["path"].([]string)
	if strings.Join(foldedSteps, ",") != "open,view,buy" {
		t.Fatalf("folded path mismatch: %v", foldedSteps)
	}
}
