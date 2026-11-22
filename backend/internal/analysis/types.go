// Package analysis owns bounded typed queries and durable execution policy.
package analysis

import (
	"context"
	"encoding/json"
	"time"
)

type TimeRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}
type Field struct {
	Kind          string `json:"kind"`
	Key           string `json:"key"`
	EventKey      string `json:"event_key,omitempty"`
	SchemaVersion int64  `json:"schema_version,omitempty"`
	ResolvedType  string `json:"resolved_type,omitempty"`
}
type Filter struct {
	Op    string    `json:"op"`
	Args  []*Filter `json:"args,omitempty"`
	Field *Field    `json:"field,omitempty"`
	Value any       `json:"value,omitempty"`
}
type Metric struct {
	Op       string `json:"op"`
	Field    *Field `json:"field,omitempty"`
	Accuracy string `json:"accuracy,omitempty"`
}
type IdentityFilter struct {
	UID         *string `json:"uid,omitempty"`
	AnonymousID *string `json:"anonymous_id,omitempty"`
	// ResolvedPersonID pins one resolved person for a resolved_person subject
	// query; the plan expands it into a bounded member predicate.
	ResolvedPersonID *string `json:"resolved_person_id,omitempty"`
}
type Step struct {
	EventKey      string  `json:"event_key"`
	SchemaVersion int64   `json:"schema_version"`
	Filter        *Filter `json:"filter,omitempty"`
}
type EntityField struct {
	StepIndex int   `json:"step_index"`
	Field     Field `json:"field"`
}
type Funnel struct {
	Steps         []Step        `json:"steps"`
	WindowSeconds int           `json:"window_seconds"`
	Mode          string        `json:"mode"`
	SameEntity    []EntityField `json:"same_entity,omitempty"`
}

// Retention defines a calendar-cohort retention matrix. Subjects enter a
// cohort in the period of their first start event; return events are counted
// per offset period relative to that cohort. Granularity is a timezone-aligned
// calendar day or Monday-starting week.
type Retention struct {
	StartEvent  Step   `json:"start_event"`
	ReturnEvent Step   `json:"return_event"`
	Granularity string `json:"granularity"`
	Periods     int    `json:"periods"`
}

// Segment partitions subjects into mutually exclusive groups. Each subject is
// labeled once, using the field value on its earliest matching segment event,
// so a subject can never be counted in two groups. Missing or blank values
// fall into an explicit missing group.
type Segment struct {
	Event Step  `json:"event"`
	Field Field `json:"field"`
}

// Path aggregates the ordered event sequences subjects actually traverse.
// StartEvent optionally pins where each subject's path begins; when absent the
// subject's earliest in-window event is the entry. Depth is the fixed number
// of events that make up one path (2..8).
type Path struct {
	StartEvent      *Step `json:"start_event,omitempty"`
	Depth           int   `json:"depth"`
	CollapseRepeats bool  `json:"collapse_repeats,omitempty"`
}

// Attribution credits converting subjects to the last matching touchpoint
// before their earliest conversion inside a bounded window. Model is an
// explicit closed set; v1 supports last-touch only, so later models can be
// versioned without changing published results.
type Attribution struct {
	Conversion           Step   `json:"conversion"`
	Touchpoints          []Step `json:"touchpoints"`
	WindowSeconds        int64  `json:"window_seconds"`
	Model                string `json:"model"`
	DecayHalfLifeSeconds int64  `json:"decay_half_life_seconds,omitempty"`
}

// Heatmap selects one exact route and one exact browser viewport. The event,
// schema version and 20x10 coordinate grid are fixed by the v1 engine.
type Heatmap struct {
	RouteName      *string `json:"route_name,omitempty"`
	RouteTemplate  *string `json:"route_template,omitempty"`
	ViewportWidth  int     `json:"viewport_width"`
	ViewportHeight int     `json:"viewport_height"`
}

