// Package telemetry signs and verifies short-lived browser collection grants.
// Browser write keys are public source identifiers, not secrets; the grant
// proves that the server recently authorized this exact source/project/
// environment/origin/config revision and request write key.
package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Version = "evia.telemetry.v1"

type Claims struct {
	Version      string `json:"v"`
	SourceID     string `json:"sid"`
	ProjectID    string `json:"pid"`
	Environment  string `json:"env"`
	Origin       string `json:"org"`
	Revision     int64  `json:"rev"`
	WriteKeyHash string `json:"wkh"`
	ExpiresAt    int64  `json:"exp"`
}

type Signer struct{ key []byte }

func New(key []byte) (*Signer, error) {
	if len(key) != 32 {
		return nil, errors.New("telemetry signing key must be 32 bytes")
	}
	return &Signer{key: append([]byte(nil), key...)}, nil
}

func HashWriteKey(writeKey string) string {
	hash := sha256.Sum256([]byte(writeKey))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func (s *Signer) Sign(claims Claims) (string, error) {
	claims.Version = Version
	if claims.SourceID == "" || claims.ProjectID == "" || claims.Environment == "" || claims.Origin == "" || claims.Revision < 1 || claims.WriteKeyHash == "" || claims.ExpiresAt <= time.Now().Unix() {
		return "", errors.New("invalid telemetry claims")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Signer) Verify(token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 2048 {
		return Claims{}, errors.New("invalid telemetry token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return Claims{}, errors.New("invalid telemetry token signature")
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return Claims{}, errors.New("invalid telemetry token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) > 1536 {
		return Claims{}, errors.New("invalid telemetry token payload")
	}
	var claims Claims
	if err = json.Unmarshal(payload, &claims); err != nil || claims.Version != Version || claims.ExpiresAt <= now.Unix() || claims.ExpiresAt > now.Add(10*time.Minute).Unix() {
		return Claims{}, errors.New("expired or invalid telemetry token")
	}
	return claims, nil
}
