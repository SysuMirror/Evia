package telemetry

import (
	"strings"
	"testing"
	"time"
)

func TestTokenRoundTripScopeAndExpiry(t *testing.T) {
	signer, err := New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{SourceID: "source", ProjectID: "project", Environment: "development", Origin: "https://site.example", Revision: 7, WriteKeyHash: HashWriteKey("public-key"), ExpiresAt: now.Add(5 * time.Minute).Unix()}
	token, err := signer.Sign(claims)
	if err != nil || strings.Contains(token, "public-key") {
		t.Fatal(token, err)
	}
	verified, err := signer.Verify(token, now)
	if err != nil || verified.SourceID != claims.SourceID || verified.WriteKeyHash != claims.WriteKeyHash || verified.Revision != 7 {
		t.Fatal(verified, err)
	}
	if _, err = signer.Verify(token+"x", now); err == nil {
		t.Fatal("tampered signature must fail")
	}
	if _, err = signer.Verify(token, now.Add(6*time.Minute)); err == nil {
		t.Fatal("expired token must fail")
	}
}
