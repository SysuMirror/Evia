package exporting

import (
	"bytes"
	"encoding/json"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// renderNDJSON materializes the tabular result as newline-delimited JSON:
// each result row is one compact, self-describing JSON object terminated by a
// line feed. json.Marshal keeps json.Number lexical form and the same escaping
// as the full-document JSON export, so integers and high-precision decimals
// survive without re-parsing. There is no envelope or metadata block; an empty
// result yields an empty (zero-byte) artifact, a valid empty NDJSON stream for
// downstream pipelines.
func renderNDJSON(result analysis.Result) ([]byte, error) {
	var buffer bytes.Buffer
	for _, row := range result.Rows {
		payload, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		buffer.Write(payload)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}
