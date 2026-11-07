package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

func TestBoundaryCancelsDependenciesAndReturnsSafeTimeout(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		b := boundary(t)
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		timeout := 20 * time.Millisecond
		if callerDeadline {
			ctx, cancel := context.WithTimeout(request.Context(), timeout)
			defer cancel()
			request = request.WithContext(ctx)
			timeout = time.Second
		}
		var dependency context.Context
		handler := b.middleware(b.Handler(func(w http.ResponseWriter, r *http.Request) error {
			dependency = r.Context()
			<-r.Context().Done()
			return r.Context().Err()
		}), timeout)
		response := httptest.NewRecorder()
		started := time.Now()
		handler.ServeHTTP(response, request)
		if time.Since(started) > 500*time.Millisecond {
			t.Fatal("dependency did not obey the earliest request deadline")
		}
		if dependency == nil || dependency.Err() != context.DeadlineExceeded {
			t.Fatal("deadline did not reach dependency")
		}
		var problem wire.Problem
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusServiceUnavailable || problem.Code != "REQUEST_TIMEOUT" || problem.RequestID == "" || problem.RequestID != response.Header().Get("X-Request-ID") {
			t.Fatal("timeout lost its stable HTTP error boundary", response.Code, problem.Code)
		}
	}
}

func TestBoundaryReleasesContextWhenHandlerReturns(t *testing.T) {
	b := boundary(t)
	var dependency context.Context
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dependency = r.Context()
		deadline, ok := dependency.Deadline()
		if !ok || time.Until(deadline) > 20*time.Second {
			t.Fatal("default boundary has no bounded deadline")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent || dependency == nil || dependency.Err() != context.Canceled {
		t.Fatal("completed handler retained a live dependency context")
	}
}
