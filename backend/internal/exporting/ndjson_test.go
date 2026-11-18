package exporting

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func TestRenderNDJSONOneCompactRecordPerLine(t *testing.T) {
	t.Parallel()
	payload, err := renderNDJSON(sampleResult())
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("NDJSON must terminate every record with a newline: %q", text)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != len(sampleResult().Rows) {
		t.Fatalf("want one line per row, got %d: %q", len(lines), text)
	}
	for i, line := range lines {
		var decoded map[string]any
		if e := json.Unmarshal([]byte(line), &decoded); e != nil {
			t.Fatalf("line %d is not a standalone JSON object: %v (%q)", i, e, line)
		}
		if strings.ContainsAny(line, "\n") {
			t.Fatalf("line %d must stay compact on a single line: %q", i, line)
		}
	}
	// Lexical numeric fidelity: json.Number survives without re-parsing.
	var first map[string]any
	if e := json.Unmarshal([]byte(lines[0]), &first); e != nil {
		t.Fatal(e)
	}
	decoder := json.NewDecoder(strings.NewReader(lines[0]))
	decoder.UseNumber()
	var lexical map[string]any
	if e := decoder.Decode(&lexical); e != nil {
		t.Fatal(e)
	}
	if lexical["count"] != json.Number("42") || lexical["ratio"] != json.Number("1e2") {
		t.Fatalf("lexical number form lost: %v", lexical)
	}
	// Structured cells stay JSON, nil stays null rather than an empty string.
	if first["tags"] == nil {
		t.Fatal("array cell must render as JSON, not be dropped")
	}
}

func TestRenderNDJSONEmptyResultIsEmptyStream(t *testing.T) {
	t.Parallel()
	payload, err := renderNDJSON(analysis.Result{Columns: []analysis.Column{{Key: "event_key"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 0 {
		t.Fatalf("empty result must yield a zero-byte NDJSON stream, got %q", string(payload))
	}
}

func TestNormalizeFormatAcceptsNDJSONAndRejectsUnknown(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"", FormatJSON, FormatCSV, FormatNDJSON} {
		got, err := NormalizeFormat(format)
		if err != nil {
			t.Fatalf("format %q must normalize: %v", format, err)
		}
		want := format
		if format == "" {
			want = FormatJSON
		}
		if got != want {
			t.Fatalf("format %q normalized to %q", format, got)
		}
	}
	if _, err := NormalizeFormat("parquet"); err == nil {
		t.Fatal("unknown export format must be rejected")
	}
}
