// Package delivery owns verified release activation and source-scoped public configuration.
package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
)

const Capability = "raw-facts-v1"
const MaxRevision int64 = canonical.MaxInteger

// ProjectReleaseEventLimit bounds the events inside one project release
// manifest; reserved sys.* events never enter project manifests.
const ProjectReleaseEventLimit = 200

// ReservedEventLimit is the fixed size of the embedded platform sys.* catalog
// pinned by the system event fixture invariants.
const ReservedEventLimit = 7

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

type ConfigSigner interface {
	SignDigest(string) (keyID, signature string, err error)
}
type ContentEncryption struct {
	KeyID     string         `json:"key_id"`
	Algorithm string         `json:"alg"`
	PublicJWK map[string]any `json:"public_jwk"`
}

type ConfigSignature struct {
	KeyID        string `json:"key_id"`
	Algorithm    string `json:"alg"`
	DigestSHA256 string `json:"digest_sha256"`
	Signature    string `json:"signature"`
}

type ConfigExperimentVariant struct {
	Key       string `json:"key"`
	WeightBps int    `json:"weight_bps"`
}

type ConfigExperiment struct {
	Key              string                    `json:"experiment_key"`
	Version          int64                     `json:"version"`
	Salt             string                    `json:"salt"`
	ControlVariant   string                    `json:"control_variant"`
	ExposureEventKey string                    `json:"exposure_event_key"`
	Variants         []ConfigExperimentVariant `json:"variants"`
	Overrides        map[string]string         `json:"overrides,omitempty"`
}

// ExperimentProvider supplies running experiments for deterministic local
// bucketing in public config. Returned slices must be deterministically ordered
// because their JSON feeds ETag and Ed25519 config_signature.
type ExperimentProvider interface {
	RunningExperiments(ctx context.Context, projectID, environment string) ([]ConfigExperiment, error)
}

type Config struct {
	Format             string             `json:"format"`
	ProjectID          string             `json:"project_id"`
	Environment        string             `json:"environment"`
	Revision           int64              `json:"release_revision"`
	Events             []ConfigEvent      `json:"events"`
	Experiments        []ConfigExperiment `json:"experiments,omitempty"`
	ContentEncryption  *ContentEncryption `json:"content_encryption,omitempty"`
	TelemetryToken     string             `json:"telemetry_token,omitempty"`
	TelemetryExpiresAt *time.Time         `json:"telemetry_expires_at,omitempty"`
	ConfigSignature    *ConfigSignature   `json:"config_signature,omitempty"`
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
