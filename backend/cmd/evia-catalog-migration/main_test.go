package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommandEmitsMigrationForValidCatalog(t *testing.T) {
	dir := t.TempDir()
	catalog := `{"revision":1,"entries":[{"event_key":"page_opened","schema_version":1,"property_key":"score","resolved_type":"integer"}]}`
	path := writeCatalog(t, dir, "catalog.json", catalog)

	var stdout, stderr bytes.Buffer
	code := command([]string{"-catalog", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "ALTER TABLE event_facts_raw ADD COLUMN IF NOT EXISTS") {
		t.Fatalf("output missing raw slot DDL:\n%s", out)
	}
	if !strings.Contains(out, "CREATE OR REPLACE VIEW event_facts AS") {
		t.Fatalf("output missing view recreation:\n%s", out)
	}
	if strings.Contains(out, " AS score") {
		t.Fatal("output must encode property keys rather than emit raw names")
	}
}

func TestCommandRejectsMissingAndInvalidCatalog(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"missing file", []string{"-catalog", filepath.Join(dir, "absent.json")}},
		{"no flag", nil},
		{"positional argument", []string{"unexpected"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := command(tc.args, &stdout, &stderr); code == 0 {
				t.Fatalf("%s expected non-zero exit", tc.name)
			}
			if stdout.Len() != 0 {
				t.Fatalf("%s must not write migration SQL on failure", tc.name)
			}
		})
	}

	invalid := writeCatalog(t, dir, "invalid.json",
		`{"revision":1,"entries":[{"event_key":"BAD","schema_version":1,"property_key":"k","resolved_type":"string"}]}`)
	var stdout, stderr bytes.Buffer
	if code := command([]string{"-catalog", invalid}, &stdout, &stderr); code != 1 || stderr.Len() == 0 {
		t.Fatalf("invalid catalog must fail closed with a message, code=%d stderr=%q", code, stderr.String())
	}
}
