package content

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type rsaJWK struct {
	Kty, N, E, D, P, Q, DP, DQ, QI string
}

// KeyUnwrapper is the KMS/HSM boundary. Implementations receive the opaque
// RSA-OAEP envelope and return one 32-byte AES key; callers never access an RSA
// private key. LocalJWKUnwrapper exists only for development/single-node use.
type KeyUnwrapper interface {
	Unwrap(context.Context, string, []byte) ([]byte, error)
}

type localJWKUnwrapper struct{ key *rsa.PrivateKey }

func (u *localJWKUnwrapper) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	return rsa.DecryptOAEP(sha256.New(), rand.Reader, u.key, wrapped, nil)
}

// RSAOAEPDecryptor authenticates and decrypts chunks after a KeyUnwrapper
// (local secret file, cloud KMS or HSM adapter) unwraps the per-message AES key.
type RSAOAEPDecryptor struct {
	keyID     string
	unwrapper KeyUnwrapper
}

func decodeInteger(value string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("invalid JWK integer")
	}
	return new(big.Int).SetBytes(raw), nil
}

func NewRSAOAEPDecryptorFromFile(path, keyID string) (*RSAOAEPDecryptor, error) {
	if path == "" || keyID == "" {
		return nil, errors.New("content private JWK path and key id are required")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > 32*1024 {
		return nil, errors.New("content private JWK must be a private 0600 file under 32 KiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var jwk rsaJWK
	if err = json.Unmarshal(raw, &jwk); err != nil || jwk.Kty != "RSA" {
		return nil, errors.New("content private JWK must be RSA")
	}
	n, err := decodeInteger(jwk.N)
	if err != nil {
		return nil, err
	}
	d, err := decodeInteger(jwk.D)
	if err != nil {
		return nil, err
	}
	p, err := decodeInteger(jwk.P)
	if err != nil {
		return nil, err
	}
	q, err := decodeInteger(jwk.Q)
	if err != nil {
		return nil, err
	}
	eRaw, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil || len(eRaw) == 0 || len(eRaw) > 4 {
		return nil, errors.New("invalid RSA exponent")
	}
	e := 0
	for _, value := range eRaw {
		e = e<<8 | int(value)
	}
	key := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: n, E: e}, D: d, Primes: []*big.Int{p, q}}
	if err = key.Validate(); err != nil {
		return nil, fmt.Errorf("invalid RSA private key: %w", err)
	}
	key.Precompute()
	return &RSAOAEPDecryptor{keyID: keyID, unwrapper: &localJWKUnwrapper{key: key}}, nil
}

type kmsUnwrapper struct {
	endpoint *url.URL
	token    string
	client   *http.Client
}

type kmsRequest struct {
	KeyID      string `json:"key_id"`
	WrappedKey string `json:"wrapped_key"`
	Algorithm  string `json:"alg"`
}

type kmsResponse struct {
	PlaintextKey string `json:"plaintext_key"`
}

// NewRSAOAEPDecryptorFromKMS builds a vendor-neutral KMS adapter. The endpoint
// accepts a wrapped key and returns the unwrapped AES key over HTTPS. A bearer
// token is loaded from a 0600 secret file; neither token nor plaintext key is
// logged or persisted. Vendor gateways can translate this contract to cloud
// KMS/HSM decrypt operations.
func NewRSAOAEPDecryptorFromKMS(endpointRaw, tokenPath, keyID string, allowInsecureLoopback bool) (*RSAOAEPDecryptor, error) {
	endpoint, err := url.Parse(endpointRaw)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || (endpoint.Scheme != "https" && !(allowInsecureLoopback && endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost"))) {
		return nil, errors.New("content KMS endpoint must use HTTPS")
	}
	info, err := os.Stat(tokenPath)
	if err != nil || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > 8*1024 {
		return nil, errors.New("content KMS token must be a private 0600 file under 8 KiB")
	}
	tokenRaw, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tokenRaw))
	if token == "" || keyID == "" {
		return nil, errors.New("content KMS token and key id are required")
	}
	return &RSAOAEPDecryptor{keyID: keyID, unwrapper: &kmsUnwrapper{
		endpoint: endpoint, token: token, client: &http.Client{Timeout: 10 * time.Second},
	}}, nil
}

