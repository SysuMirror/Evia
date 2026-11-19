package ingestion

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const MaxRevision int64 = 9007199254740991

func Invalid(code, message string) error { return fault.New(fault.Invalid, code, message) }
func ValidEnvironment(s string) bool {
	return env.Valid(s)
}
func MissingSource() error {
	return fault.New(fault.NotFound, "SOURCE_NOT_FOUND", "来源不存在或不可见")
}
func RevisionConflict() error {
	return fault.New(fault.Conflict, "SOURCE_REVISION_CONFLICT", "来源版本已变化，请重新加载")
}
func Digest(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

type SourceService struct {
	store      SourceStore
	aead       cipher.AEAD
	invalidate func()
}

func NewSourceService(store SourceStore, key []byte, invalidate func()) (*SourceService, error) {
	if store == nil {
		return nil, errors.New("source store is required")
	}
	if len(key) != 32 {
		return nil, errors.New("source secret encryption key must contain 32 bytes")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &SourceService{store: store, aead: aead, invalidate: invalidate}, nil
}
func (s *SourceService) changed() {
	if s.invalidate != nil {
		s.invalidate()
	}
}
func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, v := range key {
		if v < 33 || v > 126 {
			return false
		}
	}
	return true
}
func NormalizeOrigins(origins []string) ([]string, error) {
	if len(origins) > 50 {
		return nil, Invalid("INVALID_ORIGINS", "最多允许 50 个来源域名")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(origins))
	for _, origin := range origins {
		u, e := url.Parse(origin)
		if e != nil || len(origin) > 2048 || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(origin, "*?#") || strings.ContainsRune(origin, 0) {
			return nil, Invalid("INVALID_ORIGINS", "来源必须是无路径的完整 http(s) Origin")
		}
		if port := u.Port(); port != "" {
			n, e := strconv.Atoi(port)
			if e != nil || n < 1 || n > 65535 {
				return nil, Invalid("INVALID_ORIGINS", "来源端口无效")
			}
		}
		for _, r := range u.Host {
			if r > 127 {
				return nil, Invalid("INVALID_ORIGINS", "来源主机名必须使用浏览器 Origin 中的 ASCII 编码")
			}
		}
		u.Host = strings.ToLower(u.Host)
		if u.Port() == "443" && u.Scheme == "https" || u.Port() == "80" && u.Scheme == "http" {
			host := u.Hostname()
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			u.Host = host
		}
		origin = u.Scheme + "://" + u.Host
		if !seen[origin] {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (s *SourceService) credential(sourceID, kind string) (Credential, string, error) {
	c := Credential{ID: ids.New(), SourceID: sourceID, WriteKey: "evia_w_" + ids.Secret()}
	if kind != SourceKindServer {
		return c, "", nil
	}
	secret := ids.Secret()
	c.SecretDigest = Digest(secret)
	nonce := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return c, "", e
	}
	c.SecretCiphertext = s.aead.Seal(nonce, nonce, []byte(secret), []byte(sourceID+":"+c.ID))
	return c, secret, nil
}
func (s *SourceService) DecryptCredential(c Credential) (string, error) {
	n := s.aead.NonceSize()
	if len(c.SecretCiphertext) < n {
		return "", errors.New("invalid source credential ciphertext")
	}
	plain, e := s.aead.Open(nil, c.SecretCiphertext[:n], c.SecretCiphertext[n:], []byte(c.SourceID+":"+c.ID))
	if e != nil {
		return "", errors.New("source credential decryption failed")
	}
	return string(plain), nil
}
func (s *SourceService) CreateSource(ctx context.Context, actor, project string, in SourceInput) (Issued, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 || strings.ContainsRune(in.Name, 0) || !utf8.ValidString(in.Name) {
		return Issued{}, Invalid("INVALID_SOURCE_NAME", "来源名称必须为 1 到 100 个字符")
	}
	if !ValidEnvironment(in.Environment) {
		return Issued{}, Invalid("INVALID_ENVIRONMENT", "环境无效")
	}
	if !ValidSourceKind(in.Kind) {
		return Issued{}, Invalid("INVALID_SOURCE_KIND", "来源类型无效")
	}
	origins, e := NormalizeOrigins(in.AllowedOrigins)
	if e != nil {
		return Issued{}, e
	}
	if in.Kind == SourceKindBrowser && len(origins) == 0 {
		return Issued{}, Invalid("INVALID_ORIGINS", "浏览器来源必须配置至少一个 Origin")
	}
	if in.Kind == SourceKindServer && len(origins) != 0 {
		return Issued{}, Invalid("INVALID_ORIGINS", "服务端来源不使用浏览器 Origin 配置")
	}
	src := Source{ID: ids.New(), ProjectID: project, Name: in.Name, Environment: in.Environment, Kind: in.Kind, AllowedOrigins: origins, Status: SourceStatusActive, Revision: 1, ApplicationState: ApplicationStatePending}
	c, secret, e := s.credential(src.ID, in.Kind)
	if e != nil {
		return Issued{}, e
	}
	src.WriteKey = c.WriteKey
	src, e = s.store.CreateSource(ctx, actor, project, src, c)
	if e != nil {
		return Issued{}, e
	}
	s.changed()
	return Issued{Source: src, SecretOnce: secret}, nil
}
func (s *SourceService) GetSource(ctx context.Context, actor, project, id string) (Source, error) {
	return s.store.GetSource(ctx, actor, project, id)
}
func (s *SourceService) ListSources(ctx context.Context, actor, project, env, after string, limit int) ([]Source, error) {
	if (env != "" && !ValidEnvironment(env)) || limit < 1 || limit > 200 {
		return nil, Invalid("INVALID_QUERY", "来源列表条件无效")
	}
	return s.store.ListSources(ctx, actor, project, env, after, limit+1)
}
func (s *SourceService) UpdateSource(ctx context.Context, actor, project, id string, revision int64, in SourceUpdate) (Source, error) {
	if revision < 1 || revision >= MaxRevision {
		return Source{}, Invalid("INVALID_REVISION", "来源版本无效")
	}
	if in.Status == nil && in.AllowedOrigins == nil {
		return Source{}, Invalid("INVALID_SOURCE_UPDATE", "必须指定来源更新字段")
	}
	if in.Status != nil && !ValidSourceStatus(*in.Status) {
		return Source{}, Invalid("INVALID_SOURCE_STATUS", "来源状态无效")
	}
	if in.AllowedOrigins != nil {
		origins, e := NormalizeOrigins(*in.AllowedOrigins)
		if e != nil {
			return Source{}, e
		}
		in.AllowedOrigins = &origins
		prior, e := s.store.GetSourceForUpdate(ctx, actor, project, id)
		if e != nil {
			return Source{}, e
		}
		if prior.Kind == SourceKindServer && len(origins) != 0 {
			return Source{}, Invalid("INVALID_ORIGINS", "服务端来源不使用浏览器 Origin 配置")
		}
	}
	src, e := s.store.UpdateSource(ctx, actor, project, id, revision, in)
	if e == nil {
		s.changed()
	}
	return src, e
}
func (s *SourceService) RotateSource(ctx context.Context, actor, project, id, key string, overlap int64) (Issued, error) {
	if !validKey(key) {
		return Issued{}, Invalid("INVALID_IDEMPOTENCY_KEY", "幂等键必须为 1 到 128 个可见 ASCII 字符")
	}
	if overlap < 0 || overlap > 86400 {
		return Issued{}, Invalid("INVALID_OVERLAP", "凭据重叠期必须在 0 到 86400 秒之间")
	}
	prior, e := s.store.GetSourceForRotation(ctx, actor, project, id)
	if e != nil {
		return Issued{}, e
	}
	c, _, e := s.credential(id, prior.Kind)
	if e != nil {
		return Issued{}, e
	}
	src, c, e := s.store.RotateSource(ctx, actor, project, id, key, overlap, c)
	if e != nil {
		return Issued{}, e
	}
	secret := ""
	if src.Kind == SourceKindServer {
		secret, e = s.DecryptCredential(c)
		if e != nil {
			return Issued{}, e
		}
	}
	s.changed()
	return Issued{Source: src, SecretOnce: secret}, nil
}
