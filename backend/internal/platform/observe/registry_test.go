package observe

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func render(t *testing.T, registry *Registry) string {
	t.Helper()
	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("render metrics: %v", err)
	}
	return buffer.String()
}

func TestCounterAccumulatesAndIsolatesLabelSeries(t *testing.T) {
	registry := NewRegistry()
	counter := registry.Counter("evia_test_events_total", "test counter", "kind")
	counter.Inc("alpha")
	counter.Add(2, "alpha")
	counter.Inc("beta")

	output := render(t, registry)
	for _, expected := range []string{
		"# HELP evia_test_events_total test counter",
		"# TYPE evia_test_events_total counter",
		`evia_test_events_total{kind="alpha"} 3`,
		`evia_test_events_total{kind="beta"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("rendered output missing %q:\n%s", expected, output)
		}
	}
}

func TestGaugeSetIncAndDec(t *testing.T) {
	registry := NewRegistry()
	gauge := registry.Gauge("evia_test_depth", "test gauge")
	gauge.Set(5)
	gauge.Inc()
	gauge.Dec()
	gauge.Add(0.5)

	output := render(t, registry)
	if !strings.Contains(output, "evia_test_depth 5.5\n") {
		t.Fatalf("gauge value wrong:\n%s", output)
	}
}

func TestHistogramBucketsAreCumulativeWithInfinity(t *testing.T) {
	registry := NewRegistry()
	histogram := registry.Histogram("evia_test_latency_seconds", "test histogram", []float64{1, 2, 5})
	for _, value := range []float64{0.5, 1, 1.5, 2, 6, 6} {
		histogram.Observe(value)
	}

	output := render(t, registry)
	for _, expected := range []string{
		`evia_test_latency_seconds_bucket{le="1"} 2`,
		`evia_test_latency_seconds_bucket{le="2"} 4`,
		`evia_test_latency_seconds_bucket{le="5"} 4`,
		`evia_test_latency_seconds_bucket{le="+Inf"} 6`,
		`evia_test_latency_seconds_count 6`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("histogram output missing %q:\n%s", expected, output)
		}
	}
	if !strings.Contains(output, "# TYPE evia_test_latency_seconds histogram") {
		t.Fatalf("histogram type header missing:\n%s", output)
	}
}

func TestHistogramRoutesNaNToInfinityBucket(t *testing.T) {
	registry := NewRegistry()
	histogram := registry.Histogram("evia_test_nan_seconds", "nan histogram", []float64{1})
	histogram.Observe(math.NaN())
	histogram.Observe(0.5)

	output := render(t, registry)
	for _, expected := range []string{
		`evia_test_nan_seconds_bucket{le="1"} 1`,
		`evia_test_nan_seconds_bucket{le="+Inf"} 2`,
		`evia_test_nan_seconds_count 2`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("NaN bucket output missing %q:\n%s", expected, output)
		}
	}
}

func TestHistogramPreservesLabelSeriesAndSum(t *testing.T) {
	registry := NewRegistry()
	histogram := registry.Histogram("evia_test_split_seconds", "split histogram", []float64{1}, "route")
	histogram.Observe(0.5, "a")
	histogram.Observe(2, "a")
	histogram.Observe(0.25, "b")

	output := render(t, registry)
	for _, expected := range []string{
		`evia_test_split_seconds_bucket{route="a",le="1"} 1`,
		`evia_test_split_seconds_bucket{route="a",le="+Inf"} 2`,
		`evia_test_split_seconds_sum{route="a"} 2.5`,
		`evia_test_split_seconds_count{route="a"} 2`,
		`evia_test_split_seconds_bucket{route="b",le="1"} 1`,
		`evia_test_split_seconds_count{route="b"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("labeled histogram missing %q:\n%s", expected, output)
		}
	}
}

