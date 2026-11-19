package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const testProject = "11111111-1111-4111-8111-111111111111"
const testRelease = "22222222-2222-4222-8222-222222222222"

type memoryStore struct {
	Store
	candidate               Candidate
	snapshot                ConfigSnapshot
	replay                  *Activation
	prepareErr, errorCommit error
	commits, renews         int
}

func (s *memoryStore) PrepareActivation(context.Context, string, string, string, string, ActivationInput) (Candidate, *Activation, error) {
	return s.candidate, s.replay, s.prepareErr
}
func (s *memoryStore) CommitActivation(_ context.Context, _, _, _, _ string, _ ActivationInput, c Candidate, p Proof, _, _ time.Duration) (Activation, error) {
	s.commits++
	return Activation{Environment: c.Environment, ActiveReleaseRevision: c.Revision}, s.errorCommit
}
func (s *memoryStore) ReadConfig(context.Context, string, int64) (ConfigSnapshot, error) {
	return s.snapshot, nil
}
func (s *memoryStore) ConfigSource(context.Context, string) (Source, error) {
	return s.snapshot.Source, nil
}
func (s *memoryStore) RenewReadiness(context.Context, Candidate, Proof, time.Duration) error {
	s.renews++
	return nil
}

type probeFunc func(context.Context, ProbeRequest) (Proof, error)

