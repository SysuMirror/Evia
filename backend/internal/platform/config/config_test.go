package config

import (
	"strings"
	"testing"
)

func TestSecureConfiguration(t *testing.T) {
	values := map[string]string{"EVIA_DATABASE_URL": "postgres://example", "EVIA_PUBLIC_ORIGIN": "https://evia.example", "EVIA_CURSOR_KEY": strings.Repeat("ab", 32)}
	get := func(k string) string { return values[k] }
	if c, e := Read(get); e != nil || !c.SecureCookies {
		t.Fatal(c, e)
	}
	values["EVIA_PUBLIC_ORIGIN"] = "http://localhost:5173"
	if _, e := Read(get); e == nil {
		t.Fatal("insecure default")
	}
	values["EVIA_ALLOW_INSECURE_HTTP"] = "true"
	if c, e := Read(get); e != nil || c.SecureCookies {
		t.Fatal(c, e)
	}
	values["EVIA_PUBLIC_ORIGIN"] = "https://user:password@example.test"
	if _, e := Read(get); e == nil {
		t.Fatal("credential origin accepted")
	}
}
