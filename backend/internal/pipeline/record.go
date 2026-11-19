// Package pipeline implements durable queue and raw fact persistence adapters.
// It does not perform stateful identity resolution or session aggregation.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
)

const MaxRecordBytes = 64 * 1024
const MaxBatchRecords = 500
const maxInteger int64 = canonical.MaxInteger

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var eventPattern = regexp.MustCompile(`^(sys\.)?[a-z][a-z0-9_]{0,63}$`)
var errInvalidRecord = errors.New("invalid internal event record")

type queueMessage struct {
	Version int              `json:"version"`
	Record  ingestion.Record `json:"record"`
}

// EncodeRecord validates the private, versioned queue message before enqueueing.
func EncodeRecord(r ingestion.Record) ([]byte, error) {
	normalized, e := normalizeRecord(r)
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(queueMessage{Version: 1, Record: normalized})
	if e != nil || len(b) > MaxRecordBytes {
		return nil, errInvalidRecord
	}
	return b, nil
}
func DecodeRecord(raw []byte) (ingestion.Record, error) {
	if len(raw) > MaxRecordBytes {
		return ingestion.Record{}, errInvalidRecord
	}
	if _, e := contract.Decode(raw); e != nil {
		return ingestion.Record{}, errInvalidRecord
	}
	var message queueMessage
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if e := d.Decode(&message); e != nil || message.Version != 1 {
		return ingestion.Record{}, errInvalidRecord
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return ingestion.Record{}, errInvalidRecord
	}
	return normalizeRecord(message.Record)
}
func normalizeRecord(r ingestion.Record) (ingestion.Record, error) {
	if !ingestion.ValidSourceKind(r.SourceKind) || !uuidPattern.MatchString(r.ProjectID) || !uuidPattern.MatchString(r.SourceID) || !uuidPattern.MatchString(r.EventID) || !eventPattern.MatchString(r.EventKey) || !env.Valid(r.Environment) || r.SchemaVersion < 1 || r.SchemaVersion > maxInteger || r.ReleaseRevision < 1 || r.ReleaseRevision > maxInteger {
		return r, errInvalidRecord
	}
	if !validTime(r.OccurredAt) || !validTime(r.ReceivedAt) || r.ReceivedAt.Before(time.Unix(0, 0)) || utf8.RuneCountInString(r.UID) > 128 || (r.UID == "" && r.AnonymousID == "") || (r.AnonymousID != "" && !uuidPattern.MatchString(r.AnonymousID)) || r.SDKName == "" || utf8.RuneCountInString(r.SDKName) > 128 || r.SDKVersion == "" || utf8.RuneCountInString(r.SDKVersion) > 128 {
		return r, errInvalidRecord
	}
	if !utf8.ValidString(r.UID) || !utf8.ValidString(r.SDKName) || !utf8.ValidString(r.SDKVersion) || strings.ContainsRune(r.UID, 0) || strings.ContainsRune(r.SDKName, 0) || strings.ContainsRune(r.SDKVersion, 0) {
		return r, errInvalidRecord
	}
	for _, id := range []string{r.Context.SessionID, r.Context.TabID, r.Context.PageViewID, r.Context.SourcePageViewID, r.Context.NavigationID, r.Context.PreviousPageViewID} {
		if id != "" && !uuidPattern.MatchString(id) {
			return r, errInvalidRecord
		}
	}
	if r.Context.Sequence != nil && (*r.Context.Sequence < 1 || *r.Context.Sequence > maxInteger || r.Context.TabID == "") {
		return r, errInvalidRecord
	}
	for _, field := range []struct {
		value string
		max   int
	}{{r.Context.OperationID, 128}, {r.Context.RouteName, 128}, {r.Context.RouteTemplate, 256}, {r.Context.SourceModule, 64}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max {
			return r, errInvalidRecord
		}
	}
	switch r.Context.NavigationType {
	case "", NavigationInitial, NavigationPush, NavigationReplace, NavigationPop, NavigationRestore:
	default:
		return r, errInvalidRecord
	}
	properties, e := contract.Decode(r.PropertiesJSON)
	if e != nil {
		return r, errInvalidRecord
	}
	object, ok := properties.(map[string]any)
	if !ok || len(object) > 50 {
		return r, errInvalidRecord
	}
	for key, value := range object {
		if !propertyKeyPattern.MatchString(key) || !validProperty(value) {
			return r, errInvalidRecord
		}
	}
	// Canonicalization preserves JSON numeric text without floating-point conversion.
	r.PropertiesJSON, e = json.Marshal(object)
	if e != nil || len(r.PropertiesJSON) > 16*1024 {
		return r, errInvalidRecord
	}
	contextValue, e := contract.Decode(r.ContextJSON)
	if e != nil {
		return r, errInvalidRecord
	}
	contextObject, ok := contextValue.(map[string]any)
	if !ok {
		return r, errInvalidRecord
	}
	for key, value := range contextObject {
		if key == "sequence" {
			if _, ok := value.(json.Number); !ok {
				return r, errInvalidRecord
			}
			continue
		}
		text, ok := value.(string)
		if !ok {
			return r, errInvalidRecord
		}
		switch key {
		case "session_id", "tab_id", "page_view_id", "source_page_view_id", "navigation_id", "previous_page_view_id":
			if !uuidPattern.MatchString(text) {
				return r, errInvalidRecord
			}
			contextObject[key] = strings.ToLower(text)
		case "operation_id":
			if text == "" {
				return r, errInvalidRecord
			}
		case "navigation_type":
			if text == "" {
				return r, errInvalidRecord
			}
		}
	}
	r.ProjectID = strings.ToLower(r.ProjectID)
	r.SourceID = strings.ToLower(r.SourceID)
	r.EventID = strings.ToLower(r.EventID)
	r.AnonymousID = strings.ToLower(r.AnonymousID)
	r.Context.SessionID = strings.ToLower(r.Context.SessionID)
	r.Context.TabID = strings.ToLower(r.Context.TabID)
	r.Context.PageViewID = strings.ToLower(r.Context.PageViewID)
	r.Context.SourcePageViewID = strings.ToLower(r.Context.SourcePageViewID)
	r.Context.NavigationID = strings.ToLower(r.Context.NavigationID)
	r.Context.PreviousPageViewID = strings.ToLower(r.Context.PreviousPageViewID)
	r.ContextJSON, e = json.Marshal(contextObject)
	if e != nil || len(r.ContextJSON) > 4096 {
		return r, errInvalidRecord
	}
	// The exact context is authoritative for presence; reject mismatched denormalized
	// fields rather than persisting two conflicting descriptions of one event.
	var c ingestion.EventContext
	d := json.NewDecoder(bytes.NewReader(r.ContextJSON))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return r, errInvalidRecord
	}
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(r.Context)
	if !bytes.Equal(a, b) {
		return r, errInvalidRecord
	}
	r.OccurredAt = r.OccurredAt.UTC()
	r.ReceivedAt = r.ReceivedAt.UTC()
	return r, nil
}

