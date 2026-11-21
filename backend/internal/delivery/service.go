package delivery

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/platform/telemetry"
)

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Invalid(code, message string) error { return fault.New(fault.Invalid, code, message) }
func Unavailable(code string) error {
	return fault.New(fault.Unavailable, code, "发布配置暂不可用，请稍后重试")
}
func validEnvironment(v string) bool {
	return env.Valid(v)
}
func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

type Service struct {
	store                  Store
	probe                  Probe
	validator              Validator
	options                Options
	reserved               ReservedEventProvider
	contentEncryption      *ContentEncryption
	telemetrySigner        *telemetry.Signer
	telemetryTTL           time.Duration
	configSigner           ConfigSigner
	mu                     sync.Mutex
	cache                  map[string]manifestCache
	cacheBytes, cacheRules int
	flights                map[string]*manifestFlight
	slots                  chan struct{}
}

// WithReservedEvents attaches the platform sys.* catalog provider. It is
// optional: a nil provider keeps public config limited to project release
// events, byte-identical to the historical behaviour.
func (s *Service) WithReservedEvents(provider ReservedEventProvider) *Service {
	s.reserved = provider
	return s
}

// WithContentEncryption advertises the active browser encryption public key in
// /v1/config. The public JWK is safe to distribute; private keys remain outside
// the API process. Passing nil keeps the historical response byte-identical.
func (s *Service) WithConfigSigner(signer ConfigSigner) *Service {
	s.configSigner = signer
	return s
}

func (s *Service) WithTelemetrySigner(signer *telemetry.Signer, ttl time.Duration) *Service {
	s.telemetrySigner = signer
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	s.telemetryTTL = ttl
	return s
}

func (s *Service) WithContentEncryption(encryption *ContentEncryption) *Service {
	if encryption == nil {
		s.contentEncryption = nil
		return s
	}
	clone := *encryption
	clone.PublicJWK = make(map[string]any, len(encryption.PublicJWK))
	for key, value := range encryption.PublicJWK {
		clone.PublicJWK[key] = value
	}
	s.contentEncryption = &clone
	return s
}

