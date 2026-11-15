// Package delivery owns verified release activation and source-scoped public configuration.
package delivery

import (
	"context"
	"encoding/json"
	"time"
)

const Capability = "raw-facts-v1"
const MaxRevision int64 = 9007199254740991

// ProjectReleaseEventLimit bounds the events inside one project release
// manifest; reserved sys.* events never enter project manifests.
const ProjectReleaseEventLimit = 200

// ReservedEventLimit is the fixed size of the embedded platform sys.* catalog
// pinned by the system event fixture invariants.
const ReservedEventLimit = 5

// PublicConfigEventLimit bounds total events served in one public config:
// 200 project release events plus the five platform-reserved sys.* events.
const PublicConfigEventLimit = ProjectReleaseEventLimit + ReservedEventLimit

type ActivationInput struct {
	Environment            string `json:"environment"`
	ReleaseID              string `json:"release_id"`
	ExpectedActiveRevision int64  `json:"expected_active_revision"`
}
type Activation struct {
	Environment           string `json:"environment"`
	ActiveReleaseRevision int64  `json:"active_release_revision"`
}
type Candidate struct {
	ReleaseID, ProjectID, Environment, ManifestHash string
	Revision                                        int64
	Manifest                                        json.RawMessage
	Ready                                           bool
	ReadyUntil                                      time.Time
}
type ProbeRequest struct {
	ProjectID    string    `json:"project_id"`
	Environment  string    `json:"environment"`
	ReleaseID    string    `json:"release_id"`
	Revision     int64     `json:"revision"`
	ManifestHash string    `json:"manifest_hash"`
	Nonce        string    `json:"nonce"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type Proof struct {
	ProbeRequest
	Capability string    `json:"capability"`
	ObservedAt time.Time `json:"observed_at"`
}
type Probe interface {
	Verify(context.Context, ProbeRequest) (Proof, error)
}
type Validator interface{ Validate(string, any) error }
type Source struct {
	ID, ProjectID, Environment, Kind, Status, SecretDigest string
	AllowedOrigins                                         []string
	ExpiresAt                                              *time.Time
}
type Authentication struct{ Origin, Bearer string }
type ConfigEvent struct {
	Key     string         `json:"event_key"`
	Version int64          `json:"schema_version"`
	Schema  map[string]any `json:"schema"`
}

// ReservedEvent is a platform-built sys.* event that exists outside every
// project release. Kinds keeps the same source-kind allowlist semantics as
// release manifest events, so public config can apply the identical filter.
type ReservedEvent struct {
	Key     string
	Version int64
	Kinds   []string
	Schema  map[string]any
}

// ReservedEventProvider supplies the embedded system catalog to public config.
// Implementations must return a stable, deterministic ordering because config
// contents feed the response ETag.
type ReservedEventProvider interface {
	ReservedEvents() []ReservedEvent
}
type Config struct {
	Format      string        `json:"format"`
	ProjectID   string        `json:"project_id"`
	Environment string        `json:"environment"`
	Revision    int64         `json:"release_revision"`
	Events      []ConfigEvent `json:"events"`
}
type ConfigResult struct {
	Config              Config
	ETag, AllowedOrigin string
}
type ConfigSnapshot struct {
	Source          Source
	Active          Candidate
	Current         *Candidate
	CurrentAccepted bool
}
type Options struct{ CompatibilityWindow, LeaseDuration, ProbeTimeout time.Duration }

// Store rechecks authorization and scope within every activation transaction.
// ReadConfig and ConfigSource always read live credential/revocation state.
type Store interface {
	GetActivation(context.Context, string, string, string) (Activation, error)
	PrepareActivation(context.Context, string, string, string, string, ActivationInput) (Candidate, *Activation, error)
	CommitActivation(context.Context, string, string, string, string, ActivationInput, Candidate, Proof, time.Duration, time.Duration) (Activation, error)
	ReadConfig(context.Context, string, int64) (ConfigSnapshot, error)
	ConfigSource(context.Context, string) (Source, error)
	ListReadiness(context.Context, int) ([]Candidate, error)
	RenewReadiness(context.Context, Candidate, Proof, time.Duration) error
}
