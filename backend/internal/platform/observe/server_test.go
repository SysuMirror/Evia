package observe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freeAddr reserves an ephemeral loopback port and releases it so the server
// can bind deterministically without hardcoding a port that may be in use.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitForReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("admin server did not become ready")
}

func TestAdminServerServesHealthAndRoleScopedMetrics(t *testing.T) {
	addr := freeAddr(t)
	registry := NewRegistry("retention-worker")
	server := NewServer(addr, registry)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, server) }()
	waitForReady(t, addr)

	health, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusNoContent {
		t.Fatalf("healthz status = %d", health.StatusCode)
	}

	metricsResponse, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(metricsResponse.Body)
	metricsResponse.Body.Close()
	if metricsResponse.StatusCode != http.StatusOK ||
		!strings.HasPrefix(metricsResponse.Header.Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("metrics response wrong: status=%d type=%q", metricsResponse.StatusCode, metricsResponse.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), `evia_process_info{role="retention-worker",goversion=`) {
		t.Fatalf("role label missing from metrics:\n%s", body)
	}

	request, err := http.NewRequest(http.MethodPost, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	postResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusMethodNotAllowed || postResponse.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("POST /metrics wrong: status=%d allow=%q", postResponse.StatusCode, postResponse.Header.Get("Allow"))
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunServer after cancel returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admin server did not shut down within deadline")
	}
}

func TestAdminServerReportsBindFailure(t *testing.T) {
	addr := freeAddr(t)
	first := NewServer(addr, NewRegistry("first"))
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstDone := make(chan error, 1)
	go func() { firstDone <- RunServer(firstCtx, first) }()
	waitForReady(t, addr)

	second := NewServer(addr, NewRegistry("second"))
	err := RunServer(context.Background(), second)
	if err == nil {
		t.Fatal("second listener on the same address did not fail")
	}
}