// DateTime64(9) stores signed nanoseconds and ClickHouse supports dates from
// 1900-01-01 through 2262-04-11T23:47:16.854775807Z. Compare UTC instants rather
// than local calendar years, and reject invalid queue records before sink retry.
func validTime(t time.Time) bool {
	return !t.IsZero() && !t.Before(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)) && !t.After(time.Unix(0, 1<<63-1))
}
func canonicalHash(r ingestion.Record) (string, error) {
	// Receipt time changes on retries; immutable event content does not.
	r.ReceivedAt = time.Unix(0, 0).UTC()
	b, e := json.Marshal(r)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

var propertyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validProperty(value any) bool {
	if values, ok := value.([]any); ok {
		if len(values) > 20 {
			return false
		}
		for _, item := range values {
			if !validScalar(item) {
				return false
			}
		}
		return true
	}
	return validScalar(value)
}
func validScalar(value any) bool {
	switch v := value.(type) {
	case string:
		return utf8.ValidString(v) && utf8.RuneCountInString(v) <= 1024
	case bool:
		return true
	case json.Number:
		n, _, e := big.ParseFloat(v.String(), 10, 256, big.ToNearestEven)
		if e != nil {
			return false
		}
		n.Abs(n)
		return n.Cmp(new(big.Float).SetInt64(maxInteger)) <= 0
	default:
		return false
	}
}
