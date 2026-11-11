package observe

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultDurationBuckets cover local API responses from sub-millisecond
// handlers up to the 25 second request boundary enforced by the runtime.
var DefaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25}

// statusWriter captures the committed status while leaving interface upgrades
// (flush, connection control) available to wrapped handlers through Unwrap.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Instrumenter owns the HTTP instruments for one listener. Label values use
// route templates and fixed status codes so series cardinality stays bounded
// regardless of path identifiers or request volume.
type Instrumenter struct {
	requests *Counter
	duration *Histogram
	inFlight *Gauge
}

// NewInstrumenter registers the standard HTTP instruments on a registry.
func NewInstrumenter(registry *Registry) *Instrumenter {
	return &Instrumenter{
		requests: registry.Counter("evia_http_requests_total",
			"HTTP requests handled by the process, partitioned by method, route template and status code.",
			"method", "route", "code"),
		duration: registry.Histogram("evia_http_request_duration_seconds",
			"HTTP request handling duration in seconds, partitioned by method and route template.",
			DefaultDurationBuckets, "method", "route"),
		inFlight: registry.Gauge("evia_http_requests_in_flight",
			"HTTP requests currently being handled by the process."),
	}
}

// routeLabel returns the matched ServeMux pattern without its method prefix.
// Raw paths never become labels, so identifiers cannot inflate cardinality.
func routeLabel(request *http.Request) string {
	pattern := request.Pattern
	if pattern == "" {
		return "unmatched"
	}
	if _, route, ok := strings.Cut(pattern, " "); ok {
		return route
	}
	return pattern
}

// Middleware records request counts, latency and in-flight depth. The metrics
// endpoint itself is not recorded to avoid scrape feedback.
func (in *Instrumenter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/metrics" {
			next.ServeHTTP(writer, request)
			return
		}
		in.inFlight.Inc()
		defer in.inFlight.Dec()
		status := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(status, request)
		route := routeLabel(request)
		code := strconv.Itoa(status.status)
		in.requests.Inc(request.Method, route, code)
		in.duration.Observe(time.Since(start).Seconds(), request.Method, route)
	})
}

// MetricsHandler exposes the registry in Prometheus text exposition format.
// It carries no business data: labels are route templates, status codes and
// runtime metadata only. Deployments must still restrict the listener at the
// network layer because the endpoint is unauthenticated, like /healthz.
func MetricsHandler(registry *Registry) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", http.MethodGet)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		if request.Method == http.MethodHead {
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.WriteHeader(http.StatusOK)
		if _, err := registry.WriteTo(writer); err != nil {
			// The header is already committed; the scrape client sees a
			// truncated body and retries, matching standard exposition handlers.
			return
		}
	}
}
