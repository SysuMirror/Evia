// Package observe provides a dependency-free in-process metrics registry with
// Prometheus text exposition. Evia keeps runtime third-party dependencies
// minimal, so the registry uses only the standard library and exposes a small,
// fixed instrument set instead of pulling in a full telemetry framework.
package observe

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	metricNamePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNamePattern  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// atomicFloat holds a float64 behind atomic uint64 bits so request-path
// instruments need no mutex per sample.
type atomicFloat struct {
	bits atomic.Uint64
}

func (a *atomicFloat) Load() float64 { return math.Float64frombits(a.bits.Load()) }

func (a *atomicFloat) Store(value float64) { a.bits.Store(math.Float64bits(value)) }

func (a *atomicFloat) Add(delta float64) float64 {
	for {
		old := a.bits.Load()
		next := math.Float64frombits(old) + delta
		if a.bits.CompareAndSwap(old, math.Float64bits(next)) {
			return next
		}
	}
}

type meta struct {
	name   string
	help   string
	labels []string
}

// seriesKey is collision-free for arbitrary label values, including values
// that contain the joining character, because every segment is length-prefixed.
func seriesKey(values []string) string {
	var builder strings.Builder
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
		builder.WriteByte('|')
	}
	return builder.String()
}

func validateNames(name string, labels []string) {
	if !metricNamePattern.MatchString(name) {
		panic(fmt.Sprintf("observe: invalid metric name %q", name))
	}
	for _, label := range labels {
		if !labelNamePattern.MatchString(label) {
			panic(fmt.Sprintf("observe: metric %q has invalid label name %q", name, label))
		}
	}
}

// Counter is a monotonically increasing float64 instrument keyed by fixed
// label names. Misuse (wrong label count, negative increment) is a programming
// error and panics instead of being silently dropped.
type Counter struct {
	meta
	mu      sync.RWMutex
	samples map[string]*counterSeries
}

type counterSeries struct {
	values []string
	total  atomicFloat
}

func (c *Counter) check(values []string) {
	if len(values) != len(c.labels) {
		panic(fmt.Sprintf("observe: counter %q expects %d label values, got %d", c.name, len(c.labels), len(values)))
	}
}