func (p probeFunc) Verify(ctx context.Context, r ProbeRequest) (Proof, error) { return p(ctx, r) }
func successfulProbe(_ context.Context, r ProbeRequest) (Proof, error) {
	return Proof{ProbeRequest: r, Capability: Capability, ObservedAt: time.Now().UTC()}, nil
}
func candidate(t *testing.T, revision int64, kind string, schema map[string]any) Candidate {
	t.Helper()
	value := map[string]any{"format": "activity.event-plan.v1", "status": "published", "project_key": testProject, "environment": "development", "release_revision": revision, "source": map[string]any{"private": "hidden"}, "description": "private release description", "events": []any{map[string]any{"event_key": "opened", "display_name": "Private label", "schema_version": revision, "allowed_source_kinds": []string{kind}, "fact_kind": "user_intent", "trigger": "private trigger", "schema": schema, "field_metadata": map[string]any{"private": "hidden"}, "example_properties": map[string]any{"count": 0}}}}
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	hash, e := canonical.Hash(value)
	if e != nil {
		t.Fatal(e)
	}
	return Candidate{ReleaseID: testRelease, ProjectID: testProject, Environment: "development", Revision: revision, ManifestHash: hash, Manifest: raw, Ready: true, ReadyUntil: time.Now().Add(time.Minute)}
}
func schema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "description": "private object description", "properties": map[string]any{"count": map[string]any{"type": "integer", "description": "private field description", "default": 1}, "description": map[string]any{"type": "string", "title": "private label"}}}
}
func newTestService(t *testing.T, store *memoryStore, probe Probe) *Service {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(store, probe, v, Options{})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func code(t *testing.T, e error, want string) {
	t.Helper()
	if f := fault.Public(e); f.Code != want {
		t.Fatalf("want %s got %v", want, e)
	}
}
func TestActivationRequiresCompiledHashAndBoundFreshProof(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*memoryStore)
		probe probeFunc
		want  string
	}{
		{"success", nil, successfulProbe, ""},
		{"hash mismatch", func(s *memoryStore) { s.candidate.ManifestHash = strings.Repeat("a", 64) }, successfulProbe, "RELEASE_MANIFEST_HASH_MISMATCH"},
		{"unavailable", nil, func(context.Context, ProbeRequest) (Proof, error) {
			return Proof{}, errors.New("private broker diagnostic")
		}, "PIPELINE_NOT_READY"},
		{"wrong nonce", nil, func(ctx context.Context, r ProbeRequest) (Proof, error) {
			p, _ := successfulProbe(ctx, r)
			p.Nonce = ids.New()
			return p, nil
		}, "PIPELINE_PROOF_INVALID"},
		{"wrong scope", nil, func(ctx context.Context, r ProbeRequest) (Proof, error) {
			p, _ := successfulProbe(ctx, r)
			p.ProjectID = testRelease
			return p, nil
		}, "PIPELINE_PROOF_INVALID"},
		{"stale proof", nil, func(ctx context.Context, r ProbeRequest) (Proof, error) {
			p, _ := successfulProbe(ctx, r)
			p.ObservedAt = time.Now().Add(-time.Hour)
			return p, nil
		}, "PIPELINE_PROOF_INVALID"},
		{"wrong capability", nil, func(ctx context.Context, r ProbeRequest) (Proof, error) {
			p, _ := successfulProbe(ctx, r)
			p.Capability = "push-only"
			return p, nil
		}, "PIPELINE_PROOF_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{candidate: candidate(t, 1, ingestion.SourceKindBrowser, schema())}
			if tc.edit != nil {
				tc.edit(store)
			}
			s := newTestService(t, store, tc.probe)
			out, e := s.Activate(context.Background(), testProject, testProject, "activation-1", ActivationInput{Environment: "development", ReleaseID: testRelease})
			if tc.want != "" {
				code(t, e, tc.want)
				if store.commits != 0 {
					t.Fatal("unverified activation committed")
				}
			} else if e != nil || out.ActiveReleaseRevision != 1 || store.commits != 1 {
				t.Fatal(out, e)
			}
		})
	}
}
func TestActivationReplayDoesNotRepeatPipelineSideEffects(t *testing.T) {
	store := &memoryStore{replay: &Activation{Environment: "development", ActiveReleaseRevision: 1}}
	s := newTestService(t, store, probeFunc(func(context.Context, ProbeRequest) (Proof, error) {
		t.Fatal("idempotent replay invoked probe")
		return Proof{}, nil
	}))
	out, e := s.Activate(context.Background(), testProject, testProject, "activation-1", ActivationInput{Environment: "development", ReleaseID: testRelease})
	if e != nil || out.ActiveReleaseRevision != 1 || store.commits != 0 {
		t.Fatal(out, e)
	}
}
func TestManifestRejectsRemoteSchemaBeforeCompilation(t *testing.T) {
	raw := schema()
	raw["$ref"] = "https://untrusted.invalid/schema"
	store := &memoryStore{candidate: candidate(t, 1, ingestion.SourceKindBrowser, raw)}
	s := newTestService(t, store, probeFunc(successfulProbe))
	_, e := s.Activate(context.Background(), testProject, testProject, "activation-1", ActivationInput{Environment: "development", ReleaseID: testRelease})
	code(t, e, "RELEASE_SCHEMA_INVALID")
	if store.commits != 0 {
		t.Fatal("remote schema became eligible")
	}
}
func TestPublicConfigAllowlistSourceFilterAndRevocation(t *testing.T) {
	store := &memoryStore{}
	store.snapshot = ConfigSnapshot{Source: Source{ID: testRelease, ProjectID: testProject, Environment: "development", Kind: ingestion.SourceKindBrowser, Status: ingestion.SourceStatusActive, AllowedOrigins: []string{"https://site.example"}}, Active: candidate(t, 1, ingestion.SourceKindBrowser, schema())}
	s := newTestService(t, store, probeFunc(successfulProbe))
	auth := Authentication{Origin: "https://site.example"}
	out, e := s.PublicConfig(context.Background(), "public-key", auth, 0)
	if e != nil || out.AllowedOrigin != auth.Origin || len(out.Config.Events) != 1 || len(out.ETag) != 66 {
		t.Fatal(out, e)
	}
	raw, e := json.Marshal(out.Config)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"private", "trigger", "field_metadata", "example_properties", "source\"", "default"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private annotation escaped", string(raw))
		}
	}
	if _, ok := out.Config.Events[0].Schema["properties"].(map[string]any)["description"]; !ok {
		t.Fatal("business property named description was removed")
	}
	store.snapshot.Source.Kind = ingestion.SourceKindServer
	store.snapshot.Source.SecretDigest = strings.Repeat("0", 64)
	_, e = s.PublicConfig(context.Background(), "public-key", Authentication{}, 0)
	code(t, e, "SOURCE_CREDENTIAL_INVALID")
	store.snapshot.Source.Kind = ingestion.SourceKindBrowser
	store.snapshot.Source.Status = ingestion.SourceStatusDisabled
	_, e = s.PublicConfig(context.Background(), "public-key", auth, 0)
	code(t, e, "SOURCE_DISABLED")
	store.snapshot.Source.Status = ingestion.SourceStatusActive
	store.snapshot.Active = candidate(t, 1, ingestion.SourceKindServer, schema())
	out, e = s.PublicConfig(context.Background(), "public-key", auth, 0)
	if e != nil || len(out.Config.Events) != 0 {
		t.Fatal("source kind filter cannot produce empty valid config", out, e)
	}
	expired := time.Now().Add(-time.Second)
	store.snapshot.Source.ExpiresAt = &expired
	_, e = s.PublicConfig(context.Background(), "public-key", auth, 0)
	code(t, e, "SOURCE_CREDENTIAL_INVALID")
}
func TestPublicConfigCompatibleUpgradeAndBoundedOldRevision(t *testing.T) {
	prior := candidate(t, 1, ingestion.SourceKindBrowser, schema())
	nextSchema := schema()
	nextSchema["properties"].(map[string]any)["extra"] = map[string]any{"type": "boolean"}
	store := &memoryStore{snapshot: ConfigSnapshot{Source: Source{ID: testRelease, ProjectID: testProject, Environment: "development", Kind: ingestion.SourceKindBrowser, Status: ingestion.SourceStatusActive, AllowedOrigins: []string{"https://site.example"}}, Active: candidate(t, 2, ingestion.SourceKindBrowser, nextSchema), Current: &prior, CurrentAccepted: true}}
	s := newTestService(t, store, probeFunc(successfulProbe))
	auth := Authentication{Origin: "https://site.example"}
	out, e := s.PublicConfig(context.Background(), "key", auth, 1)
	if e != nil || out.Config.Revision != 2 {
		t.Fatal("compatible release did not upgrade", out, e)
	}
	breaking := schema()
	breaking["properties"].(map[string]any)["count"] = map[string]any{"type": "string"}
	store.snapshot.Active = candidate(t, 3, ingestion.SourceKindBrowser, breaking)
	out, e = s.PublicConfig(context.Background(), "key", auth, 1)
	if e != nil || out.Config.Revision != 1 {
		t.Fatal("breaking release blindly upgraded old client", out, e)
	}
	store.snapshot.CurrentAccepted = false
	_, e = s.PublicConfig(context.Background(), "key", auth, 1)
	code(t, e, "CONFIG_COMPATIBILITY_EXPIRED")
	store.snapshot.Current = nil
	_, e = s.PublicConfig(context.Background(), "key", auth, 9)
	code(t, e, "CONFIG_REVISION_NOT_ACTIVATED")
	store.snapshot.Active.ReadyUntil = time.Now().Add(-time.Second)
	_, e = s.PublicConfig(context.Background(), "key", auth, 0)
	code(t, e, "RELEASE_NOT_READY")
}

