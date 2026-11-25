package analysis

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
)

const (
	journeyCursorPrefix  = "jt1."
	journeyCursorVersion = 1
	maxJourneyCursorLen  = 2048
)

// JourneyCursorPosition is the exact total-order keyset position of the last
// emitted journey event in ClickHouse.
type JourneyCursorPosition struct {
	EventTimeNs int64
	SessionID   string
	TabID       string
	Sequence    uint64
	SourceID    string
	EventID     string
}

type journeyCursorPayload struct {
	Version     int    `json:"v"`
	Fingerprint string `json:"fp"`
	EventTimeNs int64  `json:"t"`
	SessionID   string `json:"s"`
	TabID       string `json:"b"`
	Sequence    uint64 `json:"q"`
	SourceID    string `json:"r"`
	EventID     string `json:"e"`
}

func journeyCursorFingerprint(scope Scope, r Request) (string, error) {
	eventKeys := append([]string(nil), r.EventKeys...)
	slices.Sort(eventKeys)
	eventVersions := append([]EventVersion(nil), r.EventVersions...)
	slices.SortFunc(eventVersions, func(a, b EventVersion) int {
		if a.EventKey != b.EventKey {
			return strings.Compare(a.EventKey, b.EventKey)
		}
		if a.SchemaVersion < b.SchemaVersion {
			return -1
		}
		if a.SchemaVersion > b.SchemaVersion {
			return 1
		}
		return 0
	})
	digest, err := canonical.Hash(map[string]any{
		"project_id":      scope.ProjectID,
		"environment":     scope.Environment,
		"kind":            r.Kind,
		"subject":         r.Subject,
		"identity_filter": r.IdentityFilter,
		"time_range": map[string]string{
			"from": r.TimeRange.From.UTC().Format(time.RFC3339Nano),
			"to":   r.TimeRange.To.UTC().Format(time.RFC3339Nano),
		},
		"event_keys":     eventKeys,
		"event_versions": eventVersions,
		"filter":         normalizeJourneyFilter(r.Filter),
	})
	if err != nil {
		return "", err
	}
	return digest[:32], nil
}

func normalizeJourneyFilter(f *Filter) *Filter {
	if f == nil {
		return nil
	}
	clone := &Filter{Op: f.Op, Value: f.Value}
	if f.Field != nil {
		fieldCopy := *f.Field
		fieldCopy.ResolvedType = ""
		clone.Field = &fieldCopy
	}
	if len(f.Args) > 0 {
		clone.Args = make([]*Filter, len(f.Args))
		for i, arg := range f.Args {
			clone.Args[i] = normalizeJourneyFilter(arg)
		}
	}
	return clone
}

// EncodeJourneyCursor serializes a scope-bound engine keyset position for
// lossless journey timeline continuation.
func EncodeJourneyCursor(scope Scope, r Request, pos JourneyCursorPosition) (string, error) {
	if pos.EventTimeNs < 0 || pos.SourceID == "" || pos.EventID == "" {
		return "", Invalid("INVALID_QUERY_CURSOR", "行为链续拉游标位置无效", "/cursor")
	}
	fp, err := journeyCursorFingerprint(scope, r)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(journeyCursorPayload{
		Version:     journeyCursorVersion,
		Fingerprint: fp,
		EventTimeNs: pos.EventTimeNs,
		SessionID:   pos.SessionID,
		TabID:       pos.TabID,
		Sequence:    pos.Sequence,
		SourceID:    pos.SourceID,
		EventID:     pos.EventID,
	})
	if err != nil {
		return "", err
	}
	token := journeyCursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if len(token) > maxJourneyCursorLen {
		return "", Invalid("INVALID_QUERY_CURSOR", "行为链续拉游标超过长度预算", "/cursor")
	}
	return token, nil
}

// ParseJourneyCursor validates the syntax and field bounds of an engine-level
// journey continuation cursor.
func ParseJourneyCursor(raw string) (journeyCursorPayload, error) {
	invalid := Invalid("INVALID_QUERY_CURSOR", "行为链续拉游标无效", "/cursor")
	if raw == "" || len(raw) > maxJourneyCursorLen || !strings.HasPrefix(raw, journeyCursorPrefix) {
		return journeyCursorPayload{}, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, journeyCursorPrefix))
	if err != nil || len(decoded) == 0 {
		return journeyCursorPayload{}, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var payload journeyCursorPayload
	if err = decoder.Decode(&payload); err != nil {
		return journeyCursorPayload{}, invalid
	}
	var trailing any
	if err = decoder.Decode(&trailing); err == nil {
		return journeyCursorPayload{}, invalid
	}
	if payload.Version != journeyCursorVersion || !isLowerHex32(payload.Fingerprint) || payload.EventTimeNs < 0 {
		return journeyCursorPayload{}, invalid
	}
	if payload.SourceID == "" || payload.EventID == "" {
		return journeyCursorPayload{}, invalid
	}
	for _, s := range []string{payload.SessionID, payload.TabID, payload.SourceID, payload.EventID} {
		if len(s) > 256 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return journeyCursorPayload{}, invalid
		}
	}
	return payload, nil
}

// ValidateJourneyCursorScope ensures the cursor matches the active project,
// environment, subject, identity filter, time range, event selection, and filter.
func ValidateJourneyCursorScope(scope Scope, r Request) (JourneyCursorPosition, error) {
	payload, err := ParseJourneyCursor(r.Cursor)
	if err != nil {
		return JourneyCursorPosition{}, err
	}
	if payload.EventTimeNs < r.TimeRange.From.UTC().UnixNano() || payload.EventTimeNs >= r.TimeRange.To.UTC().UnixNano() {
		return JourneyCursorPosition{}, Invalid("INVALID_QUERY_CURSOR", "行为链续拉游标超出查询时间范围", "/cursor")
	}
	expected, err := journeyCursorFingerprint(scope, r)
	if err != nil {
		return JourneyCursorPosition{}, err
	}
	if subtle.ConstantTimeCompare([]byte(payload.Fingerprint), []byte(expected)) != 1 {
		return JourneyCursorPosition{}, Invalid("INVALID_QUERY_CURSOR", "行为链续拉游标与当前查询条件不匹配", "/cursor")
	}
	return JourneyCursorPosition{
		EventTimeNs: payload.EventTimeNs,
		SessionID:   payload.SessionID,
		TabID:       payload.TabID,
		Sequence:    payload.Sequence,
		SourceID:    payload.SourceID,
		EventID:     payload.EventID,
	}, nil
}

func isLowerHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func uint64Value(v any) (uint64, bool) {
	switch value := v.(type) {
	case uint64:
		return value, true
	case int64:
		if value < 0 {
			return 0, false
		}
		return uint64(value), true
	case json.Number:
		parsed, err := strconv.ParseUint(value.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
