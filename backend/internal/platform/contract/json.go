package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Decode rejects ambiguous keys, invalid UTF-8, trailing values and excessive
// nesting before a schema or business policy sees the request.
func Decode(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := decodeValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return v, nil
}

func decodeValue(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("JSON nesting exceeds limit")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if value, ok := token.(string); ok && strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("JSON strings cannot contain U+0000")
		}
		return token, nil
	}
	switch delim {
	case '{':
		result := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok || strings.ContainsRune(name, 0) {
				return nil, fmt.Errorf("invalid object key")
			}
			if _, exists := result[name]; exists {
				return nil, fmt.Errorf("duplicate object key")
			}
			value, err := decodeValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("unterminated object")
		}
		return result, nil
	case '[':
		result := []any{}
		for d.More() {
			value, err := decodeValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("unterminated array")
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter")
	}
}