func (c *Counter) series(values []string) *counterSeries {
	key := seriesKey(values)
	c.mu.RLock()
	found, ok := c.samples[key]
	c.mu.RUnlock()
	if ok {
		return found
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if found, ok = c.samples[key]; ok {
		return found
	}
	found = &counterSeries{values: append([]string(nil), values...)}
	c.samples[key] = found
	return found
}

// Add increments the counter by a non-negative value.
func (c *Counter) Add(value float64, values ...string) {
	if value < 0 {
		panic(fmt.Sprintf("observe: counter %q cannot decrease by %v", c.name, value))
	}
	c.check(values)
	c.series(values).total.Add(value)
}

// Inc increments the counter by one.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Gauge holds a value that can move in both directions.
type Gauge struct {
	meta
	mu      sync.RWMutex
	samples map[string]*gaugeSeries
}

type gaugeSeries struct {
	values []string
	value  atomicFloat
}

func (g *Gauge) check(values []string) {
	if len(values) != len(g.labels) {
		panic(fmt.Sprintf("observe: gauge %q expects %d label values, got %d", g.name, len(g.labels), len(values)))
	}
}

func (g *Gauge) series(values []string) *gaugeSeries {
	key := seriesKey(values)
	g.mu.RLock()
	found, ok := g.samples[key]
	g.mu.RUnlock()
	if ok {
		return found
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if found, ok = g.samples[key]; ok {
		return found
	}
	found = &gaugeSeries{values: append([]string(nil), values...)}
	g.samples[key] = found
	return found
}

// Set replaces the gauge value.
func (g *Gauge) Set(value float64, values ...string) {
	g.check(values)
	g.series(values).value.Store(value)
}

// Inc adds one to the gauge value.
func (g *Gauge) Inc(values ...string) { g.Add(1, values...) }

// Dec subtracts one from the gauge value.
func (g *Gauge) Dec(values ...string) { g.Add(-1, values...) }

// Add applies a signed delta to the gauge value.
func (g *Gauge) Add(delta float64, values ...string) {
	g.check(values)
	g.series(values).value.Add(delta)
}

// Histogram stores observations in fixed upper-bound buckets. The configured
// buckets are cumulative at exposition time; an implicit +Inf bucket always
// exists.
type Histogram struct {
	meta
	buckets []float64
	mu      sync.RWMutex
	samples map[string]*histogramSeries
}

type histogramSeries struct {
	values []string
	// One atomic counter per configured bucket plus the trailing +Inf bucket.
	counts []atomic.Uint64
	sum    atomicFloat
}

func (h *Histogram) check(values []string) {
	if len(values) != len(h.labels) {
		panic(fmt.Sprintf("observe: histogram %q expects %d label values, got %d", h.name, len(h.labels), len(values)))
	}
}

func (h *Histogram) series(values []string) *histogramSeries {
	key := seriesKey(values)
	h.mu.RLock()
	found, ok := h.samples[key]
	h.mu.RUnlock()
	if ok {
		return found
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if found, ok = h.samples[key]; ok {
		return found
	}
	found = &histogramSeries{values: append([]string(nil), values...), counts: make([]atomic.Uint64, len(h.buckets)+1)}
	h.samples[key] = found
	return found
}

// Observe records one value into exactly one bucket: the configured upper
// bound that is the smallest bound not below the value, or the trailing +Inf
// bucket when every bound is exceeded (including NaN, matching Prometheus).
func (h *Histogram) Observe(value float64, values ...string) {
	h.check(values)
	series := h.series(values)
	series.sum.Add(value)
	idx := len(h.buckets)
	if !math.IsNaN(value) {
		if found := sort.SearchFloat64s(h.buckets, value); found < len(h.buckets) {
			idx = found
		}
	}
	series.counts[idx].Add(1)
}

// Registry owns instrument registration order and scrapes every instrument in
// a deterministic render order.
type Registry struct {
	mu         sync.RWMutex
	startedAt  time.Time
	counters   []*Counter
	gauges     []*Gauge
	histograms []*Histogram
	names      map[string]string
}

// NewRegistry records process start time for uptime metrics.
func NewRegistry() *Registry {
	return &Registry{startedAt: time.Now(), names: make(map[string]string)}
}

func (r *Registry) claim(name, kind string, labels []string) {
	if existing, ok := r.names[name]; ok {
		panic(fmt.Sprintf("observe: metric %q already registered as %s", name, existing))
	}
	validateNames(name, labels)
	r.names[name] = kind
}

// Counter registers a named counter with fixed label names.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim(name, "counter", labels)
	counter := &Counter{meta: meta{name: name, help: help, labels: append([]string(nil), labels...)}, samples: make(map[string]*counterSeries)}
	r.counters = append(r.counters, counter)
	return counter
}

// Gauge registers a named gauge with fixed label names.
func (r *Registry) Gauge(name, help string, labels ...string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim(name, "gauge", labels)
	gauge := &Gauge{meta: meta{name: name, help: help, labels: append([]string(nil), labels...)}, samples: make(map[string]*gaugeSeries)}
	r.gauges = append(r.gauges, gauge)
	return gauge
}

// Histogram registers a named histogram. Buckets must be finite, strictly
// ascending and contain no NaN values.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	for idx, bound := range buckets {
		if math.IsNaN(bound) || math.IsInf(bound, 1) || math.IsInf(bound, -1) || (idx > 0 && bound <= buckets[idx-1]) {
			panic(fmt.Sprintf("observe: histogram %q has invalid buckets %v", name, buckets))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim(name, "histogram", labels)
	histogram := &Histogram{
		meta:    meta{name: name, help: help, labels: append([]string(nil), labels...)},
		buckets: append([]float64(nil), buckets...),
		samples: make(map[string]*histogramSeries),
	}
	r.histograms = append(r.histograms, histogram)
	return histogram
}

type countingWriter struct {
	target io.Writer
	count  int64
	err    error
}

func (w *countingWriter) write(line string) {
	if w.err != nil {
		return
	}
	n, err := io.WriteString(w.target, line)
	w.count += int64(n)
	w.err = err
}

func escapeHelp(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, "\n", `\n`)
}

func escapeLabel(value string) string {
	value = escapeHelp(value)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func formatFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "+Inf"
	case math.IsInf(value, -1):
		return "-Inf"
	default:
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
}

func labelBlock(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for idx, name := range names {
		parts[idx] = name + `="` + escapeLabel(values[idx]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// labelOrder compares fixed-position label values lexicographically so series
// render in a stable, human-meaningful order independent of insertion timing.
func labelOrder(left, right []string) int {
	for idx := 0; idx < len(left) && idx < len(right); idx++ {
		if left[idx] != right[idx] {
			return strings.Compare(left[idx], right[idx])
		}
	}
	return len(left) - len(right)
}

// sortedSeries returns map values ordered by their label values.
func sortedSeries[T any](samples map[string]T, labelsOf func(T) []string) []T {
	ordered := make([]T, 0, len(samples))
	for _, sample := range samples {
		ordered = append(ordered, sample)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return labelOrder(labelsOf(ordered[i]), labelsOf(ordered[j])) < 0
	})
	return ordered
}

func writeHeader(w *countingWriter, name, help, kind string) {
	w.write("# HELP " + name + " " + escapeHelp(help) + "\n")
	w.write("# TYPE " + name + " " + kind + "\n")
}

func writeCounter(w *countingWriter, counter *Counter) {
	counter.mu.RLock()
	defer counter.mu.RUnlock()
	writeHeader(w, counter.name, counter.help, "counter")
	for _, series := range sortedSeries(counter.samples, func(s *counterSeries) []string { return s.values }) {
		w.write(counter.name + labelBlock(counter.labels, series.values) + " " + formatFloat(series.total.Load()) + "\n")
	}
}

func writeGauge(w *countingWriter, gauge *Gauge) {
	gauge.mu.RLock()
	defer gauge.mu.RUnlock()
	writeHeader(w, gauge.name, gauge.help, "gauge")
	for _, series := range sortedSeries(gauge.samples, func(s *gaugeSeries) []string { return s.values }) {
		w.write(gauge.name + labelBlock(gauge.labels, series.values) + " " + formatFloat(series.value.Load()) + "\n")
	}
}

func writeHistogram(w *countingWriter, histogram *Histogram) {
	histogram.mu.RLock()
	defer histogram.mu.RUnlock()
	writeHeader(w, histogram.name, histogram.help, "histogram")
	for _, series := range sortedSeries(histogram.samples, func(s *histogramSeries) []string { return s.values }) {
		var cumulative uint64
		for idx, bound := range histogram.buckets {
			cumulative += series.counts[idx].Load()
			labels := append(append([]string(nil), series.values...), formatFloat(bound))
			w.write(histogram.name + "_bucket" + histogramLabelBlock(histogram.labels, labels) + " " + strconv.FormatUint(cumulative, 10) + "\n")
		}
		cumulative += series.counts[len(histogram.buckets)].Load()
		infLabels := append(append([]string(nil), series.values...), "+Inf")
		w.write(histogram.name + "_bucket" + histogramLabelBlock(histogram.labels, infLabels) + " " + strconv.FormatUint(cumulative, 10) + "\n")
		w.write(histogram.name + "_sum" + labelBlock(histogram.labels, series.values) + " " + formatFloat(series.sum.Load()) + "\n")
		w.write(histogram.name + "_count" + labelBlock(histogram.labels, series.values) + " " + strconv.FormatUint(cumulative, 10) + "\n")
	}
}

// histogramLabelBlock appends the synthetic le label to the instrument labels.
func histogramLabelBlock(names, values []string) string {
	labelNames := append(append([]string(nil), names...), "le")
	return labelBlock(labelNames, values)
}

// WriteTo renders every registered instrument followed by process and Go
// runtime samples in Prometheus text exposition format 0.0.4.
func (r *Registry) WriteTo(target io.Writer) (int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	writer := &countingWriter{target: target}
	for _, counter := range r.counters {
		writeCounter(writer, counter)
	}
	for _, gauge := range r.gauges {
		writeGauge(writer, gauge)
	}
	for _, histogram := range r.histograms {
		writeHistogram(writer, histogram)
	}
	writeRuntime(writer, r.startedAt)
	return writer.count, writer.err
}
