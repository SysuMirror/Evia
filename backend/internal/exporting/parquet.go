package exporting

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/parquet-go/parquet-go"
)

// parquetCell converts one analysis result cell into a value compatible with
// the parquet node selected for its column.
type parquetCell func(any) (any, error)

func parquetUnsupported(detail string) error {
	return fault.New(fault.Invalid, "EXPORT_PARQUET_UNSUPPORTED", "结果包含无法序列化为 Parquet 的类型："+detail)
}

// renderParquet materializes the tabular result as a strongly typed Parquet
// file. Column types follow analysis.Column.Type: booleans, integers and
// numbers become physical typed columns; strings and datetimes stay UTF-8 so
// their lexical form (including RFC3339 datetimes) is preserved; structured
// cells become either a typed LIST or a compact JSON string. Cells that cannot
// satisfy the column type fail closed instead of being silently truncated.
func renderParquet(result analysis.Result) ([]byte, error) {
	if len(result.Columns) == 0 {
		return nil, parquetUnsupported("结果没有可导出的表格列")
	}
	root := parquet.Group{}
	converters := make(map[string]parquetCell, len(result.Columns))
	for _, column := range result.Columns {
		node, converter, err := planParquetColumn(column, result.Rows)
		if err != nil {
			return nil, err
		}
		root[column.Key] = parquet.Optional(node)
		converters[column.Key] = converter
	}
	schema := parquet.NewSchema("result", root)
	var buffer bytes.Buffer
	writer := parquet.NewGenericWriter[map[string]any](&buffer, schema)
	for _, row := range result.Rows {
		record := make(map[string]any, len(result.Columns))
		for _, column := range result.Columns {
			value, err := converters[column.Key](row[column.Key])
			if err != nil {
				return nil, err
			}
			record[column.Key] = value
		}
		if _, err := writer.Write([]map[string]any{record}); err != nil {
			return nil, parquetUnsupported(err.Error())
		}
	}
	if err := writer.Close(); err != nil {
		return nil, parquetUnsupported(err.Error())
	}
	return buffer.Bytes(), nil
}

// planParquetColumn selects the parquet node and cell converter for a column.
// Generic array columns inspect their cells once and fall back to a UTF-8 JSON
// column when element types are not homogeneous enough for a typed LIST.
func planParquetColumn(column analysis.Column, rows []map[string]any) (parquet.Node, parquetCell, error) {
	switch column.Type {
	case "boolean":
		return parquet.Leaf(parquet.BooleanType), parquetBoolCell, nil
	case "integer":
		return parquet.Int(64), parquetIntCell, nil
	case "number":
		return parquet.Leaf(parquet.DoubleType), parquetFloatCell, nil
	case "string", "datetime":
		return parquet.String(), parquetStringCell, nil
	case "object", "json":
		return parquet.String(), parquetJSONCell, nil
	}
	elementType := ""
	if strings.HasPrefix(column.Type, "array:") {
		elementType = column.Type[len("array:"):]
	} else if column.Type == "array" {
		elementType = inferArrayElementType(column.Key, rows)
	} else {
		return nil, nil, parquetUnsupported("未知列类型 " + column.Type)
	}
	switch elementType {
	case "boolean":
		return parquet.List(parquet.Leaf(parquet.BooleanType)), parquetListCell(parquetBoolCell), nil
	case "integer":
		return parquet.List(parquet.Int(64)), parquetListCell(parquetIntCell), nil
	case "number":
		return parquet.List(parquet.Leaf(parquet.DoubleType)), parquetListCell(parquetFloatCell), nil
	case "string":
		return parquet.List(parquet.String()), parquetListCell(parquetStringCell), nil
	default:
		// Heterogeneous or structurally unknown arrays keep their compact
		// JSON representation in a UTF-8 column rather than faking a typed LIST.
		return parquet.String(), parquetJSONCell, nil
	}
}

func inferArrayElementType(key string, rows []map[string]any) string {
	elementType := ""
	for _, row := range rows {
		elements, ok := row[key].([]any)
		if !ok {
			continue
		}
		for _, element := range elements {
			kind := parquetScalarKind(element)
			if kind == "" {
				return ""
			}
			if elementType == "" {
				elementType = kind
			} else if elementType != kind {
				return ""
			}
		}
	}
	return elementType
}

func parquetScalarKind(value any) string {
	switch value.(type) {
	case nil:
		return ""
	case bool:
		return "boolean"
	case string, json.Number:
		if _, ok := value.(json.Number); ok {
			if _, err := value.(json.Number).Int64(); err == nil {
				return "integer"
			}
			return "number"
		}
		return "string"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32:
		return "integer"
	case uint64:
		if value.(uint64) > 1<<63-1 {
			return ""
		}
		return "integer"
	case float32, float64:
		return "number"
	default:
		return ""
	}
}

func parquetBoolCell(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if v, ok := value.(bool); ok {
		return v, nil
	}
	return nil, parquetUnsupported("布尔列遇到非布尔值")
}

func parquetIntCell(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return nil, parquetUnsupported("整数列遇到非整数值 " + v.String())
		}
		return parsed, nil
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v > 1<<63-1 {
			return nil, parquetUnsupported("整数超出 INT64 上限")
		}
		return int64(v), nil
	default:
		return nil, parquetUnsupported("整数列遇到不支持的 Go 类型")
	}
}

func parquetFloatCell(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return nil, parquetUnsupported("数值列遇到不可解析值 " + v.String())
		}
		return parsed, nil
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return nil, parquetUnsupported("数值列遇到不支持的 Go 类型")
	}
}

func parquetStringCell(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case json.Number:
		// Preserve the exact lexical form emitted upstream, mirroring CSV.
		return v.String(), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		return nil, parquetUnsupported("字符串列遇到结构化值")
	}
}

func parquetJSONCell(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if v, ok := value.(string); ok {
		return v, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, parquetUnsupported("结构化值无法编码为 JSON")
	}
	return string(encoded), nil
}

func parquetListCell(convert parquetCell) parquetCell {
	return func(value any) (any, error) {
		if value == nil {
			return nil, nil
		}
		var elements []any
		switch v := value.(type) {
		case []any:
			elements = v
		case []string:
			elements = make([]any, len(v))
			for i, item := range v {
				elements[i] = item
			}
		case []int:
			elements = make([]any, len(v))
			for i, item := range v {
				elements[i] = item
			}
		case []int64:
			elements = make([]any, len(v))
			for i, item := range v {
				elements[i] = item
			}
		case []float64:
			elements = make([]any, len(v))
			for i, item := range v {
				elements[i] = item
			}
		case []bool:
			elements = make([]any, len(v))
			for i, item := range v {
				elements[i] = item
			}
		default:
			return nil, parquetUnsupported("LIST 列遇到非数组值")
		}
		converted := make([]any, 0, len(elements))
		for _, element := range elements {
			item, err := convert(element)
			if err != nil {
				return nil, err
			}
			converted = append(converted, item)
		}
		return converted, nil
	}
}
