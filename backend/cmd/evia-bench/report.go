package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Counts struct {
	Attempted         int `json:"attempted"`
	Accepted          int `json:"accepted"`
	PermanentRejected int `json:"permanent_rejected"`
	RetryableRejected int `json:"retryable_rejected"`
	RateLimited       int `json:"rate_limited"`
	ServerErrors      int `json:"server_errors"`
	Unexpected        int `json:"unexpected"`
	Dropped           int `json:"dropped"`
}
type Failure struct {
	Ordinal    int    `json:"ordinal"`
	Replay     bool   `json:"replay"`
	HTTPStatus int    `json:"http_status"`
	Code       string `json:"code"`
}
type Latency struct {
	Method  string  `json:"method"`
	Samples int     `json:"sample_count"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
	Max     float64 `json:"max_ms"`
}
type ExpectedFacts struct {
	Count             int     `json:"count"`
	UIDCardinality    int     `json:"uid_cardinality"`
	ValueSum          int64   `json:"value_sum"`
	DurationMSAverage float64 `json:"duration_ms_average"`
	SectionZeroCount  int     `json:"section_zero_count"`
}
type Report struct {
	Version                 int           `json:"version"`
	Complete                bool          `json:"complete"`
	StopReason              string        `json:"stop_reason"`
	Mode                    string        `json:"mode"`
	RunID                   string        `json:"run_id"`
	SourceKind              string        `json:"source_kind"`
	ProjectID               string        `json:"project_id"`
	SourceID                string        `json:"source_id,omitempty"`
	Environment             string        `json:"environment"`
	EventKey                string        `json:"event_key"`
	SchemaVersion           int64         `json:"schema_version"`
	ReleaseRevision         int64         `json:"release_revision"`
	Expected                ExpectedFacts `json:"expected"`
	RequestedUnique         int           `json:"requested_unique_events"`
	ExpectedUIDs            int           `json:"expected_uid_cardinality"`
	PlannedReplay           int           `json:"planned_replay_events"`
	BatchSize               int           `json:"batch_size"`
	Concurrency             int           `json:"concurrency"`
	TargetRate              float64       `json:"target_events_per_second"`
	WindowStart             string        `json:"window_start"`
	WindowEnd               string        `json:"window_end"`
	StartedAt               string        `json:"started_at"`
	FinishedAt              string        `json:"finished_at"`
	Elapsed                 float64       `json:"elapsed_seconds"`
	Unique                  Counts        `json:"unique"`
	Replay                  Counts        `json:"replay"`
	Requests                int           `json:"requests"`
	RequestBytes            int64         `json:"request_bytes"`
	RequestsPerSecond       float64       `json:"requests_per_second"`
	UniqueAcceptedPerSecond float64       `json:"unique_accepted_events_per_second"`
	MaxScheduleLag          float64       `json:"max_schedule_lag_ms"`
	Latency                 Latency       `json:"latency"`
	HTTPStatuses            map[int]int   `json:"http_statuses"`
	Failures                []Failure     `json:"failure_samples"`
	FailureSamplesTruncated bool          `json:"failure_samples_truncated"`
	RetryPolicy             string        `json:"retry_policy"`
}

type recorder struct {
	mu         sync.Mutex
	report     Report
	buckets    [2048]int
	maxLatency time.Duration
}

func (m *recorder) counts(replay bool) *Counts {
	if replay {
		return &m.report.Replay
	}
	return &m.report.Unique
}
func (m *recorder) failure(b batch, index, status int, code string) {
	if len(m.report.Failures) < 64 {
		m.report.Failures = append(m.report.Failures, Failure{b.start + index, b.replay, status, code})
	} else {
		m.report.FailureSamplesTruncated = true
	}
}
func (m *recorder) latency(d time.Duration) {
	bin := 0
	if d > time.Microsecond {
		bin = int(math.Ceil(math.Log(float64(d)/float64(time.Microsecond)) / math.Log(1.02)))
	}
	m.buckets[min(bin, len(m.buckets)-1)]++
	m.maxLatency = max(m.maxLatency, d)
}
func (m *recorder) summarizeLatency() Latency {
	l := Latency{Method: "logarithmic_1.02_upper_bound", Samples: m.report.Requests, Max: float64(m.maxLatency) / float64(time.Millisecond)}
	quantile := func(p float64) float64 {
		if l.Samples == 0 {
			return 0
		}
		target, count := int(math.Ceil(float64(l.Samples)*p)), 0
		for i, n := range m.buckets {
			count += n
			if count >= target {
				return min(l.Max, math.Pow(1.02, float64(i))/1000)
			}
		}
		return l.Max
	}
	l.P50, l.P95, l.P99 = quantile(.5), quantile(.95), quantile(.99)
	return l
}

func writeReport(path string, report Report) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".evia-bench-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(report)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
