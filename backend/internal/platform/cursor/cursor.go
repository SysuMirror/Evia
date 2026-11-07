// Package cursor signs scope-bound continuation positions without trusting client input.
package cursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Codec struct{ key []byte }
type position struct {
	Scope   string `json:"s"`
	After   string `json:"a"`
	Expires int64  `json:"e"`
}

func New(key []byte) (*Codec, error) {
	if len(key) < 32 {
		return nil, errors.New("cursor signing key must contain at least 32 bytes")
	}
	return &Codec{append([]byte(nil), key...)}, nil
}
func (c *Codec) Encode(scope, after string) string {
	data, _ := json.Marshal(position{scope, after, time.Now().Add(24 * time.Hour).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (c *Codec) Decode(scope, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	invalid := errors.New("invalid or expired cursor")
	if len(token) > 2048 {
		return "", invalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", invalid
	}
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", invalid
	}
	var p position
	if json.Unmarshal(data, &p) != nil || p.Scope != scope || p.Expires <= time.Now().Unix() || p.After == "" {
		return "", invalid
	}
	return p.After, nil
}
