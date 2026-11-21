// Package configsign signs canonical public configuration digests with an
// Ed25519 deployment key. Browsers pin the matching public JWK out-of-band;
// the signing public key is never trusted from the response it authenticates.
package configsign

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
)

type jwk struct {
	Kty, Crv, X, D string
}

type Signer struct {
	keyID string
	key   ed25519.PrivateKey
}

func NewFromFile(path, keyID string) (*Signer, error) {
	if path == "" || keyID == "" {
		return nil, errors.New("config signing key path and key id are required")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > 8*1024 {
		return nil, errors.New("config signing private JWK must be a 0600 file under 8 KiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value jwk
	if err = json.Unmarshal(raw, &value); err != nil || value.Kty != "OKP" || value.Crv != "Ed25519" || value.D == "" || value.X == "" {
		return nil, errors.New("config signing key must be an Ed25519 private JWK")
	}
	seed, err := base64.RawURLEncoding.DecodeString(value.D)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("config signing private seed is invalid")
	}
	key := ed25519.NewKeyFromSeed(seed)
	public, err := base64.RawURLEncoding.DecodeString(value.X)
	if err != nil || len(public) != ed25519.PublicKeySize || !key.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(public)) {
		return nil, errors.New("config signing public key does not match private seed")
	}
	return &Signer{keyID: keyID, key: key}, nil
}

func (s *Signer) SignDigest(digest string) (keyID, signature string, err error) {
	if len(digest) != 64 {
		return "", "", errors.New("config digest must be sha256 hex")
	}
	signatureBytes := ed25519.Sign(s.key, []byte(digest))
	return s.keyID, base64.RawURLEncoding.EncodeToString(signatureBytes), nil
}
