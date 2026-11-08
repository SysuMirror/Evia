package main

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

type itemError struct {
	Code       string   `json:"code"`
	Retryable  *bool    `json:"retryable"`
	FieldPaths []string `json:"field_paths,omitempty"`
}
type receiptItem struct {
	Index   *int       `json:"index"`
	EventID string     `json:"event_id,omitempty"`
	Status  string     `json:"status"`
	Error   *itemError `json:"error,omitempty"`
}

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,100}$`)

func decodeStrict(raw []byte, dst any) bool {
	if _, err := contract.Decode(raw); err != nil {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst) == nil && d.Decode(new(any)) == io.EOF
}

// A 202 alone is not admission evidence. Every index and event ID must match.
func parseReceipt(raw []byte, b batch) ([]receiptItem, bool) {
	var r wire.CollectResponse
	if !decodeStrict(raw, &r) || r.BatchID != b.request.BatchID || !uuidPattern.MatchString(r.ReceiptID) || len(r.Results) != b.count {
		return nil, false
	}
	items := make([]receiptItem, b.count)
	seen := make([]bool, b.count)
	for _, rawItem := range r.Results {
		var item receiptItem
		if !decodeStrict(rawItem, &item) || item.Index == nil || *item.Index < 0 || *item.Index >= b.count || seen[*item.Index] {
			return nil, false
		}
		i := *item.Index
		seen[i] = true
		if item.EventID != "" && item.EventID != b.request.Events[i].EventID {
			return nil, false
		}
		switch item.Status {
		case "accepted":
			if item.EventID == "" || item.Error != nil {
				return nil, false
			}
		case "rejected":
			if item.Error == nil || item.Error.Retryable == nil || !codePattern.MatchString(item.Error.Code) || len(item.Error.FieldPaths) > 20 {
				return nil, false
			}
			for _, path := range item.Error.FieldPaths {
				if len(path) > 256 {
					return nil, false
				}
			}
		default:
			return nil, false
		}
		items[i] = item
	}
	return items, true
}
