package analysis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/overview"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const overviewSQL = `SELECT kind,bucket_ms,events,sessions,identified_users,anonymous_ids,watermark_ns,event_key FROM (
SELECT 'summary' AS kind,toInt64(0) AS bucket_ms,count() AS events,
 uniqExactIf(session_id,session_id IS NOT NULL) AS sessions,
 uniqExactIf(uid,uid IS NOT NULL) AS identified_users,
 uniqExactIf(anonymous_id,anonymous_id IS NOT NULL) AS anonymous_ids,
 maxOrNull(toUnixTimestamp64Nano(received_at)) AS watermark_ns,'' AS event_key
FROM %s WHERE project_id={project:String} AND environment={environment:String}
 AND event_time>=fromUnixTimestamp64Nano({from_ns:Int64}) AND event_time<fromUnixTimestamp64Nano({to_ns:Int64})
UNION ALL
SELECT 'trend',toUnixTimestamp64Milli(multiIf({interval:String}='hour',toStartOfHour(event_time,'UTC'),toStartOfDay(event_time,'UTC'))) AS bucket_ms,
 count() AS events,uniqExactIf(session_id,session_id IS NOT NULL) AS sessions,
 toUInt64(0),toUInt64(0),maxOrNull(toUnixTimestamp64Nano(received_at)),''
FROM %s WHERE project_id={project:String} AND environment={environment:String}
 AND event_time>=fromUnixTimestamp64Nano({from_ns:Int64}) AND event_time<fromUnixTimestamp64Nano({to_ns:Int64}) GROUP BY bucket_ms
UNION ALL
SELECT 'top',toInt64(0),events,toUInt64(0),toUInt64(0),toUInt64(0),NULL,event_key FROM (
 SELECT event_key,count() AS events FROM %s WHERE project_id={project:String} AND environment={environment:String}
 AND event_time>=fromUnixTimestamp64Nano({from_ns:Int64}) AND event_time<fromUnixTimestamp64Nano({to_ns:Int64})
 GROUP BY event_key ORDER BY events DESC,event_key ASC LIMIT 10)
) FORMAT JSONEachRow`

var _ overview.Reader = (*ClickHouse)(nil)

func overviewCount(value any) (int64, bool) {
	n, ok := integer(value)
	return n, ok && n >= 0 && n <= 9007199254740991
}

func (c *ClickHouse) ReadOverview(ctx context.Context, request overview.Request) (overview.Result, error) {
	if err := overview.Validate(request); err != nil {
		return overview.Result{}, err
	}
	table := "`" + c.database + "`.`" + c.table + "`"
	sql := fmt.Sprintf(overviewSQL, table, table, table)
	params := map[string]string{"project": request.ProjectID, "environment": request.Environment, "from_ns": strconv.FormatInt(request.From.UnixNano(), 10), "to_ns": strconv.FormatInt(request.To.UnixNano(), 10), "interval": request.Interval}
	executionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	queryID := "evia-overview-" + ids.New()
	response, err := c.request(executionCtx, sql, queryID, params, true)
	if err != nil {
		if executionCtx.Err() != nil {
			c.kill(queryID)
			return overview.Result{}, fault.New(fault.Unavailable, "QUERY_TIMEOUT", "概览查询已取消或超时")
		}
		return overview.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析存储不可用")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil {
		return overview.Result{}, fault.New(fault.Unavailable, "QUERY_READ_FAILED", "读取概览结果失败")
	}
	if len(body) > 256*1024 {
		return overview.Result{}, Invalid("QUERY_RESULT_LIMIT_EXCEEDED", "概览结果超过字节预算")
	}
	if response.StatusCode != http.StatusOK {
		return overview.Result{}, clickHouseFailure(body)
	}
	result := overview.Result{Trend: []overview.TrendPoint{}, TopEvents: []overview.TopEvent{}}
	summary := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 256*1024)
	rows := 0
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		rows++
		if rows > 180 {
			return overview.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "概览存储返回过多结果")
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var row map[string]any
		if err = decoder.Decode(&row); err != nil {
			return overview.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "概览存储返回无效结果")
		}
		kind, ok := row["kind"].(string)
		if !ok {
			return overview.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "概览存储返回无效结果")
		}
		switch kind {
		case "summary":
			if summary {
				return overview.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "概览汇总结果重复")
			}
			summary = true
			if result.TotalEvents, ok = overviewCount(row["events"]); !ok {
				return overview.Result{}, invalidOverviewStorage()
			}
			if result.UniqueSessions, ok = overviewCount(row["sessions"]); !ok {
				return overview.Result{}, invalidOverviewStorage()
			}
			if result.IdentifiedUsers, ok = overviewCount(row["identified_users"]); !ok {
				return overview.Result{}, invalidOverviewStorage()
			}
			if result.AnonymousIDs, ok = overviewCount(row["anonymous_ids"]); !ok {
				return overview.Result{}, invalidOverviewStorage()
			}
			if row["watermark_ns"] != nil {
				n, valid := integer(row["watermark_ns"])
				if !valid {
					return overview.Result{}, invalidOverviewStorage()
				}
				value := time.Unix(0, n).UTC()
				result.DataWatermark = &value
			}
		case "trend":
			bucket, valid := integer(row["bucket_ms"])
			if !valid {
				return overview.Result{}, invalidOverviewStorage()
			}
			events, valid := overviewCount(row["events"])
			if !valid {
				return overview.Result{}, invalidOverviewStorage()
			}
			sessions, valid := overviewCount(row["sessions"])
			if !valid {
				return overview.Result{}, invalidOverviewStorage()
			}
			result.Trend = append(result.Trend, overview.TrendPoint{Bucket: time.UnixMilli(bucket).UTC(), Events: events, Sessions: sessions})
		case "top":
			key, valid := row["event_key"].(string)
			if !valid || key == "" || len(key) > 128 {
				return overview.Result{}, invalidOverviewStorage()
			}
			count, valid := overviewCount(row["events"])
			if !valid {
				return overview.Result{}, invalidOverviewStorage()
			}
			result.TopEvents = append(result.TopEvents, overview.TopEvent{EventKey: key, Count: count})
		default:
			return overview.Result{}, invalidOverviewStorage()
		}
	}
	if err = scanner.Err(); err != nil {
		return overview.Result{}, fault.New(fault.Unavailable, "QUERY_READ_FAILED", "读取概览结果失败")
	}
	if !summary || len(result.Trend) > 169 || len(result.TopEvents) > 10 {
		return overview.Result{}, invalidOverviewStorage()
	}
	sort.Slice(result.Trend, func(i, j int) bool { return result.Trend[i].Bucket.Before(result.Trend[j].Bucket) })
	sort.Slice(result.TopEvents, func(i, j int) bool {
		if result.TopEvents[i].Count == result.TopEvents[j].Count {
			return result.TopEvents[i].EventKey < result.TopEvents[j].EventKey
		}
		return result.TopEvents[i].Count > result.TopEvents[j].Count
	})
	return result, nil
}

func invalidOverviewStorage() error {
	return fault.New(fault.Unavailable, "QUERY_STORAGE_RESPONSE_INVALID", "概览存储返回无效结果")
}
