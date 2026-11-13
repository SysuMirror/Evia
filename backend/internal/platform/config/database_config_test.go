package config

import "testing"

func TestValidateDatabaseURLAccepts(t *testing.T) {
	accepted := []string{
		"postgres://example",
		"postgresql://example",
		"postgres://evia:secret@127.0.0.1:55432/evia_fullstack?sslmode=disable",
		"postgres://127.0.0.1:5432/evia?sslmode=disable",
		"postgres://pg-1:5432,pg-2:5433/evia?target_session_attrs=read-write",
		"postgresql://pg-1,pg-2,pg-3/evia?target_session_attrs=read-write",
		"postgres://pg-1:5432,pg-2:5432/evia?sslmode=require&target_session_attrs=read-write",
		"postgres://10.0.0.1:5432,10.0.0.2:5432/evia?target_session_attrs=read-write",
		"postgres://evia:secret@pg-1:5432,pg-2:5432/evia?sslmode=verify-full&target_session_attrs=read-write",
		"postgres://127.0.0.1:5432/evia?sslmode=disable&target_session_attrs=read-write",
		"postgres://[::1]:5432/evia",
	}
	for _, raw := range accepted {
		if err := ValidateDatabaseURL(raw); err != nil {
			t.Fatalf("valid DSN rejected %q: %v", raw, err)
		}
	}
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		raw := "postgres://127.0.0.1:5432/evia?sslmode=" + mode
		if err := ValidateDatabaseURL(raw); err != nil {
			t.Fatalf("sslmode %s rejected: %v", mode, err)
		}
	}
}

func TestValidateDatabaseURLRejects(t *testing.T) {
	rejected := map[string]string{
		"wrong scheme":              "mysql://127.0.0.1:5432/evia",
		"missing scheme":            "127.0.0.1:5432/evia",
		"fragment":                  "postgres://127.0.0.1:5432/evia#frag",
		"escaped path":              "postgres://127.0.0.1:5432/evi%61",
		"empty host":                "postgres:///evia",
		"zero port":                 "postgres://127.0.0.1:0/evia",
		"oversized port":            "postgres://127.0.0.1:65536/evia",
		"bare unbracketed ipv6":     "postgres://::1:5432/evia",
		"host with space":           "postgres://pg 1:5432/evia",
		"host too long":             "postgres://" + repeat("a", 254) + ":5432/evia",
		"nested database path":      "postgres://127.0.0.1:5432/evia/public",
		"unknown query key":         "postgres://127.0.0.1:5432/evia?connect_timeout=5",
		"invalid sslmode":           "postgres://127.0.0.1:5432/evia?sslmode=paranoid",
		"read only session attrs":   "postgres://pg-1,pg-2/evia?target_session_attrs=read-only",
		"primary session attrs":     "postgres://pg-1,pg-2/evia?target_session_attrs=primary",
		"any session attrs":         "postgres://pg-1,pg-2/evia?target_session_attrs=any",
		"repeated query parameter":  "postgres://127.0.0.1:5432/evia?sslmode=disable&sslmode=require",
		"malformed query pair":      "postgres://127.0.0.1:5432/evia?sslmode",
		"multi host bracketed ipv6": "postgres://[::1]:5432,[::2]:5432/evia?target_session_attrs=read-write",
		"multi host mixed bracket":  "postgres://[::1]:5432,pg-2:5432/evia?target_session_attrs=read-write",
	}
	for name, raw := range rejected {
		if err := ValidateDatabaseURL(raw); err == nil {
			t.Fatalf("%s accepted invalid DSN %q", name, raw)
		}
	}
}

func TestValidateDatabaseURLHostBound(t *testing.T) {
	hosts := ""
	for i := 0; i < 9; i++ {
		if i > 0 {
			hosts += ","
		}
		hosts += "pg-1:5432"
	}
	if err := ValidateDatabaseURL("postgres://" + hosts + "/evia"); err == nil {
		t.Fatal("nine-host DSN must exceed the closed host bound")
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