func (u *kmsUnwrapper) Unwrap(ctx context.Context, keyID string, wrapped []byte) ([]byte, error) {
	body, err := json.Marshal(kmsRequest{KeyID: keyID, WrappedKey: base64.RawURLEncoding.EncodeToString(wrapped), Algorithm: "RSA-OAEP-256"})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+u.token)
	response, err := u.client.Do(request)
	if err != nil {
		return nil, errors.New("content KMS request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 || response.StatusCode != http.StatusOK {
		return nil, errors.New("content KMS rejected key unwrap")
	}
	var decoded kmsResponse
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("content KMS response is invalid")
	}
	key, err := base64.RawURLEncoding.DecodeString(decoded.PlaintextKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("content KMS returned an invalid AES key")
	}
	return key, nil
}

func decodeURL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (d *RSAOAEPDecryptor) Decrypt(ctx context.Context, chunk EncryptedChunk) (DecryptedChunk, error) {
	if chunk.KeyID != d.keyID || chunk.Algorithm != "A256GCM+RSA-OAEP-256" {
		return DecryptedChunk{}, errors.New("content key id or algorithm is unavailable")
	}
	wrapped, err := decodeURL(chunk.WrappedKey)
	if err != nil {
		return DecryptedChunk{}, errors.New("invalid wrapped key")
	}
	rawKey, err := d.unwrapper.Unwrap(ctx, chunk.KeyID, wrapped)
	if err != nil || len(rawKey) != 32 {
		return DecryptedChunk{}, errors.New("content key unwrap failed")
	}
	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return DecryptedChunk{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return DecryptedChunk{}, err
	}
	nonce, err := decodeURL(chunk.Nonce)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return DecryptedChunk{}, errors.New("invalid content nonce")
	}
	ciphertext, err := decodeURL(chunk.Ciphertext)
	if err != nil {
		return DecryptedChunk{}, errors.New("invalid content ciphertext")
	}
	aadV1 := []byte(fmt.Sprintf(
		"evia-content-v1|%s|%s|%s|%s|%s|%s|%d|%d|%d|%d|%s|%d",
		chunk.ProjectID, chunk.Environment, chunk.KeyID, chunk.StreamID, chunk.ItemID, chunk.ActorType,
		chunk.ChunkIndex, chunk.ChunkCount, chunk.ContentLength, boolInt(chunk.Truncated), chunk.Status, chunk.DurationMS,
	))
	var opened []byte
	paddedV1 := false
	if chunk.AADVersion == "evia-content-v1" && chunk.Padding == "fixed-600-v1" {
		opened, err = gcm.Open(nil, nonce, ciphertext, aadV1)
		paddedV1 = true
	} else if chunk.AADVersion == "" && chunk.Padding == "" {
		// Compatibility for pre-v1 and the short-lived development transition:
		// old chunks used the legacy AAD/plaintext, while transitional padded
		// chunks used v1 AAD before version markers were published.
		legacyAAD := []byte(fmt.Sprintf("%s|%s|%d|%d|%s", chunk.KeyID, chunk.ItemID, chunk.ChunkIndex, chunk.ChunkCount, chunk.ActorType))
		opened, err = gcm.Open(nil, nonce, ciphertext, legacyAAD)
		if err != nil {
			opened, err = gcm.Open(nil, nonce, ciphertext, aadV1)
			paddedV1 = err == nil
		}
	} else {
		return DecryptedChunk{}, errors.New("content AAD or padding version is unsupported")
	}
	if err != nil {
		return DecryptedChunk{}, errors.New("content authentication failed")
	}
	var plaintext []byte
	if paddedV1 {
		if len(opened) != 600 {
			return DecryptedChunk{}, errors.New("content authentication failed")
		}
		length := int(opened[0])<<8 | int(opened[1])
		if length < 1 || length > 598 {
			return DecryptedChunk{}, errors.New("content padding is invalid")
		}
		plaintext = append([]byte(nil), opened[2:2+length]...)
	} else {
		if len(opened) < 1 || len(opened) > 600 {
			return DecryptedChunk{}, errors.New("legacy content chunk is invalid")
		}
		plaintext = opened
	}
	return DecryptedChunk{EncryptedChunk: chunk, Plaintext: plaintext}, nil
}
