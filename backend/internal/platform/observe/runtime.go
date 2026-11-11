package observe

import (
	"runtime"
	"runtime/pprof"
	"strconv"
	"time"
)

type namedValue struct {
	name  string
	help  string
	kind  string
	value float64
}

type labeledValue struct {
	name   string
	help   string
	kind   string
	labels [][2]string
	value  float64
}

// runtimeSamples reads the current Go runtime and process state. Values are
// captured at scrape time rather than cached, so each /metrics response
// reflects the live process. ReadMemStats pauses the world briefly; the scrape
// path is operator-initiated and the metric set stays intentionally small.
func runtimeSamples(startedAt time.Time) (scalars []namedValue, labeled []labeledValue) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	uptime := time.Since(startedAt).Seconds()
	scalars = []namedValue{
		{"process_start_time_seconds", "Unix time at which the process started.", "gauge", float64(startedAt.Unix())},
		{"process_uptime_seconds", "Elapsed seconds since the process started.", "gauge", uptime},
		{"go_goroutines", "Number of currently running goroutines.", "gauge", float64(runtime.NumGoroutine())},
		{"go_threads", "Number of OS threads created by the Go runtime.", "gauge", float64(pprof.Lookup("threadcreate").Count())},
		{"go_memstats_alloc_bytes", "Bytes of allocated heap objects still in use.", "gauge", float64(memory.Alloc)},
		{"go_memstats_alloc_bytes_total", "Cumulative bytes allocated for heap objects.", "counter", float64(memory.TotalAlloc)},
		{"go_memstats_sys_bytes", "Bytes obtained from the operating system.", "gauge", float64(memory.Sys)},
		{"go_memstats_heap_inuse_bytes", "Heap bytes that are in use.", "gauge", float64(memory.HeapInuse)},
		{"go_memstats_heap_objects", "Number of allocated heap objects.", "gauge", float64(memory.HeapObjects)},
		{"go_memstats_next_gc_bytes", "Target heap size when the next garbage collection runs.", "gauge", float64(memory.NextGC)},
		{"go_gc_duration_seconds_total", "Cumulative wall-clock seconds spent in stop-the-world GC pauses.", "counter", float64(memory.PauseTotalNs) / float64(time.Second)},
		{"go_gc_cycles_total", "Cumulative number of completed garbage collection cycles.", "counter", float64(memory.NumGC)},
	}
	labeled = []labeledValue{
		{
			name:   "evia_build_info",
			help:   "Constant build metadata for the running Evia process.",
			kind:   "gauge",
			labels: [][2]string{{"goversion", runtime.Version()}},
			value:  1,
		},
	}
	return scalars, labeled
}

func writeRuntime(w *countingWriter, startedAt time.Time) {
	scalars, labeled := runtimeSamples(startedAt)
	for _, sample := range scalars {
		writeHeader(w, sample.name, sample.help, sample.kind)
		w.write(sample.name + " " + formatFloat(sample.value) + "\n")
	}
	for _, sample := range labeled {
		writeHeader(w, sample.name, sample.help, sample.kind)
		names := make([]string, len(sample.labels))
		values := make([]string, len(sample.labels))
		for idx, pair := range sample.labels {
			names[idx] = pair[0]
			values[idx] = pair[1]
		}
		w.write(sample.name + labelBlock(names, values) + " " + strconv.FormatFloat(sample.value, 'g', -1, 64) + "\n")
	}
}