// EventVersion selects facts from one immutable event schema version.
type EventVersion struct {
	EventKey      string `json:"event_key"`
	SchemaVersion int64  `json:"schema_version"`
}
type Request struct {
	Kind             string          `json:"kind"`
	Environment      string          `json:"environment"`
	TimeRange        TimeRange       `json:"time_range"`
	Timezone         string          `json:"timezone"`
	EventKeys        []string        `json:"event_keys,omitempty"`
	EventVersions    []EventVersion  `json:"event_versions,omitempty"`
	Filter           *Filter         `json:"filter,omitempty"`
	Subject          string          `json:"subject,omitempty"`
	IdentityRevision *int64          `json:"identity_revision,omitempty"`
	IdentityFilter   *IdentityFilter `json:"identity_filter,omitempty"`
	GroupBy          []Field         `json:"group_by,omitempty"`
	Metrics          []Metric        `json:"metrics,omitempty"`
	Interval         string          `json:"interval,omitempty"`
	Funnel           *Funnel         `json:"funnel,omitempty"`
	Retention        *Retention      `json:"retention,omitempty"`
	Segment          *Segment        `json:"segment,omitempty"`
	Path             *Path           `json:"path,omitempty"`
	Attribution      *Attribution    `json:"attribution,omitempty"`
	Heatmap          *Heatmap        `json:"heatmap,omitempty"`
	Limit            int             `json:"limit,omitempty"`
	Cursor           string          `json:"cursor,omitempty"`
	// Formula is an internal-only execution marker. It is absent from the
	// public query contract, whose strict decoder rejects unknown fields, so a
	// client can never set it; only the formula metric enqueue path writes it
	// into a durable job, and the composite engine routes on it.
	Formula *FormulaJobRef `json:"formula_ref,omitempty"`
	// IdentityExpansion is an internal-only execution attachment. It is
	// absent from the public query contract (strict decoding rejects unknown
	// fields), and only the enqueue transaction materializes it from the
	// versioned identity graph: the pinned revision plus the bounded member
	// mapping the planner needs. Workers and the result metadata consume it
	// without further graph reads.
	IdentityExpansion *IdentityExpansion `json:"identity_expansion,omitempty"`
}

// IdentityExpansion is the plan-time materialization of one identity graph
// revision for a resolved_person query. Mapping covers every person of the
// pinned revision (or just the pinned person), bounded by the expansion
// budgets enforced at enqueue.
type IdentityExpansion struct {
	Revision int64 `json:"revision"`
	// PersonID is set when the query pins exactly one resolved person.
	PersonID string `json:"person_id,omitempty"`
	// Mapping lists person -> raw member sets for subject-column mapping.
	Mapping []PersonMapping `json:"mapping,omitempty"`
	// PinnedMembers are the raw member sets of the pinned person.
	PinnedUIDs         []string `json:"pinned_uids,omitempty"`
	PinnedAnonymousIDs []string `json:"pinned_anonymous_ids,omitempty"`
}

// PersonMapping is one resolved person's member sets inside an expansion.
type PersonMapping struct {
	PersonID     string   `json:"person_id"`
	UIDs         []string `json:"uids"`
	AnonymousIDs []string `json:"anonymous_ids"`
}

// FormulaJobRef identifies the immutable composite metric version a durable job
// must evaluate. The query worker resolves its atomic leaves at execution time.
type FormulaJobRef struct {
	MetricID string `json:"metric_id"`
	Version  int64  `json:"version"`
}
type Scope struct{ ProjectID, Environment string }
type JobError struct {
	Code       string   `json:"code"`
	Retryable  bool     `json:"retryable"`
	FieldPaths []string `json:"field_paths,omitempty"`
}
type Job struct {
	ID          string     `json:"query_id"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	PollAfterMS *int       `json:"poll_after_ms,omitempty"`
	ResultURL   string     `json:"result_url,omitempty"`
	Error       *JobError  `json:"error,omitempty"`
}
type Column struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
}
type Coverage struct {
	TimeRange TimeRange `json:"time_range"`
	State     string    `json:"state"`
	KnownGaps []string  `json:"known_gaps"`
}
type Metadata struct {
	DataWatermark    *time.Time `json:"data_watermark"`
	IdentityRevision *int64     `json:"identity_revision,omitempty"`
	Accuracy         string     `json:"accuracy"`
	SampleRate       float64    `json:"sample_rate"`
	Coverage         Coverage   `json:"coverage"`
	CacheHit         bool       `json:"cache_hit"`
	GeneratedAt      time.Time  `json:"generated_at"`
}
type Result struct {
	QueryID    string           `json:"query_id"`
	Columns    []Column         `json:"columns"`
	Rows       []map[string]any `json:"rows"`
	NextCursor *string          `json:"next_cursor"`
	Metadata   Metadata         `json:"metadata"`
}
type Claim struct {
	JobID, ActorID, ProjectID, Worker string
	CredentialID                      string
	RequiresRaw                       bool
	Generation                        int64
	Request                           Request
}
type Outcome struct {
	Result *Result
	Error  *JobError
}
type Validator interface{ Validate(string, any) error }
type Store interface {
	EnqueueQuery(context.Context, string, string, Request) (Job, error)
	GetQuery(context.Context, string, string, string) (Job, error)
	CancelQuery(context.Context, string, string, string) (Job, error)
	QueryResult(context.Context, string, string, string) (Result, error)
}
type JobStore interface {
	ClaimQuery(context.Context, string, time.Duration) (*Claim, error)
	QueryLeaseValid(context.Context, Claim) (bool, error)
	CompleteQuery(context.Context, Claim, Outcome) error
}
type Engine interface {
	Execute(context.Context, Scope, Request) (Result, error)
}
type QueryService interface {
	Create(context.Context, string, string, json.RawMessage) (Job, error)
	Get(context.Context, string, string, string) (Job, error)
	Cancel(context.Context, string, string, string) (Job, error)
	Result(context.Context, string, string, string) (Result, error)
}
