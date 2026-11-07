package config

import (
	"strings"
	"testing"
)

func TestPublicOriginUsesBrowserCanonicalForm(t *testing.T) {
	for _, tt := range []struct{ input, expected string }{
		{"https://EVIA.Example:443", "https://evia.example"},
		{"https://EVIA.Example:0443", "https://evia.example"},
		{"http://LOCALHOST:80", "http://localhost"},
		{"https://EVIA.Example:8443", "https://evia.example:8443"},
		{"https://[2001:0DB8:0:0:0:0:0:1]:443", "https://[2001:db8::1]"},
		{"http://[::1]:8080", "http://[::1]:8080"},
		{"https://[::ffff:127.0.0.1]:443", "https://[::ffff:7f00:1]"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			values := map[string]string{"EVIA_DATABASE_URL": "postgres://example", "EVIA_PUBLIC_ORIGIN": tt.input, "EVIA_ALLOW_INSECURE_HTTP": "true", "EVIA_CURSOR_KEY": strings.Repeat("ab", 32)}
			c, err := Read(func(key string) string { return values[key] })
			if err != nil || c.Origin != tt.expected {
				t.Fatalf("origin %q, want %q, err=%v", c.Origin, tt.expected, err)
			}
		})
	}
}
func TestPublicOriginRejectsNonOriginComponents(t *testing.T) {
	for _, raw := range []string{
		"https://user:synthetic@example.test", "https://example.test/", "https://example.test/path",
		"https://example.test?", "https://example.test?query=value", "https://example.test#", "https://example.test#fragment",
		"https://example.test:", "https://example.test:65536", "https://例子.test", "http://127.1", "http://0x7f000001", "http://0177.0.0.1",
	} {
		if _, err := canonicalOrigin(raw); err == nil {
			t.Fatalf("accepted non-origin value %q", raw)
		}
	}
}
