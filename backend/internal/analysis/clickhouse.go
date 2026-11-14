package analysis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

type ClickHouseConfig struct {
	URL, Username, Password, Database, Table string
	Client                                   *http.Client
	// Catalog optionally enables dedicated hot-key reads; nil means disabled.
	Catalog *propertycatalog.Catalog
}
type ClickHouse struct {
	endpoint                            *url.URL
	username, password, database, table string
	client                              *http.Client
	catalog                             *propertycatalog.Catalog
}

func NewClickHouse(cfg ClickHouseConfig) (*ClickHouse, error) {
	endpoint, e := url.Parse(cfg.URL)
	if e != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != "" {
		return nil, fmt.Errorf("invalid ClickHouse endpoint")
	}
	if cfg.Table == "" {
		cfg.Table = "event_facts"
	}
	if !identifier.MatchString(cfg.Database) || cfg.Table != "event_facts" {
		return nil, fmt.Errorf("invalid ClickHouse query database or view")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 8, ResponseHeaderTimeout: 32 * time.Second, IdleConnTimeout: time.Minute}}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout <= 0 || copyClient.Timeout > 35*time.Second {
		copyClient.Timeout = 35 * time.Second
	}
	client = &copyClient
	catalog := cfg.Catalog
	if catalog == nil {
		catalog = propertycatalog.Disabled()
	}
	return &ClickHouse{endpoint: endpoint, username: cfg.Username, password: cfg.Password, database: cfg.Database, table: cfg.Table, client: client, catalog: catalog}, nil
}
func (c *ClickHouse) request(ctx context.Context, sql, queryID string, parameters map[string]string, execution bool) (*http.Response, error) {
	endpoint := *c.endpoint
	values := endpoint.Query()
	values.Set("database", c.database)
	values.Set("query_id", queryID)
	for key, value := range parameters {
		values.Set("param_"+key, value)
	}
	if execution {
		for key, value := range map[string]string{"readonly": "2", "max_execution_time": "30", "timeout_overflow_mode": "throw", "max_rows_to_read": "2000000", "max_bytes_to_read": "268435456", "read_overflow_mode": "throw", "max_memory_usage": "536870912", "max_threads": "2", "max_result_rows": "100001", "max_result_bytes": "4194304", "result_overflow_mode": "throw", "output_format_json_quote_64bit_integers": "0", "output_format_json_quote_denormals": "0", "cancel_http_readonly_queries_on_client_close": "1"} {
			values.Set(key, value)
		}
	}
	endpoint.RawQuery = values.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(sql))
	if e != nil {
		return nil, e
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	return c.client.Do(req)
}
func (c *ClickHouse) kill(queryID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, e := c.request(ctx, "KILL QUERY WHERE query_id={target:String} ASYNC", "evia-kill-"+ids.New(), map[string]string{"target": queryID}, false)
	if e == nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
	}
}
func (c *ClickHouse) Execute(ctx context.Context, scope Scope, r Request) (Result, error) {
	plan, e := CompileWithCatalog(c.database, c.table, scope, r, c.catalog)
	if e != nil {
		return Result{}, e
	}
	executionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	queryID := "evia-analysis-" + ids.New()
	response, e := c.request(executionCtx, plan.SQL+" FORMAT JSONEachRow", queryID, plan.Parameters, true)
	if e != nil {
		if executionCtx.Err() != nil {
			c.kill(queryID)
			return Result{}, fault.New(fault.Unavailable, "QUERY_TIMEOUT", "查询执行已取消或超时")
		}
		return Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析存储不可用")
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if e != nil {
		if executionCtx.Err() != nil {
			c.kill(queryID)
		}
		return Result{}, fault.New(fault.Unavailable, "QUERY_READ_FAILED", "读取查询结果失败")
	}
	if len(body) > 4*1024*1024 {
		return Result{}, Invalid("QUERY_RESULT_LIMIT_EXCEEDED", "查询结果超过字节预算")
	}
	if response.StatusCode != http.StatusOK {
		return Result{}, clickHouseFailure(body)
	}
	rows := make([]map[string]any, 0)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 8192), 4*1024*1024)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var row map[string]any
		if e = decoder.Decode(&row); e != nil {
			return Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "分析存储返回无效结果")
		}
		rows = append(rows, row)
	}
	if e = scanner.Err(); e != nil {
		return Result{}, e
	}
	if e = executionCtx.Err(); e != nil {
		c.kill(queryID)
		return Result{}, e
	}
	metadata := Metadata{Accuracy: plan.Accuracy, SampleRate: 1, Coverage: Coverage{TimeRange: r.TimeRange, State: "provisional", KnownGaps: []string{"pipeline_completeness_not_established", "identity_resolution_not_applied"}}, GeneratedAt: time.Now().UTC()}
	result := Result{Columns: plan.Columns, Rows: make([]map[string]any, 0), Metadata: metadata}
	for _, row := range rows {
		key := "received_at_ns"
		if r.Kind == "trend" {
			key = "watermark_ns"
		}
		if v, ok := integer(row[key]); ok {
			value := time.Unix(0, v).UTC()
			if result.Metadata.DataWatermark == nil || value.After(*result.Metadata.DataWatermark) {
				result.Metadata.DataWatermark = &value
			}
		}
	}
	if r.Kind == "funnel" {
		if len(rows) > 100000 {
			return Result{}, Invalid("QUERY_COST_EXCEEDED", "漏斗明细超过执行预算，请缩小范围")
		}
		result.Rows, e = evaluateFunnel(executionCtx, r, rows)
		if e != nil {
			return Result{}, e
		}
		return result, nil
	}
	if r.Kind == "retention" {
		if len(rows) > 100000 {
			return Result{}, Invalid("QUERY_COST_EXCEEDED", "留存明细超过执行预算，请缩小范围")
		}
		result.Rows, e = evaluateRetention(executionCtx, r, rows)
		if e != nil {
			return Result{}, e
		}
		return result, nil
	}
	if r.Kind == "segment" {
		if len(rows) > 100000 {
			return Result{}, Invalid("QUERY_COST_EXCEEDED", "分群明细超过执行预算，请缩小范围")
		}
		result.Rows, e = evaluateSegment(executionCtx, r, rows)
		if e != nil {
			return Result{}, e
		}
		return result, nil
	}
	if r.Kind == "path" {
		if len(rows) > 100000 {
			return Result{}, Invalid("QUERY_COST_EXCEEDED", "路径明细超过执行预算，请缩小范围")
		}
		result.Rows, e = evaluatePath(executionCtx, r, rows)
		if e != nil {
			return Result{}, e
		}
		return result, nil
	}
	if r.Kind == "attribution" {
		if len(rows) > 100000 {
			return Result{}, Invalid("QUERY_COST_EXCEEDED", "归因明细超过执行预算，请缩小范围")
		}
		result.Rows, e = evaluateAttribution(executionCtx, r, rows)
		if e != nil {
			return Result{}, e
		}
		return result, nil
	}
	if len(rows) > r.Limit {
		return Result{}, Invalid("QUERY_RESULT_LIMIT_EXCEEDED", "结果超过行数限制，请缩小范围")
	}
	for _, row := range rows {
		if r.Kind == "trend" {
			delete(row, "watermark_ns")
			if v, ok := integer(row["bucket_ms"]); ok {
				row["bucket"] = time.UnixMilli(v).UTC().Format(time.RFC3339Nano)
				delete(row, "bucket_ms")
			}
		} else {
			when, ok := integer(row["event_time_ns"])
			if !ok {
				return Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "事件时间无效")
			}
			row["event_time"] = time.Unix(0, when).UTC().Format(time.RFC3339Nano)
			delete(row, "event_time_ns")
			delete(row, "received_at_ns")
			for _, key := range []string{"context", "properties"} {
				raw, ok := row[key+"_json"].(string)
				if !ok {
					return Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "事件属性无效")
				}
				decoder := json.NewDecoder(strings.NewReader(raw))
				decoder.UseNumber()
				var object map[string]any
				if e = decoder.Decode(&object); e != nil {
					return Result{}, e
				}
				row[key] = object
				delete(row, key+"_json")
			}
			row["original_uid"] = row["uid"]
		}
		for _, column := range plan.Columns {
			if column.Type == "boolean" {
				if number, ok := integer(row[column.Key]); ok {
					row[column.Key] = number != 0
				}
			}
		}
		if e = checkResultNumbers(row); e != nil {
			return Result{}, e
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}
func clickHouseFailure(body []byte) error {
	message := string(body)
	if strings.Contains(message, "TIMEOUT_EXCEEDED") || strings.Contains(message, "TOO_SLOW") {
		return fault.New(fault.Unavailable, "QUERY_TIMEOUT", "查询超过执行时间预算")
	}
	if strings.Contains(message, "LIMIT_EXCEEDED") || strings.Contains(message, "TOO_MANY_ROWS_OR_BYTES") || strings.Contains(message, "TOO_MANY_BYTES") || strings.Contains(message, "TOO_MANY_ROWS") || strings.Contains(message, "MEMORY_LIMIT") {
		return Invalid("QUERY_COST_EXCEEDED", "查询超过执行资源预算")
	}
	return fault.New(fault.Unavailable, "QUERY_EXECUTION_FAILED", "分析存储无法完成查询")
}
func integer(value any) (int64, bool) {
	switch n := value.(type) {
	case json.Number:
		v, e := n.Int64()
		return v, e == nil
	case float64:
		return int64(n), n == float64(int64(n))
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}
func checkResultNumbers(value any) error {
	switch v := value.(type) {
	case json.Number:
		if _, e := scalarType(v); e != nil {
			return Invalid("QUERY_NUMERIC_OVERFLOW", "查询数值超过 JavaScript 安全范围")
		}
	case map[string]any:
		for _, item := range v {
			if e := checkResultNumbers(item); e != nil {
				return e
			}
		}
	case []any:
		for _, item := range v {
			if e := checkResultNumbers(item); e != nil {
				return e
			}
		}
	}
	return nil
}

var _ Engine = (*ClickHouse)(nil)
