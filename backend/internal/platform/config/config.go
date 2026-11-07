package config

import (
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
)

type Config struct {
	DatabaseURL, Listen, Origin string
	CursorKey                   []byte
	SecureCookies               bool
}

func Read(get func(string) string) (Config, error) {
	c := Config{DatabaseURL: get("EVIA_DATABASE_URL"), Listen: get("EVIA_LISTEN"), Origin: strings.TrimSuffix(get("EVIA_PUBLIC_ORIGIN"), "/"), SecureCookies: true}
	if c.DatabaseURL == "" {
		return c, errors.New("EVIA_DATABASE_URL is required")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if _, _, e := net.SplitHostPort(c.Listen); e != nil {
		return c, errors.New("EVIA_LISTEN must be host:port")
	}
	u, e := url.Parse(c.Origin)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return c, errors.New("EVIA_PUBLIC_ORIGIN must be an http(s) origin without path or credentials")
	}
	if get("EVIA_ALLOW_INSECURE_HTTP") != "" && get("EVIA_ALLOW_INSECURE_HTTP") != "true" && get("EVIA_ALLOW_INSECURE_HTTP") != "false" {
		return c, errors.New("EVIA_ALLOW_INSECURE_HTTP must be true or false")
	}
	if u.Scheme == "http" {
		if get("EVIA_ALLOW_INSECURE_HTTP") != "true" {
			return c, errors.New("HTTP development requires EVIA_ALLOW_INSECURE_HTTP=true")
		}
		c.SecureCookies = false
	}
	c.CursorKey, e = hex.DecodeString(get("EVIA_CURSOR_KEY"))
	if e != nil || len(c.CursorKey) != 32 {
		return c, errors.New("EVIA_CURSOR_KEY must be 32 random bytes encoded as 64 hexadecimal characters")
	}
	return c, nil
}
