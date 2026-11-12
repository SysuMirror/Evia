package exporting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func sampleResult() analysis.Result {
	return analysis.Result{
		Columns: []analysis.Column{
			{Key: "subject", Type: "string"},
			{Key: "count", Type: "integer"},
			{Key: "ratio", Type: "number"},
			{Key: "active", Type: "boolean"},
			{Key: "tags", Type: "array"},
		},
		Rows: []map[string]any{
			{
				"subject": "user,1",
				"count":   json.Number("42"),
				"ratio":   json.Number("1e2"),
				"active":  true,
				"tags":    []any{"a", "b"},
			},
			{
				"subject": "user2",
				"count":   json.Number("-0.0"),
				"ratio":   json.Number("1.00"),
				"active":  false,
				"tags":    nil,
			},
		},
	}
}

func TestRenderCSVPreservesColumnOrderAndLexicalNumbers(t *testing.T) {
	t.Parallel()
	payload, err := renderCSV(sampleResult())
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d: %q", len(lines), text)
	}
	if lines[0] != "subject,count,ratio,active,tags" {
		t.Fatalf("header order wrong: %q", lines[0])
	}
	// json.Number lexical forms must survive without re-serialization.
	if !strings.Contains(lines[1], "42") || !strings.Contains(lines[1], "1e2") {
		t.Fatalf("number lexical form lost: %q", lines[1])
	}
	if !strings.Contains(lines[2], "-0.0") || !strings.Contains(lines[2], "1.00") {
		t.Fatalf("decimal lexical form lost: %q", lines[2])
	}
	// Commas trigger RFC 4180 quoting; nil cells are empty strings.
	if !strings.HasPrefix(lines[1], "\"user,1\"") {
		t.Fatalf("comma cell must be quoted: %q", lines[1])
	}
	if !strings.HasSuffix(lines[2], ",") {
		t.Fatalf("nil cell must render empty: %q", lines[2])
	}
	if !strings.Contains(lines[1], `"[""a"",""b""]"`) {
		t.Fatalf("array cell must be compact quoted JSON: %q", lines[1])
	}
}

func TestRenderCSVRejectsEmptyColumnsAndUnknownTypes(t *testing.T) {
	t.Parallel()
	if _, err := renderCSV(analysis.Result{}); err == nil {
		t.Fatal("column-less result must not render as CSV")
	}
	if _, err := renderCSV(analysis.Result{
		Columns: []analysis.Column{{Key: "weird", Type: "string"}},
		Rows:    []map[string]any{{"weird": make(chan int)}},
	}); err == nil {
		t.Fatal("unsupported cell type must be rejected")
	}
}

func TestNormalizeFormatVocabulary(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":     FormatJSON,
		"json": FormatJSON,
		"csv":  FormatCSV,
	}
	for input, want := range cases {
		got, err := NormalizeFormat(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeFormat(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := NormalizeFormat("parquet"); err == nil {
		t.Fatal("unknown format must be rejected")
	}
}

func TestFileArtifactStoreWritesFormatSpecificFiles(t *testing.T) {
	t.Parallel()
	store, err := NewFileArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	jobID := "44444444-4444-4444-8444-444444444444"
	csvArtifact, err := store.Write(context.Background(), jobID, FormatCSV, sampleResult())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(csvArtifact.Path) != ".csv" {
		t.Fatalf("csv artifact must use .csv extension: %s", csvArtifact.Path)
	}
	payload, err := os.ReadFile(csvArtifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(payload), "subject,count,ratio,active,tags\n") {
		t.Fatalf("csv payload header wrong: %q", payload)
	}
	if csvArtifact.Rows != 2 || csvArtifact.SHA256 == "" || csvArtifact.Bytes != int64(len(payload)) {
		t.Fatalf("artifact metadata wrong: %+v", csvArtifact)
	}
	jsonArtifact, err := store.Write(context.Background(), jobID, FormatJSON, sampleResult())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(jsonArtifact.Path) != ".json" {
		t.Fatalf("json artifact must use .json extension: %s", jsonArtifact.Path)
	}
}
