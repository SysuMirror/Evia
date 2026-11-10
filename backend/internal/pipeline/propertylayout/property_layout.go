// Package propertylayout derives a lossless read layout for event properties.
//
// The original properties document stays authoritative. This package only
// produces a derived map of encoded top-level keys to their untouched JSON
// tokens, so a future columnar reader can select one property without reading
// the whole document while preserving lexical numeric text (1e2, 1.00, long
// decimals and signed zero must not be re-encoded through a numeric type).
package propertylayout

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// KeyPrefix namespaces every encoded property column so it can never collide
// with a fixed event-facts column.
const KeyPrefix = "p_"

// MaxTopLevelProperties mirrors the ingestion contract's property cap.
const MaxTopLevelProperties = 50

// EncodeKey maps a decoded top-level property key to a ClickHouse-safe,
// reversible identifier: the prefix followed by the uppercase hexadecimal of
// each UTF-8 byte. Dots and other characters that conflict with nested paths
// cannot appear, and two distinct keys always produce distinct identifiers.
func EncodeKey(key string) string {
	return KeyPrefix + strings.ToUpper(hex.EncodeToString([]byte(key)))
}

// DecodeKey reverses EncodeKey, returning an error for a malformed identifier.
func DecodeKey(encoded string) (string, error) {
	if !strings.HasPrefix(encoded, KeyPrefix) {
		return "", fmt.Errorf("encoded key missing %q prefix", KeyPrefix)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(encoded, KeyPrefix))
	if err != nil {
		return "", fmt.Errorf("encoded key is not valid hex: %w", err)
	}
	return string(raw), nil
}

// DeriveMap parses a canonical properties object and returns one entry per
// top-level property keyed by EncodeKey. Values are json.RawMessage slices of
// the exact input tokens; no number, string, array or object is re-serialized,
// which keeps lexical numeric text lossless.
//
// The input is expected to be the normalized object produced by the ingestion
// pipeline. Duplicate decoded keys and duplicate encoded identifiers are
// rejected defensively rather than silently overwritten.
func DeriveMap(propertiesJSON []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(propertiesJSON)) == 0 {
		return nil, fmt.Errorf("properties document is empty")
	}
	keys, err := topLevelKeys(propertiesJSON)
	if err != nil {
		return nil, err
	}
	if len(keys) > MaxTopLevelProperties {
		return nil, fmt.Errorf("too many top-level properties: %d", len(keys))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(propertiesJSON, &raw); err != nil {
		return nil, fmt.Errorf("properties document is not a JSON object: %w", err)
	}
	if len(raw) != len(keys) {
		return nil, fmt.Errorf("property key count mismatch after decode")
	}
	derived := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		token, ok := raw[key]
		if !ok {
			return nil, fmt.Errorf("missing raw token for property %q", key)
		}
		encoded := EncodeKey(key)
		if _, exists := derived[encoded]; exists {
			return nil, fmt.Errorf("duplicate encoded property key %q", encoded)
		}
		// Copy the token so callers cannot mutate the decoder's backing array.
		derived[encoded] = append(json.RawMessage(nil), token...)
	}
	return derived, nil
}

// topLevelKeys walks only the top-level object with the streaming token API to
// detect duplicate decoded keys without unmarshalling values into typed Go
// representations.
func topLevelKeys(propertiesJSON []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(propertiesJSON))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("cannot read properties document: %w", err)
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("properties document must be a JSON object")
	}
	seen := make(map[string]struct{}, MaxTopLevelProperties)
	keys := make([]string, 0, MaxTopLevelProperties)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("cannot read property key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("property key must be a string")
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate property key %q", key)
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		// Skip the value without interpreting it; RawMessage decoding below
		// retains the exact token.
		var skip any
		if err := decoder.Decode(&skip); err != nil {
			return nil, fmt.Errorf("cannot decode value for property %q: %w", key, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("properties object is not terminated")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("properties document has trailing data")
	}
	return keys, nil
}
