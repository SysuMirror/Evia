package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

func httpClient(concurrency int) *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:           nil, // Never route private credentials through environment proxies.
			DialContext:     (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxConnsPerHost: concurrency, MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency,
			IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 8 * time.Second,
		},
	}
}

func sendBatch(ctx context.Context, client *http.Client, c Config, b batch, m *recorder) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.APIURL, "/")+"/v1/collect", bytes.NewReader(b.body))
	// GetBody permits implicit net/http retries in some transport failure cases.
	// Disable it: every request recorded here is exactly one application attempt.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.SourceKind == ingestion.SourceKindServer {
		req.Header.Set("Authorization", "Bearer "+c.ServerSecret)
	} else {
		req.Header.Set("Origin", c.Origin)
	}
	start := time.Now()
	resp, err := client.Do(req)
	var raw []byte
	status := 0
	if err == nil {
		status = resp.StatusCode
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 65537))
		if closeErr := resp.Body.Close(); err == nil {
			err = closeErr
		}
	}
	duration := time.Since(start)
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := m.counts(b.replay)
	counts.Attempted += b.count
	m.report.Requests++
	m.report.RequestBytes += int64(len(b.body))
	m.report.HTTPStatuses[status]++
	m.latency(duration)
	if err != nil || len(raw) > 65536 {
		counts.Unexpected += b.count
		m.failure(b, 0, status, "TRANSPORT_OR_RESPONSE_FAILURE")
		return
	}
	switch {
	case status == http.StatusTooManyRequests:
		counts.RateLimited += b.count
		m.failure(b, 0, status, "HTTP_RATE_LIMITED")
	case status >= 500 && status <= 599:
		counts.ServerErrors += b.count
		m.failure(b, 0, status, "HTTP_SERVER_ERROR")
	case status >= 400 && status <= 499:
		counts.PermanentRejected += b.count
		m.failure(b, 0, status, "HTTP_PERMANENT_REJECTION")
	case status != http.StatusAccepted:
		counts.Unexpected += b.count
		m.failure(b, 0, status, "UNEXPECTED_HTTP_STATUS")
	default:
		items, valid := parseReceipt(raw, b)
		if !valid {
			counts.Unexpected += b.count
			m.failure(b, 0, status, "INVALID_RECEIPT")
			return
		}
		for i, item := range items {
			if item.Status == "accepted" {
				counts.Accepted++
				continue
			}
			if *item.Error.Retryable {
				counts.RetryableRejected++
			} else {
				counts.PermanentRejected++
			}
			m.failure(b, i, status, item.Error.Code)
		}
	}
}
