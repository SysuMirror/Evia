//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func TestOverviewAuthorizationHumanAndMachineBoundaries(t *testing.T) {
	f := catalogDB(t)
	if err := f.s.AuthorizeOverview(context.Background(), f.owner, f.project, "development"); err != nil {
		t.Fatal("active human member denied", err)
	}
	if err := f.s.AuthorizeOverview(context.Background(), ids.New(), f.project, "development"); err == nil {
		t.Fatal("outsider human authorized")
	}

	readCtx, issued := queryMachine(t, f, "development", access.PermissionQueriesRead)
	if err := f.s.AuthorizeOverview(readCtx, issued.Principal.ID, f.project, "development"); err != nil {
		t.Fatal("exact machine grant denied", err)
	}
	if err := f.s.AuthorizeOverview(readCtx, issued.Principal.ID, f.project, "production"); err == nil {
		t.Fatal("machine crossed environment")
	}

	runCtx, runOnly := queryMachine(t, f, "development", access.PermissionQueriesRun)
	if err := f.s.AuthorizeOverview(runCtx, runOnly.Principal.ID, f.project, "development"); err == nil {
		t.Fatal("neighboring grant authorized overview")
	}
	if _, err := access.NewMachines(f.s).RevokeCredential(context.Background(), f.owner, f.project, issued.Principal.ID, issued.Credential.ID, issued.Principal.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AuthorizeOverview(readCtx, issued.Principal.ID, f.project, "development"); err == nil {
		t.Fatal("revoked exact credential authorized overview")
	}
}
