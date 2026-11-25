package config

import "testing"

func TestReadOAuth(t *testing.T) {
	// Unconfigured returns empty slice cleanly.
	providers, err := ReadOAuth(func(string) string { return "" })
	if err != nil || len(providers) != 0 {
		t.Fatalf("expected empty providers, got %v err=%v", providers, err)
	}

	// Partial configuration fails closed.
	_, err = ReadOAuth(func(k string) string {
		if k == "EVIA_OAUTH2_UNISSO_CLIENT_ID" {
			return "cid"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected partial OAuth config to fail")
	}

	// Missing issuer without explicit endpoints fails closed (no hardcoded default domain).
	_, err = ReadOAuth(func(k string) string {
		switch k {
		case "EVIA_OAUTH2_UNISSO_CLIENT_ID":
			return "cid"
		case "EVIA_OAUTH2_UNISSO_CLIENT_SECRET":
			return "csec"
		case "EVIA_OAUTH2_UNISSO_REDIRECT_URI":
			return "https://evia.example.com/user/login/oauth2/callback"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("expected missing issuer to fail")
	}

	// Complete configuration succeeds and derives standard UniSSO endpoints from issuer.
	providers, err = ReadOAuth(func(k string) string {
		switch k {
		case "EVIA_OAUTH2_UNISSO_ISSUER":
			return "https://sso.example.com/"
		case "EVIA_OAUTH2_UNISSO_CLIENT_ID":
			return "cid"
		case "EVIA_OAUTH2_UNISSO_CLIENT_SECRET":
			return "csec"
		case "EVIA_OAUTH2_UNISSO_REDIRECT_URI":
			return "https://evia.example.com/user/login/oauth2/callback"
		default:
			return ""
		}
	})
	if err != nil || len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %+v err=%v", providers, err)
	}
	p := providers[0]
	if p.ID != "unisso" || p.AuthorizeURL != "https://sso.example.com/authorize" || p.TokenURL != "https://sso.example.com/api/oauth/token" || p.UserInfoURL != "https://sso.example.com/api/oauth/userinfo" || p.IntrospectURL != "https://sso.example.com/api/oauth/introspect" {
		t.Fatalf("unexpected derived endpoints: %+v", p)
	}
}
