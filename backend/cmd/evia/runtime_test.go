package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

func TestSupervisorPropagatesFailureAndJoinsSibling(t *testing.T) {
	secretFailure := errors.New("driver included synthetic-sensitive-value")
	joined := make(chan struct{})
	err := supervise(context.Background(), []task{
		{"failing worker", func(context.Context) error { return secretFailure }},
		{"other worker", func(ctx context.Context) error { <-ctx.Done(); close(joined); return ctx.Err() }},
	}, time.Second)
	if err == nil || !errors.Is(err, secretFailure) || strings.Contains(err.Error(), "synthetic-sensitive-value") {
		t.Fatal("worker failure was swallowed or exposed", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("supervisor returned before sibling stopped")
	}
}
func TestSupervisorRejectsUnexpectedSuccessfulExit(t *testing.T) {
	if err := supervise(context.Background(), []task{{"unexpected exit", func(context.Context) error { return nil }}}, time.Second); err == nil {
		t.Fatal("unexpected clean worker exit was ignored")
	}
}
func TestSupervisorGracefulCancellationAndBoundedShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := supervise(ctx, []task{{"worker", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}}, time.Second); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	stopped := make(chan struct{})
	defer close(blocked)
	started := time.Now()
	err := supervise(ctx, []task{{"stuck worker", func(context.Context) error { <-blocked; close(stopped); return nil }}}, 20*time.Millisecond)
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("unbounded worker shutdown", err)
	}
}
func TestHTTPListenerFailureStopsRuntime(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Addr: listener.Addr().String(), Handler: http.NotFoundHandler()}
	err = supervise(context.Background(), []task{{"HTTP listener", func(ctx context.Context) error { return runHTTP(ctx, server, observe.NewDrainGate(), 0) }}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP listener") {
		t.Fatal("listener bind failure was lost", err)
	}
}
func TestClickHouseProbeRequiresAuthorizedExactResponse(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				body, _ := io.ReadAll(r.Body)
				if !ok || user != "synthetic" || pass != "synthetic-password" || r.URL.Query().Get("database") != "evia" || string(body) != "SELECT 1" {
					t.Error("probe omitted credentials/database or changed SQL")
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(status)
				io.WriteString(w, "1\n")
			}))
			defer server.Close()
			client := httpClient()
			defer client.CloseIdleConnections()
			cfg := config.Config{ClickHouse: config.ClickHouse{URL: server.URL, Database: "evia", Username: "synthetic", Password: "synthetic-password"}}
			err := clickHouseProbe(context.Background(), cfg, client)
			if (err == nil) != (status == http.StatusOK) {
				t.Fatal("probe trusted an unauthorized or redirected response", err)
			}
		})
	}
}
func TestInvalidCommandAndDisabledWorkersFailBeforeDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, args := range [][]string{nil, {"unknown"}, {"serve", "extra"}, {"ingest-worker"}, {"query-worker"}, {"erasure-worker"}} {
		get := func(name string) string {
			if name == "EVIA_DATABASE_URL" {
				return "postgres://synthetic-sensitive-value@unreachable"
			}
			return ""
		}
		err := execute(context.Background(), args, get, logger)
		if err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") {
			t.Fatal("invalid command leaked credentials or contacted a dependency", err)
		}
	}
}

func TestMetricsTasksDisabledByDefaultAndRoleNamedWhenEnabled(t *testing.T) {
	registry := observe.NewRegistry("retention-worker")
	if tasks := metricsTasks(config.Config{}, "retention-worker", registry); tasks != nil {
		t.Fatalf("worker without EVIA_METRICS_LISTEN must not open a listener: %+v", tasks)
	}
	enabled := config.Config{MetricsListen: "127.0.0.1:19090"}
	for _, role := range []string{"ingest-worker", "query-worker", "erasure-worker", "retention-worker"} {
		tasks := metricsTasks(enabled, role, observe.NewRegistry(role))
		if len(tasks) != 1 || tasks[0].name != role+" metrics listener" || tasks[0].run == nil {
			t.Fatalf("metrics task for %s wired incorrectly: %+v", role, tasks)
		}
	}
}

func TestReadinessKafkaStreamIsSeparateAndBounded(t *testing.T) {
	c := config.Config{InstanceID: "instance", Kafka: config.Kafka{Brokers: []string{"127.0.0.1:19092"}, Topic: "evia.events", DeadLetterTopic: "evia.quarantine", Group: strings.Repeat("g", 64), ProbeTopic: "evia.readiness", TLSEnabled: true}}
	probe := probeKafkaConfig(c, "readiness-worker")
	facts := kafkaConfig(c, "materialize")
	if probe.Topic != c.Kafka.ProbeTopic || probe.Topic == facts.Topic || probe.Group == facts.Group || len(probe.Group) > 128 || probe.TLS == nil {
		t.Fatal("readiness traffic mixed with fact stream or escaped worker identity bounds")
	}
	if probe.ClientID == facts.ClientID {
		t.Fatal("independent clients share a process identifier")
	}
	if c.Kafka.Topic != "evia.events" || c.Kafka.Group != strings.Repeat("g", 64) {
		t.Fatal("probe configuration mutated fact consumer settings")
	}
}

func TestHTTPDrainFailsReadinessBeforeClosingListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := observe.NewDrainGate()
	inFlight := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", gate.ReadinessHandler(nil))
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(inFlight)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: time.Second,
	}
	listener.Close() // runHTTP binds via Addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHTTP(ctx, server, gate, 300*time.Millisecond) }()

	base := "http://" + server.Addr
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if response, err := http.Get(base + "/readyz"); err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// An in-flight request starts before the shutdown signal.
	slowResult := make(chan int, 1)
	go func() {
		response, err := http.Get(base + "/slow")
		if err != nil {
			slowResult <- 0
			return
		}
		response.Body.Close()
		slowResult <- response.StatusCode
	}()
	<-inFlight

	cancel()

	// Readiness must fail immediately while the listener is still draining.
	readyDeadline := time.Now().Add(200 * time.Millisecond)
	sawUnavailable := false
	for time.Now().Before(readyDeadline) {
		response, err := http.Get(base + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusServiceUnavailable {
				sawUnavailable = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawUnavailable {
		t.Fatal("readyz did not fail with 503 during the drain delay")
	}
	if !gate.Draining() {
		t.Fatal("gate not marked draining")
	}

	// The in-flight request must still complete once released.
	close(release)
	select {
	case status := <-slowResult:
		if status != http.StatusNoContent {
			t.Fatalf("in-flight request status = %d, want 204", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("runHTTP returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runHTTP did not return within the bounded shutdown")
	}
}

func TestHTTPDrainZeroDelayKeepsImmediateShutdown(t *testing.T) {
	gate := observe.NewDrainGate()
	server := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHTTP(ctx, server, gate, 0) }()
	cancel()
	select {
	case <-done:
		if !gate.Draining() {
			t.Fatal("zero-delay shutdown must still mark the gate draining")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("zero-delay shutdown blocked")
	}
}
