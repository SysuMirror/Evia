// Package delivery owns verified release activation and source-scoped public configuration.
package delivery

import (
	"context"
	"encoding/json"
	"time"
)

const Capability = "raw-facts-v1"
const MaxRevision int64 = 9007199254740991

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