func TestPublicConfigMetadataMeaningChangeKeepsPriorRevision(t *testing.T) {
	prior := manifest{Events: []manifestEvent{{Key: "opened", Kinds: []string{ingestion.SourceKindBrowser}, FactKind: catalog.FactKindUserIntent, Schema: schema(), FieldMetadata: map[string]any{"duration": map[string]any{"unit": "ms"}}}}}
	next := manifest{Events: []manifestEvent{{Key: "opened", Kinds: []string{ingestion.SourceKindBrowser}, FactKind: catalog.FactKindUserIntent, Schema: schema(), FieldMetadata: map[string]any{"duration": map[string]any{"unit": "s"}}}}}
	if canUpgrade(prior, next, ingestion.SourceKindBrowser) {
		t.Fatal("same JSON type silently changed measurement meaning")
	}
}

func TestPublicConfigServerBearerAndCrossOriginFailClosed(t *testing.T) {
	secret := strings.Repeat("s", 43)
	sum := sha256.Sum256([]byte(secret))
	store := &memoryStore{snapshot: ConfigSnapshot{Source: Source{ID: testRelease, ProjectID: testProject, Environment: "development", Kind: ingestion.SourceKindServer, Status: ingestion.SourceStatusActive, SecretDigest: hex.EncodeToString(sum[:])}, Active: candidate(t, 1, ingestion.SourceKindServer, schema())}}
	service := newTestService(t, store, probeFunc(successfulProbe))
	out, e := service.PublicConfig(context.Background(), "server-key", Authentication{Bearer: secret}, 0)
	if e != nil || len(out.Config.Events) != 1 || out.AllowedOrigin != "" {
		t.Fatal(out, e)
	}
	_, e = service.PublicConfig(context.Background(), "server-key", Authentication{Bearer: secret, Origin: "https://website.example"}, 0)
	code(t, e, "SOURCE_CREDENTIAL_INVALID")
	_, e = service.PublicConfig(context.Background(), "server-key", Authentication{Bearer: strings.Repeat("x", 43)}, 0)
	code(t, e, "SOURCE_CREDENTIAL_INVALID")
}

