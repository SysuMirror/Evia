package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// safeBuffer serializes log writes from the server goroutine against test
// goroutine reads so the assertions stay race-clean under -race.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// capturingBoundary routes structured logs into a buffer so a test can prove
// client disconnects never reach the ERROR level used by alerting and SLOs.
func capturingBoundary(t *testing.T) (*Boundary, *safeBuffer) {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	logs := &safeBuffer{}
	return &Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(logs, nil))}, logs
}

// A peer that abandons the request must not be recorded as a server 500: the
// boundary emits one warning and writes nothing because the socket is gone.
func TestBoundaryClientCancellationIsNotServerError(t *testing.T) {
	b, logs := capturingBoundary(t)
	handler := b.Middleware(b.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		return r.Context().Err()
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Body.Len() != 0 {
		t.Fatalf("abandoned request must not write a problem body, got %q", response.Body.String())
	}
	if strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "request failed") {
		t.Fatalf("client disconnect was logged as a server failure: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "request canceled by client") {
		t.Fatalf("missing disconnect audit log: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "/api/v1/projects") {
		t.Fatalf("disconnect log must retain the request path: %s", logs.String())
	}
}

// Cancellation produced inside the application while the request context stays
// live is an internal fault and must keep surfacing as a 500, otherwise a real
// defect could be hidden behind the client-disconnect rule.
func TestBoundaryInternalCancellationStaysInternalError(t *testing.T) {
	b, logs := capturingBoundary(t)
	handler := b.Handler(func(http.ResponseWriter, *http.Request) error {
		return context.Canceled
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("internal cancellation status = %d, want 500", response.Code)
	}
	var problem wire.Problem
	if e := json.Unmarshal(response.Body.Bytes(), &problem); e != nil || problem.Code != "INTERNAL_ERROR" {
		t.Fatalf("internal cancellation lost its problem envelope: %s", response.Body.String())
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("internal cancellation must stay at ERROR level: %s", logs.String())
	}
}

// Dependency adapters (e.g. the Redis budget) return a fault.Unavailable that
// also wraps context.Canceled. With the request context still live that fault
// classification must win; the disconnect rule must never downgrade a 503.
func TestBoundaryDependencyFaultWrappingCanceledStaysUnavailable(t *testing.T) {
	b, _ := capturingBoundary(t)
	dependencyErr := fault.New(fault.Unavailable, "BUDGET_UNAVAILABLE", "接入预算暂不可用")
	dependencyErr.Cause = context.Canceled
	if !errors.Is(dependencyErr, context.Canceled) {
		t.Fatal("test fixture should still satisfy errors.Is context.Canceled")
	}
	handler := b.Handler(func(http.ResponseWriter, *http.Request) error {
		return dependencyErr
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("dependency fault status = %d, want 503", response.Code)
	}
	var problem wire.Problem
	if e := json.Unmarshal(response.Body.Bytes(), &problem); e != nil || problem.Code != "BUDGET_UNAVAILABLE" {
		t.Fatalf("dependency fault lost its stable code: %s", response.Body.String())
	}
}

// Exercises a real TCP socket: the client cancels in flight while the handler
// is blocked on its request context. The boundary must observe a peer
// disconnect and emit a warning rather than a 500/ERROR on the server side.
func TestBoundaryRealSocketClientDisconnectWarns(t *testing.T) {
	b, logs := capturingBoundary(t)
	entered := make(chan struct{})
	handler := b.Middleware(b.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		close(entered)
		<-r.Context().Done()
		return r.Context().Err()
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/projects", nil)
	if e != nil {
		t.Fatal(e)
	}
	finished := make(chan struct{})
	go func() {
		_, _ = http.DefaultClient.Do(request)
		close(finished)
	}()

	<-entered
	cancel()
	<-finished

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(logs.String(), "request canceled by client") {
		time.Sleep(5 * time.Millisecond)
	}
	if strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "request failed") {
		t.Fatalf("real-socket disconnect was logged as a server error: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "request canceled by client") {
		t.Fatalf("real-socket disconnect did not produce the warning: %s", logs.String())
	}
}
