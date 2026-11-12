package observe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDrainGateStartsReadyAndTransitionsOneWay(t *testing.T) {
	gate := NewDrainGate()
	if !gate.Ready() || gate.Draining() {
		t.Fatal("new gate must be ready")
	}
	gate.MarkDraining()
	if gate.Ready() || !gate.Draining() {
		t.Fatal("gate did not transition to draining")
	}
	gate.MarkDraining() // idempotent, must stay draining
	if gate.Ready() {
		t.Fatal("draining transition reversed")
	}
}

func TestNilGateStaysReadyForSafeZeroValueUse(t *testing.T) {
	var gate *DrainGate
	if !gate.Ready() || gate.Draining() {
		t.Fatal("nil gate should behave as ready")
	}
	gate.MarkDraining() // must not panic
}

func TestReadinessHandlerFailsImmediatelyWhenDraining(t *testing.T) {
	gate := NewDrainGate()
	probeCalled := 0
	handler := gate.ReadinessHandler(func(context.Context) error {
		probeCalled++
		return nil
	})

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusNoContent {
		t.Fatalf("ready probe = %d, want 204", ready.Code)
	}

	gate.MarkDraining()
	draining := httptest.NewRecorder()
	handler.ServeHTTP(draining, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if draining.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining probe = %d, want 503", draining.Code)
	}
	if draining.Header().Get("Retry-After") != "5" {
		t.Fatal("draining response should advise retry-after")
	}
	if probeCalled != 1 {
		t.Fatal("dependency probe must not run while draining")
	}
}

func TestReadinessHandlerMapsProbeFailureTo503(t *testing.T) {
	gate := NewDrainGate()
	handler := gate.ReadinessHandler(func(context.Context) error {
		return errors.New("dependency down")
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("failing dependency probe = %d, want 503", recorder.Code)
	}
}

func TestReadinessHandlerWithNilProbeOnlyReflectsGate(t *testing.T) {
	gate := NewDrainGate()
	handler := gate.ReadinessHandler(nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("nil probe ready = %d, want 204", recorder.Code)
	}
	gate.MarkDraining()
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil probe draining = %d, want 503", recorder.Code)
	}
}
