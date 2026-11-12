package observe

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// DrainGate coordinates connection draining during a rolling deployment.
//
// A multi-replica deployment removes a process from the load balancer only
// after its readiness probe starts failing. On SIGTERM the process must
// therefore flip readiness to "not ready" immediately, keep liveness up while
// it drains, and only then close the listener. Without that ordering the
// load balancer keeps forwarding new requests to a process that is already
// shutting down and clients see connection resets.
//
// The gate is safe for concurrent use and its transitions are one-way: once
// draining starts, readiness never flips back within the same process life.
type DrainGate struct {
	draining atomic.Bool
}

// NewDrainGate returns a gate in the ready state.
func NewDrainGate() *DrainGate {
	return &DrainGate{}
}

// MarkDraining moves the process into connection-draining mode. It is
// idempotent: repeated signals after the first do not change the state.
func (g *DrainGate) MarkDraining() {
	if g != nil {
		g.draining.Store(true)
	}
}

// Draining reports whether shutdown has begun.
func (g *DrainGate) Draining() bool {
	return g != nil && g.draining.Load()
}

// Ready reports whether the process should still receive traffic.
func (g *DrainGate) Ready() bool {
	return !g.Draining()
}

// ReadinessHandler answers /readyz with the deployment-visible readiness
// state. A draining process fails immediately without touching dependencies,
// so the load balancer removes it even when a downstream is unreachable. A
// ready process additionally runs probe, which must return nil for a 204; any
// probe failure is a 503 so traffic is shifted away during dependency outages.
func (g *DrainGate) ReadinessHandler(probe func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.Ready() {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if probe != nil {
			check, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			if err := probe(check); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
