// Package canonical defines the shared stable JSON representation for immutable
// metadata digests and idempotent request identities.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

const MaxInteger int64 = 9007199254740991

// CanonicalJSON uses deterministic sorted object keys and rejects unsupported JSON
// numbers. It is used for both request identity and immutable manifest hashes.
func JSON(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, err = canonicalValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func canonicalValue(v any) (any, error) {
	switch value := v.(type) {
	case json.Number:
		n, err := value.Float64()
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > float64(MaxInteger) {
			return nil, fmt.Errorf("number outside supported JSON range")
		}
		if n == 0 {
			n = 0
		}
		return n, nil
	case map[string]any:
		for k, item := range value {
			normalized, err := canonicalValue(item)
			if err != nil {
				return nil, err
			}
			value[k] = normalized
		}
		return value, nil
	case []any:
		for i, item := range value {
			normalized, err := canonicalValue(item)
			if err != nil {
				return nil, err
			}
			value[i] = normalized
		}
		return value, nil
	default:
		return value, nil
	}
}
func Hash(v any) (string, error) {
	b, e := JSON(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
