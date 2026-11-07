package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

func boundary(t *testing.T) *Boundary {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	return &Boundary{v, slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestUniformRequestFailures(t *testing.T) {
	b := boundary(t)
	h := b.Middleware(b.Handler(func(w http.ResponseWriter, r *http.Request) error {
		var in wire.ProjectInput
		if e := b.Decode(w, r, "ProjectInput", &in); e != nil {
			return e
		}
		return b.JSON(w, 200, "ProjectInput", in)
	}))
	for _, tc := range []struct {
		name, body, media string
		status            int
	}{
		{"valid", `{"name":"Forum","timezone":"UTC"}`, "application/json", 200},
		{"unknown", `{"name":"Forum","timezone":"UTC","admin":true}`, "application/json", 400},
		{"duplicate", `{"name":"a","name":"b","timezone":"UTC"}`, "application/json", 400},
		{"NUL", `{"name":"valid\u0000suffix","timezone":"UTC"}`, "application/json", 400},
		{"trailing", `{} {}`, "application/json", 400},
		{"media", `{}`, "text/plain", 415},
		{"oversize", strings.Repeat("x", 256*1024+1), "application/json", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.media)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing request id")
			}
			if w.Code >= 400 {
				var p wire.Problem
				if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
					t.Fatal(e)
				}
				if p.RequestID != w.Header().Get("X-Request-ID") {
					t.Fatal("request id mismatch")
				}
			}
		})
	}
}

func TestMalformedQueryFailsBeforeEndpoint(t *testing.T) {
	b := boundary(t)
	called := false
	h := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, query := range []string{"limit=%zz", "limit=1;limit=2", "unexpected=%zz", "limit=5&cursor=%"} {
		t.Run(query, func(t *testing.T) {
			called = false
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/?"+query, nil))
			if called || w.Code != http.StatusBadRequest {
				t.Fatal("malformed query reached endpoint", w.Code)
			}
			var problem wire.Problem
			if e := json.Unmarshal(w.Body.Bytes(), &problem); e != nil {
				t.Fatal(e)
			}
			if problem.Code != "INVALID_QUERY" || problem.RequestID == "" || problem.RequestID != w.Header().Get("X-Request-ID") {
				t.Fatal("unstructured query error", problem)
			}
		})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/?cursor=encoded%3Bvalue", nil))
	if !called || w.Code != http.StatusNoContent {
		t.Fatal("valid encoded query rejected")
	}
}

func TestResponseContractFailsBeforeHeaders(t *testing.T) {
	b := boundary(t)
	h := b.Middleware(b.Handler(func(w http.ResponseWriter, r *http.Request) error {
		return b.JSON(w, 200, "ProjectInput", map[string]any{"name": "x"})
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}

func TestRevisionAndLimitBoundaries(t *testing.T) {
	for _, value := range []string{"", "1", "W/\"1\"", "\"0\"", "\"-1\"", "*"} {
		r := httptest.NewRequest("PATCH", "/", nil)
		r.Header.Set("If-Match", value)
		if _, e := IfMatch(r); e == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	r := httptest.NewRequest("PATCH", "/?limit=200", nil)
	r.Header.Set("If-Match", "\"12\"")
	if n, e := IfMatch(r); e != nil || n != 12 {
		t.Fatal(n, e)
	}
	if n, e := Limit(r); e != nil || n != 200 {
		t.Fatal(n, e)
	}
	for _, q := range []string{"0", "201", "-1", "abc", "1&limit=2"} {
		if _, e := Limit(httptest.NewRequest("GET", "/?limit="+q, nil)); e == nil {
			t.Fatal(q)
		}
	}
}
