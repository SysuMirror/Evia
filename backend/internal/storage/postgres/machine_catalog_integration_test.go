//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

type machineCatalogCaller struct {
	ctx    context.Context
	actor  string
	issued access.MachineIssued
}

func catalogMachine(t *testing.T, f catalogFixture, environment string, permissions ...string) machineCatalogCaller {
	t.Helper()
	svc := access.NewMachines(f.s)
	issued, err := svc.CreatePrincipal(context.Background(), f.owner, f.project, ids.New(), access.MachineCreate{
		Name: "Synthetic catalog worker", Environment: environment, Permissions: permissions, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := svc.AuthenticateMachine(context.Background(), issued.SecretOnce)
	if err != nil {
		t.Fatal(err)
	}
	return machineCatalogCaller{ctx: access.WithIdentity(context.Background(), identity), actor: identity.PrincipalID, issued: issued}
}

func TestMachineCatalogMinimumPermissionsAndProjectWideDrafts(t *testing.T) {
	f := catalogDB(t)
	draft := f.event(t, "lesson_opened")
	writer := catalogMachine(t, f, "development", access.PermissionCatalogDraftsWrite)
	if _, err := f.svc.CreateEvent(writer.ctx, writer.actor, f.project, catalog.EventInput{Key: "machine_created", DisplayName: "Created"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SaveDraft(writer.ctx, writer.actor, f.project, "lesson_opened", draft.Revision, publishable()); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.GetDraft(writer.ctx, writer.actor, f.project, "lesson_opened")
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = f.svc.CreateRelease(writer.ctx, writer.actor, f.project, "writer-publish", releaseInput("lesson_opened", draft.Revision+1))
	faultCode(t, err, "MACHINE_FORBIDDEN")
	reader := catalogMachine(t, f, "production", access.PermissionCatalogDraftsRead)
	// Drafts belong to the whole project. This explicit grant reads drafts created
	// by a machine bound to a different environment.
	got, err := f.svc.GetDraft(reader.ctx, reader.actor, f.project, "lesson_opened")
	if err != nil || got.Revision != draft.Revision+1 {
		t.Fatal(got, err)
	}
	if _, err = f.svc.ValidateSample(reader.ctx, reader.actor, f.project, "lesson_opened", got.Revision, map[string]any{"lesson_id": 1}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.SaveDraft(reader.ctx, reader.actor, f.project, "lesson_opened", got.Revision, publishable())
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = f.svc.GetDraft(reader.ctx, reader.actor, f.otherProject, "lesson_opened")
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = f.svc.ListEvents(reader.ctx, reader.actor, f.project, "production", "", 10)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	if _, err = f.svc.GetDraft(context.Background(), f.owner, f.project, "machine_created"); err != nil {
		t.Fatal(err)
	}
}

func TestMachineCatalogReleaseScopeAndRequiredListFilters(t *testing.T) {
	f := catalogDB(t)
	draft := f.event(t, "lesson_opened")
	ctx := context.Background()
	development, err := f.svc.CreateRelease(ctx, f.owner, f.project, "dev", releaseInput("lesson_opened", draft.Revision))
	if err != nil {
		t.Fatal(err)
	}
	changed := publishable()
	changed.Trigger = catalogString("Changed production trigger")
	draft, err = f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", draft.Revision, changed)
	if err != nil {
		t.Fatal(err)
	}
	prodInput := releaseInput("lesson_opened", draft.Revision)
	prodInput.Environment = "production"
	production, err := f.svc.CreateRelease(ctx, f.owner, f.project, "prod", prodInput)
	if err != nil {
		t.Fatal(err)
	}
	reader := catalogMachine(t, f, "development", access.PermissionCatalogReleasesRead)
	if got, err := f.svc.GetRelease(reader.ctx, reader.actor, f.project, development.ID); err != nil || got.ID != development.ID {
		t.Fatal(got, err)
	}
	_, err = f.svc.GetRelease(reader.ctx, reader.actor, f.project, production.ID)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = f.svc.GetRelease(reader.ctx, reader.actor, f.otherProject, development.ID)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	if got, err := f.svc.GetEventVersion(reader.ctx, reader.actor, f.project, "lesson_opened", 1); err != nil || got.SchemaVersion != 1 {
		t.Fatal(got, err)
	}
	_, err = f.svc.GetEventVersion(reader.ctx, reader.actor, f.project, "lesson_opened", 2)
	faultCode(t, err, "EVENT_VERSION_NOT_FOUND")
	_, err = f.svc.ListEvents(reader.ctx, reader.actor, f.project, "development", "", 10)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	for _, env := range []string{"", "production"} {
		_, err = f.svc.ListReleases(reader.ctx, reader.actor, f.project, env, "", 10)
		faultCode(t, err, "MACHINE_FORBIDDEN")
		_, err = f.s.GetActivation(reader.ctx, reader.actor, f.project, env)
		faultCode(t, err, "MACHINE_FORBIDDEN")
	}
	if got, err := f.svc.ListReleases(reader.ctx, reader.actor, f.project, "development", "", 10); err != nil || len(got) != 1 || got[0].ID != development.ID {
		t.Fatal(got, err)
	}
	if got, err := f.s.GetActivation(reader.ctx, reader.actor, f.project, "development"); err != nil || got.ActiveReleaseRevision != 0 {
		t.Fatal(got, err)
	}
	both := catalogMachine(t, f, "development", access.PermissionCatalogDraftsRead, access.PermissionCatalogReleasesRead)
	_, err = f.svc.ListEvents(both.ctx, both.actor, f.project, "", "", 10)
	faultCode(t, err, "INVALID_ENVIRONMENT")
	_, err = f.svc.ListEvents(both.ctx, both.actor, f.project, "production", "", 10)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	if got, err := f.svc.ListEvents(both.ctx, both.actor, f.project, "development", "", 10); err != nil || len(got) != 1 || len(got[0].PublishedVersions) != 1 || got[0].PublishedVersions[0] != 1 {
		t.Fatal(got, err)
	}
}

func TestMachineCatalogPublishActivateReplayAndAuditCredential(t *testing.T) {
	f := catalogDB(t)
	draft := f.event(t, "lesson_opened")
	publisher := catalogMachine(t, f, "development", access.PermissionCatalogReleasesPublish)
	in := releaseInput("lesson_opened", draft.Revision)
	release, err := f.svc.CreateRelease(publisher.ctx, publisher.actor, f.project, "publish", in)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.svc.CreateRelease(publisher.ctx, publisher.actor, f.project, "publish", in); err != nil || got.ID != release.ID {
		t.Fatal(got, err)
	}
	_, err = f.svc.GetDraft(publisher.ctx, publisher.actor, f.project, "lesson_opened")
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = f.svc.GetRelease(publisher.ctx, publisher.actor, f.project, release.ID)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	other := in
	other.Environment = "production"
	_, err = f.svc.CreateRelease(publisher.ctx, publisher.actor, f.project, "publish", other)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	probe := &policyProbe{}
	deliver := deliveryService(t, f, probe)
	_, err = deliver.Activate(publisher.ctx, publisher.actor, f.project, "activate", activationInput(release, 0))
	faultCode(t, err, "MACHINE_FORBIDDEN")
	if probe.calls.Load() != 0 {
		t.Fatal("publish authority caused activation probe")
	}
	activator := catalogMachine(t, f, "development", access.PermissionCatalogReleasesActivate)
	if got, err := deliver.Activate(activator.ctx, activator.actor, f.project, "activate", activationInput(release, 0)); err != nil || got.ActiveReleaseRevision != 1 {
		t.Fatal(got, err)
	}
	calls := probe.calls.Load()
	if _, err = deliver.Activate(activator.ctx, activator.actor, f.project, "activate", activationInput(release, 0)); err != nil || probe.calls.Load() != calls {
		t.Fatal(err)
	}
	for _, entry := range []struct{ actor, credential, action string }{
		{publisher.actor, publisher.issued.Credential.ID, "catalog.release_built"},
		{activator.actor, activator.issued.Credential.ID, "catalog.release_activated"},
	} {
		var credential string
		if err = f.s.Pool.QueryRow(context.Background(), `SELECT credential_id::text FROM audit_entries WHERE actor_id=$1 AND action=$2`, entry.actor, entry.action).Scan(&credential); err != nil || credential != entry.credential {
			t.Fatal("audit credential mismatch", credential, err)
		}
	}
	machines := access.NewMachines(f.s)
	if _, err = machines.RevokePrincipal(context.Background(), f.owner, f.project, publisher.actor, 1); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.CreateRelease(publisher.ctx, publisher.actor, f.project, "publish", in)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	if _, err = machines.RevokePrincipal(context.Background(), f.owner, f.project, activator.actor, 1); err != nil {
		t.Fatal(err)
	}
	_, err = deliver.Activate(activator.ctx, activator.actor, f.project, "activate", activationInput(release, 0))
	faultCode(t, err, "MACHINE_FORBIDDEN")
	if probe.calls.Load() != calls {
		t.Fatal("revoked activation replay caused probe")
	}
}

func TestMachineSourcesActualScopeSecretGrantsAndRevokedReplay(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	production := sourceInput("server")
	production.Environment = "production"
	prod, err := svc.CreateSource(ctx, f.owner, f.project, production)
	if err != nil {
		t.Fatal(err)
	}
	writer := catalogMachine(t, f, "development", access.PermissionSourcesWrite)
	created, err := svc.CreateSource(writer.ctx, writer.actor, f.project, sourceInput("server"))
	if err != nil || created.SecretOnce == "" {
		t.Fatal("write grant must return new server secret", err)
	}
	if _, err = svc.UpdateSource(writer.ctx, writer.actor, f.project, created.Source.ID, created.Source.Revision, ingestion.SourceUpdate{Status: sourceString("disabled"), AllowedOrigins: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateSource(writer.ctx, writer.actor, f.project, production)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = svc.UpdateSource(writer.ctx, writer.actor, f.project, prod.Source.ID, prod.Source.Revision, ingestion.SourceUpdate{Status: sourceString("disabled")})
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = svc.GetSource(writer.ctx, writer.actor, f.project, created.Source.ID)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = svc.RotateSource(writer.ctx, writer.actor, f.project, created.Source.ID, "rotate", 0)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	reader := catalogMachine(t, f, "development", access.PermissionSourcesRead)
	if got, err := svc.GetSource(reader.ctx, reader.actor, f.project, created.Source.ID); err != nil || got.WriteKey == "" {
		t.Fatal(got, err)
	}
	if got, err := svc.ListSources(reader.ctx, reader.actor, f.project, "development", "", 10); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	for _, env := range []string{"", "production"} {
		_, err = svc.ListSources(reader.ctx, reader.actor, f.project, env, "", 10)
		faultCode(t, err, "MACHINE_FORBIDDEN")
	}
	_, err = svc.GetSource(reader.ctx, reader.actor, f.project, prod.Source.ID)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = svc.GetSource(reader.ctx, reader.actor, f.otherProject, created.Source.ID)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	rotator := catalogMachine(t, f, "development", access.PermissionSourcesRotate)
	rotated, err := svc.RotateSource(rotator.ctx, rotator.actor, f.project, created.Source.ID, "rotate", 0)
	if err != nil || rotated.SecretOnce == "" {
		t.Fatal("rotate grant must recover new server secret", err)
	}
	if replay, err := svc.RotateSource(rotator.ctx, rotator.actor, f.project, created.Source.ID, "rotate", 0); err != nil || replay.SecretOnce != rotated.SecretOnce {
		t.Fatal("rotation replay changed secret", err)
	}
	_, err = svc.RotateSource(rotator.ctx, rotator.actor, f.project, prod.Source.ID, "rotate", 0)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	var credential string
	if err = f.s.Pool.QueryRow(ctx, `SELECT credential_id::text FROM audit_entries WHERE actor_id=$1 AND action='source.credential_rotated'`, rotator.actor).Scan(&credential); err != nil || credential != rotator.issued.Credential.ID {
		t.Fatal(credential, err)
	}
	if _, err = access.NewMachines(f.s).RevokeCredential(ctx, f.owner, f.project, rotator.actor, rotator.issued.Credential.ID, 1); err != nil {
		t.Fatal(err)
	}
	_, err = svc.RotateSource(rotator.ctx, rotator.actor, f.project, created.Source.ID, "rotate", 0)
	faultCode(t, err, "MACHINE_FORBIDDEN")
}

func TestMachineCatalogRechecksExactCredentialAfterAuthentication(t *testing.T) {
	f := catalogDB(t)
	draft := f.event(t, "lesson_opened")
	machine := catalogMachine(t, f, "development", access.PermissionCatalogDraftsRead, access.PermissionCatalogReleasesPublish)
	in := releaseInput("lesson_opened", draft.Revision)
	if _, err := f.svc.CreateRelease(machine.ctx, machine.actor, f.project, "publish", in); err != nil {
		t.Fatal(err)
	}
	// Rotation with zero overlap expires the already authenticated credential,
	// while the same principal and its replacement credential remain active.
	machines := access.NewMachines(f.s)
	rotated, err := machines.RotateCredential(context.Background(), f.owner, f.project, machine.actor, 1, "expire-old", access.MachineRotate{ExpiresAt: time.Now().UTC().Add(time.Hour), OverlapSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.GetDraft(machine.ctx, machine.actor, f.project, "lesson_opened")
	faultCode(t, err, "MACHINE_FORBIDDEN")
	_, err = f.svc.CreateRelease(machine.ctx, machine.actor, f.project, "publish", in)
	faultCode(t, err, "MACHINE_FORBIDDEN")
	identity, err := machines.AuthenticateMachine(context.Background(), rotated.SecretOnce)
	if err != nil {
		t.Fatal(err)
	}
	fresh := access.WithIdentity(context.Background(), identity)
	if _, err = f.svc.GetDraft(fresh, machine.actor, f.project, "lesson_opened"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CreateRelease(fresh, machine.actor, f.project, "publish", in); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.GetDraft(context.Background(), machine.actor, f.project, "lesson_opened")
	faultCode(t, err, "PROJECT_NOT_FOUND")
	_, err = f.svc.GetDraft(fresh, f.owner, f.project, "lesson_opened")
	faultCode(t, err, "MACHINE_FORBIDDEN")
}
