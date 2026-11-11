package observe

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeSamplesCoverRequiredMetrics(t *testing.T) {
	startedAt := time.Now().Add(-2 * time.Second)
	scalars, labeled := runtimeSamples(startedAt, "retention-worker")

	required := map[string]string{
		"process_start_time_seconds":    "gauge",
		"process_uptime_seconds":        "gauge",
		"go_goroutines":                 "gauge",
		"go_threads":                    "gauge",
		"go_memstats_alloc_bytes":       "gauge",
		"go_memstats_alloc_bytes_total": "counter",
		"go_memstats_sys_bytes":         "gauge",
		"go_memstats_heap_inuse_bytes":  "gauge",
		"go_memstats_heap_objects":      "gauge",
		"go_memstats_next_gc_bytes":     "gauge",
		"go_gc_duration_seconds_total":  "counter",
		"go_gc_cycles_total":            "counter",
	}
	seen := make(map[string]bool)
	for _, sample := range scalars {
		kind, ok := required[sample.name]
		if !ok {
			t.Fatalf("unexpected scalar metric %q", sample.name)
		}
		if sample.kind != kind {
			t.Fatalf("metric %q has kind %q, want %q", sample.name, sample.kind, kind)
		}
		if sample.value < 0 {
			t.Fatalf("metric %q must not be negative: %v", sample.name, sample.value)
		}
		seen[sample.name] = true
	}
	for name := range required {
		if !seen[name] {
			t.Fatalf("required runtime metric %q not emitted", name)
		}
	}
	if len(labeled) != 1 || labeled[0].name != "evia_process_info" || labeled[0].value != 1 {
		t.Fatalf("process info sample wrong: %+v", labeled)
	}
	labels := map[string]string{}
	for _, pair := range labeled[0].labels {
		labels[pair[0]] = pair[1]
	}
	if labels["role"] != "retention-worker" {
		t.Fatalf("process role label missing: %+v", labeled[0].labels)
	}
	if labels["goversion"] == "" {
		t.Fatalf("goversion label missing: %+v", labeled[0].labels)
	}
}

func TestUptimeAdvancesBetweenScrapes(t *testing.T) {
	startedAt := time.Now().Add(-time.Second)
	first, _ := runtimeSamples(startedAt, "serve")
	time.Sleep(10 * time.Millisecond)
	second, _ := labeledUptime(t, startedAt)

	var firstUptime float64
	for _, sample := range first {
		if sample.name == "process_uptime_seconds" {
			firstUptime = sample.value
		}
	}
	if second <= firstUptime {
		t.Fatalf("uptime did not advance: %v -> %v", firstUptime, second)
	}
}

func labeledUptime(t *testing.T, startedAt time.Time) (float64, []labeledValue) {
	t.Helper()
	scalars, labeled := runtimeSamples(startedAt, "serve")
	for _, sample := range scalars {
		if sample.name == "process_uptime_seconds" {
			return sample.value, labeled
		}
	}
	t.Fatal("uptime metric missing")
	return 0, nil
}

func TestRuntimeExpositionUsesTextFormat(t *testing.T) {
	output := render(t, NewRegistry("test"))
	if !strings.Contains(output, "# TYPE go_gc_cycles_total counter") {
		t.Fatalf("GC counter header missing:\n%s", output)
	}
	if strings.Contains(output, "\n\n") {
		t.Fatalf("exposition contains blank lines:\n%s", output)
	}
}
