// Package ingestion owns source credentials and durable collection admission.
package ingestion

import (
	"context"
	"encoding/json"
	"time"
)

type Source struct {
	ID               string   `json:"id"`
	ProjectID        string   `json:"project_id"`
	Name             string   `json:"name"`
	Environment      string   `json:"environment"`
	Kind             string   `json:"kind"`
	WriteKey         string   `json:"write_key"`
	Status           string   `json:"status"`
	Revision         int64    `json:"revision"`
	ApplicationState string   `json:"application_state"`
	AllowedOrigins   []string `json:"allowed_origins"`
}
type SourceInput struct {
	Name, Environment, Kind string
	AllowedOrigins          []string
}
type SourceUpdate struct {
	Status         *string
	AllowedOrigins *[]string
}
type Credential struct {
	ID, SourceID, WriteKey, SecretDigest string
	SecretCiphertext                     []byte
	ExpiresAt                            *time.Time
}
type Issued struct {
	Source     Source
	SecretOnce string
}

// RejectionSink consumes source-attributed rejection counters. Implementations
// must be fail-open: diagnostic accounting can never change an admission
// decision. A nil sink disables rejection accounting.
type RejectionSink interface {
	RecordRejections(ctx context.Context, projectID, environment string, counts map[string]int)
}

type SourceStore interface {
	CreateSource(context.Context, string, string, Source, Credential) (Source, error)
	ListSources(context.Context, string, string, string, string, int) ([]Source, error)
	GetSource(context.Context, string, string, string) (Source, error)
	// Mutation preflights use their own operation authority; read is not implied.
	GetSourceForUpdate(context.Context, string, string, string) (Source, error)
	GetSourceForRotation(context.Context, string, string, string) (Source, error)
	UpdateSource(context.Context, string, string, string, int64, SourceUpdate) (Source, error)
	RotateSource(context.Context, string, string, string, string, int64, Credential) (Source, Credential, error)
}

// Context preserves the caller's explicit ordering and journey identifiers;
// admission does not invent links or infer identity from website credentials.
type EventContext struct {
	SessionID          string `json:"session_id,omitempty"`
	TabID              string `json:"tab_id,omitempty"`
	Sequence           *int64 `json:"sequence,omitempty"`
	PageViewID         string `json:"page_view_id,omitempty"`
	SourcePageViewID   string `json:"source_page_view_id,omitempty"`
	NavigationID       string `json:"navigation_id,omitempty"`
	OperationID        string `json:"operation_id,omitempty"`
	RouteName          string `json:"route_name,omitempty"`
	RouteTemplate      string `json:"route_template,omitempty"`
	SourceModule       string `json:"source_module,omitempty"`
	NavigationType     string `json:"navigation_type,omitempty"`
	PreviousPageViewID string `json:"previous_page_view_id,omitempty"`
}

// Record is the credential-free, project-scoped durable pipeline envelope.
type Record struct {
	ProjectID       string          `json:"project_id"`
	SourceID        string          `json:"source_id"`
	SourceKind      string          `json:"source_kind"`
	Environment     string          `json:"environment"`
	ReceivedAt      time.Time       `json:"received_at"`
	EventID         string          `json:"event_id"`
	EventKey        string          `json:"event_key"`
	SchemaVersion   int64           `json:"schema_version"`
	ReleaseRevision int64           `json:"release_revision"`
	OccurredAt      time.Time       `json:"occurred_at"`
	UID             string          `json:"uid,omitempty"`
	AnonymousID     string          `json:"anonymous_id,omitempty"`
	Context         EventContext    `json:"context"`
	ContextJSON     json.RawMessage `json:"context_json"`
	PropertiesJSON  json.RawMessage `json:"properties"`
	SDKName         string          `json:"sdk_name"`
	SDKVersion      string          `json:"sdk_version"`
}

// Publish returns nil only after every record has a durable broker acknowledgement.
// An ambiguous acknowledgement must return an error; retries retain event IDs.
type Queue interface {
	Publish(context.Context, []Record) error
}

type AdmissionSource struct {
	Source     Source
	Credential Credential
}
type SourceResolver interface {
	ResolveSource(context.Context, string) (AdmissionSource, error)
}
type EventRule struct {
	Key                string
	Version            int64
	AllowedSourceKinds []string
	Schema             map[string]any
}
type Plan struct {
	ProjectID, Environment string
	Revision               int64
	Ready, Active          bool
	// ValidFor is remaining eligibility measured with the database clock.
	// Ready and Active require a positive value; zero never implies a default.
	ValidFor time.Duration
	Events   map[string]EventRule
}
type PlanResolver interface {
	ResolvePlan(context.Context, string, string, int64) (Plan, error)
}
type Budget interface {
	ReserveCollection(context.Context, string, string, int) error
}
type SchemaValidator interface{ Validate(string, any) error }
type Batch struct {
	ID, WriteKey string
	SentAt       time.Time
	Events       []json.RawMessage
}
type Authentication struct{ Bearer, Origin string }
type ItemError struct {
	Code       string   `json:"code"`
	Retryable  bool     `json:"retryable"`
	FieldPaths []string `json:"field_paths,omitempty"`
}
type ItemResult struct {
	Index   int        `json:"index"`
	EventID string     `json:"event_id,omitempty"`
	Status  string     `json:"status"`
	Error   *ItemError `json:"error,omitempty"`
}
type Receipt struct {
	BatchID   string       `json:"batch_id"`
	ReceiptID string       `json:"receipt_id"`
	Results   []ItemResult `json:"results"`
}