type blockingValidator struct {
	Validator
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (v *blockingValidator) Validate(name string, value any) error {
	v.calls.Add(1)
	v.once.Do(func() { close(v.entered) })
	<-v.release
	return v.Validator.Validate(name, value)
}
func TestManifestValidationSingleFlightBoundsConcurrentMisses(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(t, store, probeFunc(successfulProbe))
	v := &blockingValidator{Validator: service.validator, entered: make(chan struct{}), release: make(chan struct{})}
	service.validator = v
	c := candidate(t, 1, ingestion.SourceKindBrowser, schema())
	results := make(chan error, 16)
	for range 16 {
		go func() { _, e := service.checkManifest(context.Background(), c); results <- e }()
	}
	<-v.entered
	if v.calls.Load() != 1 {
		t.Fatal("same immutable manifest compiled concurrently", v.calls.Load())
	}
	close(v.release)
	for range 16 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	if v.calls.Load() != 1 {
		t.Fatal("duplicate validation work", v.calls.Load())
	}
}

func (s *memoryStore) GetActivation(_ context.Context, _, _, environment string) (Activation, error) {
	return Activation{Environment: environment, ActiveReleaseRevision: s.candidate.Revision}, s.prepareErr
}
func TestGetActivationValidatesScopeAndPreservesPersistenceErrors(t *testing.T) {
	store := &memoryStore{}
	s := newTestService(t, store, probeFunc(successfulProbe))
	out, e := s.GetActivation(context.Background(), "actor", testProject, "development")
	if e != nil || out.Environment != "development" || out.ActiveReleaseRevision != 0 {
		t.Fatal(out, e)
	}
	for _, scope := range [][2]string{{"invalid", "development"}, {testProject, ""}, {testProject, "other"}} {
		_, e = s.GetActivation(context.Background(), "actor", scope[0], scope[1])
		code(t, e, "INVALID_ACTIVATION")
	}
	store.prepareErr = fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "不可见")
	_, e = s.GetActivation(context.Background(), "actor", testProject, "production")
	code(t, e, "PROJECT_NOT_FOUND")
	store.prepareErr = errors.New("dependency failed")
	_, e = s.GetActivation(context.Background(), "actor", testProject, "production")
	if e != store.prepareErr {
		t.Fatal(e)
	}
}

type reservedProviderStub struct {
	events []ReservedEvent
}

func (p reservedProviderStub) ReservedEvents() []ReservedEvent { return p.events }

func reservedBrowserSnapshot(t *testing.T, kind string) ConfigSnapshot {
	t.Helper()
	return ConfigSnapshot{
		Source: Source{ID: testRelease, ProjectID: testProject, Environment: "development", Kind: kind, Status: ingestion.SourceStatusActive, AllowedOrigins: []string{"https://site.example"}},
		Active: candidate(t, 1, kind, schema()),
	}
}

