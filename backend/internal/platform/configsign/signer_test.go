package configsign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSignerLoadsPrivateJWKAndSignsDigest(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := map[string]string{
		"kty": "OKP", "crv": "Ed25519",
		"x": base64.RawURLEncoding.EncodeToString(public),
		"d": base64.RawURLEncoding.EncodeToString(private.Seed()),
	}
	raw, _ := json.Marshal(jwk)
	path := filepath.Join(t.TempDir(), "signing.jwk")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := NewFromFile(path, "config-key-1")
	if err != nil {
		t.Fatal(err)
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyID, encoded, err := signer.SignDigest(digest)
	if err != nil || keyID != "config-key-1" {
		t.Fatal(keyID, err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !ed25519.Verify(public, []byte(digest), signature) {
		t.Fatal("signature did not verify", err)
	}
}