func TestRenderingEscapesHelpAndLabelText(t *testing.T) {
	registry := NewRegistry()
	counter := registry.Counter("evia_test_escaped_total", `line one\nline "two"`, "note")
	counter.Inc(`a"b\c`)

	output := render(t, registry)
	for _, expected := range []string{
		// HELP escapes backslashes and newlines only; literal quotes stay visible.
		`# HELP evia_test_escaped_total line one\\nline "two"`,
		// Label values additionally escape quotes so the block stays parseable.
		`evia_test_escaped_total{note="a\"b\\c"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("escaping wrong, missing %q:\n%s", expected, output)
		}
	}
}

func TestRenderingIsDeterministicAcrossScrapes(t *testing.T) {
	registry := NewRegistry()
	counter := registry.Counter("evia_test_order_total", "ordering", "kind")
	counter.Inc("zeta")
	counter.Inc("alpha")
	counter.Inc("mid")

	first := render(t, registry)
	counter.Inc("alpha")
	second := render(t, registry)
	firstLines := strings.Split(first, "\n")
	secondLines := strings.Split(second, "\n")
	// Sample lines must appear in sorted label order on every scrape.
	var firstSamples []string
	for _, line := range firstLines {
		if strings.HasPrefix(line, "evia_test_order_total{") {
			firstSamples = append(firstSamples, line)
		}
	}
	want := []string{
		`evia_test_order_total{kind="alpha"} 1`,
		`evia_test_order_total{kind="mid"} 1`,
		`evia_test_order_total{kind="zeta"} 1`,
	}
	if strings.Join(firstSamples, "|") != strings.Join(want, "|") {
		t.Fatalf("samples not sorted deterministically: %v", firstSamples)
	}
	if len(firstLines) != len(secondLines) {
		t.Fatalf("scrape changed line structure: %d vs %d", len(firstLines), len(secondLines))
	}
}

func TestInstrumentMisusePanics(t *testing.T) {
	registry := NewRegistry()
	counter := registry.Counter("evia_test_misuse_total", "misuse", "kind")
	gauge := registry.Gauge("evia_test_misuse_depth", "misuse")
	histogram := registry.Histogram("evia_test_misuse_seconds", "misuse", []float64{1})

	for _, tc := range []struct {
		name string
		run  func()
	}{
		{"counter label count", func() { counter.Inc("a", "b") }},
		{"negative counter", func() { counter.Add(-1, "a") }},
		{"gauge label count", func() { gauge.Set(1, "a") }},
		{"histogram label count", func() { histogram.Observe(1, "a") }},
		{"invalid metric name", func() { registry.Counter("bad-name", "x") }},
		{"invalid label name", func() { registry.Gauge("evia_test_label_gauge", "x", "bad-label") }},
		{"duplicate metric", func() { registry.Counter("evia_test_misuse_total", "dup") }},
		{"unsorted buckets", func() { registry.Histogram("evia_test_buckets_seconds", "x", []float64{2, 1}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			tc.run()
		})
	}
}

func TestConcurrentIncrementsAreRaceFreeAndExact(t *testing.T) {
	registry := NewRegistry()
	counter := registry.Counter("evia_test_concurrent_total", "concurrent", "kind")
	const goroutines = 16
	const iterations = 200
	var wait sync.WaitGroup
	for idx := 0; idx < goroutines; idx++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for round := 0; round < iterations; round++ {
				counter.Inc("shared")
			}
		}()
	}
	wait.Wait()

	output := render(t, registry)
	expected := `evia_test_concurrent_total{kind="shared"} ` + strconv.Itoa(goroutines*iterations)
	if !strings.Contains(output, expected) {
		t.Fatalf("concurrent count wrong, missing %q:\n%s", expected, output)
	}
}

func TestEmptyRegistryStillRendersRuntimeMetrics(t *testing.T) {
	output := render(t, NewRegistry())
	for _, name := range []string{
		"# TYPE go_goroutines gauge",
		"# TYPE process_uptime_seconds gauge",
		"# TYPE evia_build_info gauge",
		`evia_build_info{goversion="`,
	} {
		if !strings.Contains(output, name) {
			t.Fatalf("runtime metric missing %q:\n%s", name, output)
		}
	}
}
