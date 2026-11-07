package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeDelivery struct {
	calls   int
	err     error
	result  delivery.ConfigResult
	auth    delivery.Authentication
	current int64
}

func (f *fakeDelivery) Activate(context.Context, string, string, string, delivery.ActivationInput) (delivery.Activation, error) {
	f.calls++
	return delivery.Activation{Environment: "development", ActiveReleaseRevision: 1}, f.err
}
func (f *fakeDelivery) PublicConfig(_ context.Context, _ string, auth delivery.Authentication, current int64) (delivery.ConfigResult, error) {
	f.calls++
	f.auth = auth
	f.current = current
	return f.result, f.err
}
func (f *fakeDelivery) AuthorizeConfig(_ context.Context, _ string, auth delivery.Authentication) (string, error) {
	f.calls++
	f.auth = auth
	if f.err != nil {
		return "", f.err
	}
	return auth.Origin, nil
}
func deliveryAPI(t *testing.T, f *fakeDelivery) http.Handler {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	return (&API{Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: &catalogAccess{fakeAccess: &fakeAccess{}}, Delivery: f, Origin: "https://console.example"}).Handler()
}
func configResult() delivery.ConfigResult {
	return delivery.ConfigResult{Config: delivery.Config{Format: "evia.public-config.v1", ProjectID: projectID, Environment: "development", Revision: 1, Events: []delivery.ConfigEvent{}}, ETag: `"` + strings.Repeat("a", 64) + `"`, AllowedOrigin: "https://website.example"}
}
func publicConfigRequest(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestActivationHTTPContractSecurityAndFailure(t *testing.T) {
	const path = "/api/v1/projects/" + projectID + "/release-activations"
	const body = `{"environment":"development","release_id":"33333333-3333-4333-8333-333333333333","expected_active_revision":0}`
	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"success", map[string]string{"Idempotency-Key": "activate-1"}, 200},
		{"csrf", map[string]string{"Idempotency-Key": "activate-1", "X-CSRF-Token": ""}, 403},
		{"missing idempotency", nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDelivery{}
			h := deliveryAPI(t, f)
			w := catalogRequest(h, "POST", path, body, tc.headers)
			catalogStatus(t, w, tc.status)
			if tc.status != 200 && f.calls != 0 {
				t.Fatal("invalid mutation reached delivery")
			}
		})
	}
	f := &fakeDelivery{err: fault.New(fault.Conflict, "ACTIVE_REVISION_CONFLICT", "版本已变化")}
	h := deliveryAPI(t, f)
	out := catalogStatus(t, catalogRequest(h, "POST", path, body, map[string]string{"Idempotency-Key": "activate-1"}), 409)
	if out["code"] != "ACTIVE_REVISION_CONFLICT" {
		t.Fatal(out)
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	catalogStatus(t, w, 401)
}
func TestPublicConfigBootstrapETagAndFreshRevocation(t *testing.T) {
	f := &fakeDelivery{result: configResult()}
	h := deliveryAPI(t, f)
	headers := map[string]string{"Origin": "https://website.example"}
	w := publicConfigRequest(h, "GET", "/v1/config?write_key=public-key", headers)
	out := catalogStatus(t, w, 200)
	if out["format"] != "evia.public-config.v1" || f.current != 0 || len(out["events"].([]any)) != 0 || w.Header().Get("Access-Control-Allow-Origin") != headers["Origin"] || w.Header().Get("ETag") != f.result.ETag {
		t.Fatal(out, w.Header())
	}
	headers["If-None-Match"] = f.result.ETag
	w = publicConfigRequest(h, "GET", "/v1/config?write_key=public-key&current_release_revision=0", headers)
	if w.Code != 304 || w.Body.Len() != 0 || f.calls != 2 {
		t.Fatal("ETag skipped source authorization", w.Code, w.Body.String(), f.calls)
	}
	f.err = fault.New(fault.Forbidden, "SOURCE_DISABLED", "来源已停用")
	f.result.AllowedOrigin = ""
	w = publicConfigRequest(h, "GET", "/v1/config?write_key=public-key", headers)
	out = catalogStatus(t, w, 403)
	if out["code"] != "SOURCE_DISABLED" || f.calls != 3 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("revoked source received cached response", out)
	}
}
func TestPublicConfigStrictQueryHeadersAndPreflight(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		headers      map[string]string
		status       int
	}{
		{"GET", "/v1/config?write_key=x&project_id=forged", nil, 400},
		{"GET", "/v1/config?write_key=x&write_key=y", nil, 400},
		{"GET", "/v1/config?write_key=x&current_release_revision=-1", nil, 400},
		{"GET", "/v1/config?write_key=x&current_release_revision=9007199254740992", nil, 400},
		{"GET", "/v1/config?write_key=x&current_release_revision=01", nil, 400},
		{"GET", "/v1/config?write_key=x", map[string]string{"Cookie": "evia_session=valid"}, 403},
		{"GET", "/v1/config?write_key=x", map[string]string{"Authorization": "Bearer short"}, 401},
		{"GET", "/v1/config?write_key=x", map[string]string{"If-None-Match": "*"}, 400},
		{"OPTIONS", "/v1/config?write_key=x", map[string]string{"Origin": "https://website.example", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "If-None-Match"}, 204},
		{"OPTIONS", "/v1/config?write_key=x", map[string]string{"Origin": "https://website.example", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "Authorization"}, 403},
		{"OPTIONS", "/v1/config?write_key=x", map[string]string{"Origin": "https://website.example", "Access-Control-Request-Method": "POST"}, 400},
	} {
		t.Run(tc.method+tc.path+http.StatusText(tc.status), func(t *testing.T) {
			f := &fakeDelivery{result: configResult()}
			h := deliveryAPI(t, f)
			w := publicConfigRequest(h, tc.method, tc.path, tc.headers)
			if tc.status == 204 {
				if w.Code != 204 || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Access-Control-Allow-Origin") != "https://website.example" {
					t.Fatal(w.Code, w.Header())
				}
				return
			}
			catalogStatus(t, w, tc.status)
			if tc.status >= 400 && f.calls != 0 {
				t.Fatal("invalid query reached delivery")
			}
		})
	}
	f := &fakeDelivery{result: configResult()}
	h := deliveryAPI(t, f)
	w := publicConfigRequest(h, "GET", "/v1/config?write_key=x", map[string]string{"Authorization": "Bearer " + strings.Repeat("s", 43)})
	catalogStatus(t, w, 200)
	if f.auth.Bearer != strings.Repeat("s", 43) {
		t.Fatal("server bearer not forwarded safely")
	}
	var result map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
}
