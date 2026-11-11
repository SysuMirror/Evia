package observe

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// nestedMux reproduces the runtime layout: an outer ServeMux mounts an inner
// API mux at "/", so route labels must come from the inner mux's template
// rather than raw paths or the outer catch-all.
func nestedMux() http.Handler {
	outer := http.NewServeMux()
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/v1/projects/{project_id}/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	inner.HandleFunc("POST /api/v1/collect", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	outer.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	outer.Handle("/", inner)
	return outer
}

func TestMiddlewareRecordsRouteTemplateStatusAndLatency(t *testing.T) {
	registry := NewRegistry("test")
	instrumenter := NewInstrumenter(registry)
	server := httptest.NewServer(instrumenter.Middleware(nestedMux()))
	defer server.Close()

	responses := []struct {
		method string
		path   string
		code   int
	}{
		{http.MethodGet, "/api/v1/projects/abc-123/events", http.StatusNoContent},
		{http.MethodGet, "/api/v1/projects/abc-123/events", http.StatusNoContent},
		{http.MethodPost, "/api/v1/collect", http.StatusAccepted},
		{http.MethodGet, "/healthz", http.StatusNoContent},
	}
	for _, response := range responses {
		request, err := http.NewRequest(response.method, server.URL+response.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != response.code {
			t.Fatalf("unexpected status %d", result.StatusCode)
		}
		result.Body.Close()
	}

	output := render(t, registry)
	for _, expected := range []string{
		`evia_http_requests_total{method="GET",route="/api/v1/projects/{project_id}/events",code="204"} 2`,
		`evia_http_requests_total{method="POST",route="/api/v1/collect",code="202"} 1`,
		`evia_http_requests_total{method="GET",route="/healthz",code="204"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("request metric missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "abc-123") {
		t.Fatalf("raw path identifier leaked into metric labels:\n%s", output)
	}
	if !strings.Contains(output, `evia_http_request_duration_seconds_bucket{method="GET",route="/healthz",le="`) {
		t.Fatalf("latency histogram missing:\n%s", output)
	}
}

func TestMiddlewareReturnsInFlightGaugeToZero(t *testing.T) {
	registry := NewRegistry("test")
	instrumenter := NewInstrumenter(registry)
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	handler := instrumenter.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		started <- struct{}{}
		<-release
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	<-started
	if !strings.Contains(render(t, registry), "evia_http_requests_in_flight 1\n") {
		t.Fatalf("in-flight gauge did not rise:\n%s", render(t, registry))
	}
	close(release)
	// Wait for the handler goroutine so the deferred decrement has definitely run.
	<-done
	if !strings.Contains(render(t, registry), "evia_http_requests_in_flight 0\n") {
		t.Fatalf("in-flight gauge did not return to zero:\n%s", render(t, registry))
	}
}

func TestMetricsEndpointExcludesItselfAndServesTextFormat(t *testing.T) {
	registry := NewRegistry("test")
	instrumenter := NewInstrumenter(registry)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", MetricsHandler(registry))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := httptest.NewServer(instrumenter.Middleware(mux))
	defer server.Close()

	result, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", result.StatusCode)
	}
	if contentType := result.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain; version=0.0.4") {
		t.Fatalf("metrics content type = %q", contentType)
	}
	output := render(t, registry)
	if strings.Contains(output, `route="/metrics"`) {
		t.Fatalf("scrape endpoint recorded itself:\n%s", output)
	}

	// A health request after the scrape is still recorded.
	health, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if !strings.Contains(render(t, registry), `route="/healthz",code="204"`) {
		t.Fatalf("health request not recorded after scrape:\n%s", render(t, registry))
	}
}

func TestMetricsHandlerRejectsNonGetMethods(t *testing.T) {
	registry := NewRegistry("test")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	MetricsHandler(registry).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST /metrics response wrong: code=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func TestRouteLabelBoundedForUnmatchedRequests(t *testing.T) {
	registry := NewRegistry("test")
	instrumenter := NewInstrumenter(registry)
	handler := instrumenter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/anything/raw/42", nil))

	output := render(t, registry)
	if !strings.Contains(output, `route="unmatched",code="404"`) {
		t.Fatalf("unmatched route label wrong:\n%s", output)
	}
}

func TestConcurrentRequestsDoNotDuplicateSeries(t *testing.T) {
	registry := NewRegistry("test")
	instrumenter := NewInstrumenter(registry)
	handler := instrumenter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	const concurrency = 20
	var wait sync.WaitGroup
	for idx := 0; idx < concurrency; idx++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/same", nil))
		}()
	}
	wait.Wait()

	output := render(t, registry)
	want := `evia_http_requests_total{method="GET",route="unmatched",code="200"} ` + strconv.Itoa(concurrency)
	if !strings.Contains(output, want) {
		t.Fatalf("series duplicated or count wrong, missing %q:\n%s", want, output)
	}
	// Only the counter carries the code label; histogram buckets use le instead.
	if count := strings.Count(output, `route="unmatched",code="200"`); count != 1 {
		t.Fatalf("unexpected counter series line count %d:\n%s", count, output)
	}
}
