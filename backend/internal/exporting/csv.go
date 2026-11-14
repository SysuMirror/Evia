package exporting

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strconv"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// renderCSV materializes the tabular result in the declared column order.
// json.Number values keep their lexical form instead of being re-parsed, so
// integers and high-precision decimals survive the format conversion byte for
// byte. Structured cells (arrays/objects) become compact JSON strings.
// Native Go numeric widths and concrete slice types emitted by the in-process
// evaluators render directly; json.Number still wins for lexical fidelity.
func renderCSV(result analysis.Result) ([]byte, error) {
	if len(result.Columns) == 0 {
		return nil, fault.New(fault.Invalid, "EXPORT_CSV_UNSUPPORTED", "结果没有可导出的表格列")
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	header := make([]string, len(result.Columns))
	for i, column := range result.Columns {
		header[i] = column.Key
	}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, row := range result.Rows {
		record := make([]string, len(result.Columns))
		for i, column := range result.Columns {
			cell, err := csvCell(row[column.Key])
			if err != nil {
				return nil, err
			}
			record[i] = cell
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func csvCell(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case json.Number:
		// Preserve the exact lexical representation emitted by ClickHouse.
		return v.String(), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(v), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case []any, map[string]any, []string, []int, []int64, []float64, []bool:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	default:
		return "", fault.New(fault.Invalid, "EXPORT_CSV_UNSUPPORTED", "结果包含无法序列化为 CSV 的单元格类型")
	}
}
