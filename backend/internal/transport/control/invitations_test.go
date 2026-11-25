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
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeAccountsService struct {
	profile     access.AccountProfile
	lastDisplay string
	rotated     access.Session
	secret      string
}

func (f *fakeAccountsService) Profile(_ context.Context, _ string) (access.AccountProfile, error) {
	return f.profile, nil
}
func (f *fakeAccountsService) UpdateProfile(_ context.Context, _ string, displayName string) (access.AccountProfile, error) {
	f.lastDisplay = displayName
	f.profile.DisplayName = displayName
	return f.profile, nil
}
func (f *fakeAccountsService) ChangePassword(_ context.Context, _ access.Session, _, _ string) (access.Session, string, error) {
	return f.rotated, f.secret, nil
}

type fakeInvitationsService struct {
	items      []access.ProjectInvitation
	issued     access.InvitationIssued
	preview    access.InvitationPreview
	acceptRes  access.InvitationAcceptResult
	lastAccept access.InvitationAcceptInput
}

func (f *fakeInvitationsService) List(_ context.Context, _, _ string, _ *access.InvitationPosition, _ int) ([]access.ProjectInvitation, error) {
	return f.items, nil
}
func (f *fakeInvitationsService) Create(_ context.Context, _, _ string, _ access.InvitationCreateInput) (access.InvitationIssued, error) {
	return f.issued, nil
}
func (f *fakeInvitationsService) Revoke(_ context.Context, _, _, _ string) (access.ProjectInvitation, error) {
	inv := f.issued.Invitation
	inv.Status = access.InvitationStatusRevoked
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	inv.RevokedAt = &now
	return inv, nil
}
func (f *fakeInvitationsService) Preview(_ context.Context, _, _ string) (access.InvitationPreview, error) {
	return f.preview, nil
}
func (f *fakeInvitationsService) Accept(_ context.Context, in access.InvitationAcceptInput) (access.InvitationAcceptResult, error) {
	f.lastAccept = in
	return f.acceptRes, nil
}

func TestAccountAndInvitationHTTPContracts(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	invID := "55555555-5555-4555-8555-555555555555"
	accSvc := &fakeAccountsService{
		profile: access.AccountProfile{UserID: userID, Username: "owner", DisplayName: "Owner", CreatedAt: now},
		rotated: access.Session{UserID: userID, DisplayName: "Owner", CSRF: "rotated-csrf"},
		secret:  strings.Repeat("r", 43),
	}
	invSvc := &fakeInvitationsService{
		items: []access.ProjectInvitation{
			{ID: invID, ProjectID: projectID, Role: "editor", Note: "hello", Status: "pending", CreatedBy: userID, CreatedByDisplayName: "Owner", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
			{ID: "66666666-6666-4666-8666-666666666666", ProjectID: projectID, Role: "viewer", Note: "", Status: "pending", CreatedBy: userID, CreatedByDisplayName: "Owner", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour)},
		},
		issued: access.InvitationIssued{
			Invitation:  access.ProjectInvitation{ID: invID, ProjectID: projectID, Role: "editor", Note: "hello", Status: "pending", CreatedBy: userID, CreatedByDisplayName: "Owner", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
			InviteToken: strings.Repeat("t", 43),
		},
		preview: access.InvitationPreview{ProjectID: projectID, ProjectName: "Demo", Role: "editor", Note: "hello", ExpiresAt: now.Add(24 * time.Hour)},
		acceptRes: access.InvitationAcceptResult{
			Session:       access.Session{UserID: "77777777-7777-4777-8777-777777777777", DisplayName: "Alice", CSRF: "new-csrf"},
			SessionSecret: strings.Repeat("n", 43),
			ProjectID:     projectID,
			ProjectName:   "Demo",
			Role:          "editor",
		},
	}
	api := &API{
		Boundary:    &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:      &catalogAccess{fakeAccess: &fakeAccess{}},
		Accounts:    accSvc,
		Invitations: invSvc,
		Cursor:      codec,
		Origin:      "https://evia.example",
	}
	handler := api.Handler()

	// Account profile read, update, and password rotation.
	w := catalogRequest(handler, http.MethodGet, "/api/v1/account", "", nil)
	catalogStatus(t, w, http.StatusOK)
	var prof wire.AccountProfile
	if err := json.Unmarshal(w.Body.Bytes(), &prof); err != nil || prof.Username != "owner" {
		t.Fatalf("unexpected profile: %s err=%v", w.Body.String(), err)
	}

	w = catalogRequest(handler, http.MethodPatch, "/api/v1/account/profile", `{"display_name":"新昵称"}`, nil)
	catalogStatus(t, w, http.StatusOK)
	if accSvc.lastDisplay != "新昵称" {
		t.Fatalf("display name not updated: %q", accSvc.lastDisplay)
	}

	w = catalogRequest(handler, http.MethodPost, "/api/v1/account/password", `{"current_password":"old-password-123","new_password":"new-password-456"}`, nil)
	catalogStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Header().Get("Set-Cookie"), "evia_session="+strings.Repeat("r", 43)) {
		t.Fatalf("password change did not rotate cookie: %s", w.Header().Get("Set-Cookie"))
	}

	// Project invitation list pagination + create + revoke.
	base := "/api/v1/projects/" + projectID + "/invitations"
	w = catalogRequest(handler, http.MethodGet, base+"?limit=1", "", nil)
	catalogStatus(t, w, http.StatusOK)
	var list wire.ProjectInvitationList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.NextCursor == nil {
		t.Fatalf("expected 1 item + next_cursor, got %s", w.Body.String())
	}

	w = catalogRequest(handler, http.MethodPost, base, `{"role":"editor","note":"hello","expires_in_hours":24}`, nil)
	catalogStatus(t, w, http.StatusCreated)
	var issued wire.ProjectInvitationIssued
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil || issued.InviteToken == "" {
		t.Fatalf("unexpected create response: %s", w.Body.String())
	}

	w = catalogRequest(handler, http.MethodDelete, base+"/"+invID, "", nil)
	catalogStatus(t, w, http.StatusOK)

	// Public preview and unauthenticated accept set session cookie.
	reqPreview := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/preview", strings.NewReader(`{"token":"`+strings.Repeat("t", 43)+`"}`))
	reqPreview.Header.Set("Content-Type", "application/json")
	reqPreview.Header.Set("Origin", "https://evia.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, reqPreview)
	catalogStatus(t, rec, http.StatusOK)

	reqAccept := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept", strings.NewReader(`{"token":"`+strings.Repeat("t", 43)+`","username":"alice","display_name":"Alice","password":"strong-password-123"}`))
	reqAccept.Header.Set("Content-Type", "application/json")
	reqAccept.Header.Set("Origin", "https://evia.example")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, reqAccept)
	catalogStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "evia_session="+strings.Repeat("n", 43)) {
		t.Fatalf("accept did not issue session cookie: %s", rec.Header().Get("Set-Cookie"))
	}

	// Bearer token rejected on public invitation routes.
	reqBearer := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/preview", strings.NewReader(`{"token":"`+strings.Repeat("t", 43)+`"}`))
	reqBearer.Header.Set("Content-Type", "application/json")
	reqBearer.Header.Set("Authorization", "Bearer some-machine-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, reqBearer)
	catalogStatus(t, rec, http.StatusUnauthorized)
}