func New(store Store, probe Probe, validator Validator, options Options) (*Service, error) {
	if store == nil || probe == nil || validator == nil {
		return nil, errors.New("delivery requires persistence, actual pipeline probe and contract validator")
	}
	if options.CompatibilityWindow == 0 {
		options.CompatibilityWindow = 24 * time.Hour
	}
	if options.CompatibilityWindow < time.Minute || options.CompatibilityWindow > 30*24*time.Hour {
		return nil, errors.New("invalid release compatibility window")
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = time.Minute
	}
	if options.LeaseDuration < 30*time.Second || options.LeaseDuration > 5*time.Minute {
		return nil, errors.New("invalid readiness lease duration")
	}
	if options.ProbeTimeout == 0 {
		options.ProbeTimeout = 10 * time.Second
	}
	if options.ProbeTimeout < time.Second || options.ProbeTimeout > 20*time.Second {
		return nil, errors.New("invalid pipeline verification deadline")
	}
	return &Service{store: store, probe: probe, validator: validator, options: options, cache: map[string]manifestCache{}, flights: map[string]*manifestFlight{}, slots: make(chan struct{}, 8)}, nil
}
func (s *Service) Activate(ctx context.Context, actor, project, key string, in ActivationInput) (Activation, error) {
	if !uuid.MatchString(project) || !uuid.MatchString(in.ReleaseID) || !validEnvironment(in.Environment) || in.ExpectedActiveRevision < 0 || in.ExpectedActiveRevision >= MaxRevision {
		return Activation{}, Invalid("INVALID_ACTIVATION", "激活范围或版本无效")
	}
	if !validKey(key) {
		return Activation{}, Invalid("INVALID_IDEMPOTENCY_KEY", "请提供 1 到 128 个可见 ASCII 字符的幂等键")
	}
	hash, e := canonical.Hash(in)
	if e != nil {
		return Activation{}, e
	}
	candidate, replay, e := s.store.PrepareActivation(ctx, actor, project, key, hash, in)
	if e != nil {
		return Activation{}, e
	}
	if replay != nil {
		return *replay, nil
	}
	if candidate.ProjectID != project || candidate.Environment != in.Environment || candidate.ReleaseID != in.ReleaseID {
		return Activation{}, Unavailable("RELEASE_SCOPE_MISMATCH")
	}
	if _, e = s.checkManifest(ctx, candidate); e != nil {
		return Activation{}, e
	}
	proof, e := s.verify(ctx, candidate)
	if e != nil {
		return Activation{}, e
	}
	return s.store.CommitActivation(ctx, actor, project, key, hash, in, candidate, proof, s.options.CompatibilityWindow, s.options.LeaseDuration)
}
func (s *Service) verify(ctx context.Context, candidate Candidate) (Proof, error) {
	now := time.Now().UTC()
	request := ProbeRequest{ProjectID: candidate.ProjectID, Environment: candidate.Environment, ReleaseID: candidate.ReleaseID, Revision: candidate.Revision, ManifestHash: candidate.ManifestHash, Nonce: ids.New(), IssuedAt: now, ExpiresAt: now.Add(s.options.ProbeTimeout)}
	bounded, cancel := context.WithTimeout(ctx, s.options.ProbeTimeout)
	defer cancel()
	proof, e := s.probe.Verify(bounded, request)
	if e != nil {
		return Proof{}, Unavailable("PIPELINE_NOT_READY")
	}
	if e = ValidateProof(request, proof, time.Now().UTC()); e != nil {
		return Proof{}, e
	}
	return proof, nil
}
func ValidateProof(request ProbeRequest, proof Proof, now time.Time) error {
	if proof.ProbeRequest != request || proof.Capability != Capability || proof.ObservedAt.Before(request.IssuedAt.Add(-time.Second)) || proof.ObservedAt.After(now.Add(time.Second)) || proof.ObservedAt.Before(now.Add(-20*time.Second)) || !request.ExpiresAt.After(now) || !uuid.MatchString(request.Nonce) || !digestPattern.MatchString(request.ManifestHash) {
		return Unavailable("PIPELINE_PROOF_INVALID")
	}
	return nil
}
func (s *Service) authorize(source Source, auth Authentication) (string, error) {
	if source.ID == "" {
		return "", fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
	}
	if source.Status != ingestion.SourceStatusActive {
		return "", fault.New(fault.Forbidden, "SOURCE_DISABLED", "来源已停用")
	}
	if source.ExpiresAt != nil && !source.ExpiresAt.After(time.Now()) {
		return "", fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据已失效")
	}
	switch source.Kind {
	case ingestion.SourceKindBrowser:
		if auth.Bearer != "" {
			return "", fault.New(fault.Forbidden, "BROWSER_AUTH_FORBIDDEN", "浏览器来源不得携带服务端凭据")
		}
		for _, origin := range source.AllowedOrigins {
			if origin != "" && origin == auth.Origin {
				return origin, nil
			}
		}
		return "", fault.New(fault.Forbidden, "SOURCE_ORIGIN_FORBIDDEN", "该来源不允许此 Origin")
	case ingestion.SourceKindServer:
		sum := sha256.Sum256([]byte(auth.Bearer))
		digest := hex.EncodeToString(sum[:])
		if auth.Origin != "" || len(auth.Bearer) != 43 || subtle.ConstantTimeCompare([]byte(digest), []byte(source.SecretDigest)) != 1 {
			return "", fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
		}
		return "", nil
	default:
		return "", Unavailable("SOURCE_CONFIGURATION_INVALID")
	}
}
func (s *Service) AuthorizeConfig(ctx context.Context, key string, auth Authentication) (string, error) {
	if !validKey(key) {
		return "", fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
	}
	source, e := s.store.ConfigSource(ctx, key)
	if e != nil {
		return "", e
	}
	return s.authorize(source, auth)
}
func (s *Service) PublicConfig(ctx context.Context, key string, auth Authentication, current int64) (ConfigResult, error) {
	out := ConfigResult{}
	if !validKey(key) || current < 0 || current > MaxRevision {
		return out, Invalid("INVALID_CONFIG_QUERY", "配置查询参数无效")
	}
	snapshot, e := s.store.ReadConfig(ctx, key, current)
	if snapshot.Source.ID != "" {
		origin, authErr := s.authorize(snapshot.Source, auth)
		if authErr != nil {
			return out, authErr
		}
		out.AllowedOrigin = origin
	}
	if e != nil {
		return out, e
	}
	if snapshot.Source.ID == "" {
		return out, fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
	}
	if snapshot.Active.ProjectID != snapshot.Source.ProjectID || snapshot.Active.Environment != snapshot.Source.Environment {
		return out, Unavailable("RELEASE_SCOPE_MISMATCH")
	}
	if snapshot.Current != nil && (snapshot.Current.ProjectID != snapshot.Source.ProjectID || snapshot.Current.Environment != snapshot.Source.Environment) {
		return out, Unavailable("RELEASE_SCOPE_MISMATCH")
	}
	active, e := s.checkManifest(ctx, snapshot.Active)
	if e != nil {
		return out, e
	}
	if !snapshot.Active.Ready || !snapshot.Active.ReadyUntil.After(time.Now()) {
		return out, Unavailable("RELEASE_NOT_READY")
	}
	chosen := active
	if current > 0 && current != snapshot.Active.Revision {
		if snapshot.Current == nil {
			return out, fault.New(fault.Conflict, "CONFIG_REVISION_NOT_ACTIVATED", "请求的版本未曾激活")
		}
		prior, err := s.checkManifest(ctx, *snapshot.Current)
		if err != nil {
			return out, err
		}
		if !canUpgrade(prior, active, snapshot.Source.Kind) {
			if !snapshot.CurrentAccepted {
				return out, fault.New(fault.Conflict, "CONFIG_COMPATIBILITY_EXPIRED", "旧版本兼容期已结束，请更新接入配置")
			}
			if !snapshot.Current.Ready || !snapshot.Current.ReadyUntil.After(time.Now()) {
				return out, Unavailable("RELEASE_NOT_READY")
			}
			chosen = prior
		}
	}
	out.Config = Config{Format: "evia.public-config.v1", ProjectID: snapshot.Source.ProjectID, Environment: snapshot.Source.Environment, Revision: chosen.Revision, Events: []ConfigEvent{}}
	for _, event := range chosen.Events {
		if allows(event.Kinds, snapshot.Source.Kind) {
			out.Config.Events = append(out.Config.Events, ConfigEvent{Key: event.Key, Version: event.Version, Schema: publicSchema(event.Schema)})
		}
	}
	// Platform sys.* events follow the same source-kind allowlist. They cannot
	// collide with release events: project catalog keys reject the dot character
	// while every reserved key carries the sys. prefix.
	if s.reserved != nil {
		for _, event := range s.reserved.ReservedEvents() {
			if !allows(event.Kinds, snapshot.Source.Kind) {
				continue
			}
			out.Config.Events = append(out.Config.Events, ConfigEvent{Key: event.Key, Version: event.Version, Schema: publicSchema(event.Schema)})
		}
		if len(out.Config.Events) > PublicConfigEventLimit {
			return ConfigResult{}, Unavailable("PUBLIC_CONFIG_LIMIT_EXCEEDED")
		}
	}
	if s.contentEncryption != nil {
		clone := *s.contentEncryption
		clone.PublicJWK = make(map[string]any, len(s.contentEncryption.PublicJWK))
		for key, value := range s.contentEncryption.PublicJWK {
			clone.PublicJWK[key] = value
		}
		out.Config.ContentEncryption = &clone
	}
	if s.telemetrySigner != nil && snapshot.Source.Kind == "browser" && auth.Origin != "" {
		// Bucket issuance so the ETag/token stays stable inside one TTL window;
		// the next bucket rotates before the prior token expires.
		expires := time.Now().UTC().Truncate(s.telemetryTTL).Add(2 * s.telemetryTTL)
		token, signErr := s.telemetrySigner.Sign(telemetry.Claims{
			SourceID: snapshot.Source.ID, ProjectID: snapshot.Source.ProjectID,
			Environment: snapshot.Source.Environment, Origin: auth.Origin,
			Revision: chosen.Revision, WriteKeyHash: telemetry.HashWriteKey(key), ExpiresAt: expires.Unix(),
		})
		if signErr != nil {
			return ConfigResult{}, signErr
		}
		out.Config.TelemetryToken = token
		out.Config.TelemetryExpiresAt = &expires
	}
	hash, e := canonical.Hash(out.Config)
	if e != nil {
		return ConfigResult{}, e
	}
	if s.configSigner != nil {
		keyID, signature, signErr := s.configSigner.SignDigest(hash)
		if signErr != nil {
			return ConfigResult{}, signErr
		}
		out.Config.ConfigSignature = &ConfigSignature{
			KeyID: keyID, Algorithm: "Ed25519", DigestSHA256: hash, Signature: signature,
		}
	}
	out.ETag = "\"" + hash + "\""
	return out, nil
}
func allows(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}
func (s *Service) Renew(ctx context.Context, candidate Candidate) error {
	if _, e := s.checkManifest(ctx, candidate); e != nil {
		return e
	}
	proof, e := s.verify(ctx, candidate)
	if e != nil {
		return e
	}
	return s.store.RenewReadiness(ctx, candidate, proof, s.options.LeaseDuration)
}

func (s *Service) GetActivation(ctx context.Context, actor, project, environment string) (Activation, error) {
	if !uuid.MatchString(project) || !validEnvironment(environment) {
		return Activation{}, Invalid("INVALID_ACTIVATION", "激活范围或环境无效")
	}
	return s.store.GetActivation(ctx, actor, project, environment)
}