func TestPublicConfigAppendsReservedEventsForBrowserSource(t *testing.T) {
	reserved := []ReservedEvent{
		{Key: "sys.page_view", Version: 1, Kinds: []string{ingestion.SourceKindBrowser}, Schema: map[string]any{"type": "object", "description": "private annotation", "additionalProperties": false, "properties": map[string]any{}}},
		{Key: "sys.session_end", Version: 1, Kinds: []string{ingestion.SourceKindBrowser}, Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"reason": map[string]any{"type": "string", "title": "private label"}}}},
	}
	store := &memoryStore{snapshot: reservedBrowserSnapshot(t, ingestion.SourceKindBrowser)}
	s := newTestService(t, store, probeFunc(successfulProbe)).WithReservedEvents(reservedProviderStub{events: reserved})
	auth := Authentication{Origin: "https://site.example"}

	out, e := s.PublicConfig(context.Background(), "public-key", auth, 0)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Config.Events) != 3 {
		t.Fatalf("want 1 release + 2 reserved events, got %d", len(out.Config.Events))
	}
	// Project release events keep precedence; reserved events follow in provider order.
	if out.Config.Events[0].Key != "opened" || out.Config.Events[1].Key != "sys.page_view" || out.Config.Events[2].Key != "sys.session_end" {
		t.Fatalf("event ordering changed: %+v", out.Config.Events)
	}
	for _, event := range out.Config.Events[1:] {
		if event.Version != 1 {
			t.Fatalf("reserved version lost: %+v", event)
		}
		raw, err := json.Marshal(event.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "description") || strings.Contains(string(raw), "title") {
			t.Fatalf("reserved schema kept private annotations: %s", raw)
		}
	}
	// The ETag covers reserved events: two identical calls produce identical ETags,
	// and a provider-less service produces a different one.
	again, e := s.PublicConfig(context.Background(), "public-key", auth, 0)
	if e != nil || again.ETag != out.ETag {
		t.Fatalf("reserved config not deterministic: %v %v", e, again.ETag)
	}
	plain := newTestService(t, store, probeFunc(successfulProbe))
	without, e := plain.PublicConfig(context.Background(), "public-key", auth, 0)
	if e != nil || without.ETag == out.ETag || len(without.Config.Events) != 1 {
		t.Fatalf("provider-less config must stay on the historical shape: %v %d", e, len(without.Config.Events))
	}
}

func TestPublicConfigReservedEventsRespectSourceKind(t *testing.T) {
	reserved := []ReservedEvent{
		{Key: "sys.page_view", Version: 1, Kinds: []string{ingestion.SourceKindBrowser}, Schema: map[string]any{"type": "object"}},
		{Key: "sys.server_only", Version: 2, Kinds: []string{ingestion.SourceKindServer}, Schema: map[string]any{"type": "object"}},
	}
	store := &memoryStore{snapshot: reservedBrowserSnapshot(t, ingestion.SourceKindBrowser)}
	s := newTestService(t, store, probeFunc(successfulProbe)).WithReservedEvents(reservedProviderStub{events: reserved})
	out, e := s.PublicConfig(context.Background(), "public-key", Authentication{Origin: "https://site.example"}, 0)
	if e != nil {
		t.Fatal(e)
	}
	var keys []string
	for _, event := range out.Config.Events {
		keys = append(keys, event.Key)
	}
	if len(keys) != 2 || keys[0] != "opened" || keys[1] != "sys.page_view" {
		t.Fatalf("server-kind reserved event leaked into browser config: %v", keys)
	}
}

func TestPublicConfigReservedEventBudgetGuard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		wantErr string
	}{
		{"at limit", PublicConfigEventLimit - 1, ""},
		{"over limit", PublicConfigEventLimit, "PUBLIC_CONFIG_LIMIT_EXCEEDED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make([]ReservedEvent, tc.count)
			for i := range events {
				events[i] = ReservedEvent{Key: "sys.event_" + strconv.Itoa(i), Version: 1, Kinds: []string{ingestion.SourceKindBrowser}, Schema: map[string]any{"type": "object"}}
			}
			store := &memoryStore{snapshot: reservedBrowserSnapshot(t, ingestion.SourceKindBrowser)}
			s := newTestService(t, store, probeFunc(successfulProbe)).WithReservedEvents(reservedProviderStub{events: events})
			out, e := s.PublicConfig(context.Background(), "public-key", Authentication{Origin: "https://site.example"}, 0)
			if tc.wantErr != "" {
				code(t, e, tc.wantErr)
				return
			}
			if e != nil || len(out.Config.Events) != PublicConfigEventLimit {
				t.Fatalf("boundary config invalid: %v %d", e, len(out.Config.Events))
			}
		})
	}
}
