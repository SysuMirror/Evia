package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type auditPosition struct {
	OccurredAt string `json:"occurred_at"`
	ID         string `json:"id"`
}

func auditCursorScope(reader, project string, filter access.AuditFilter) string {
	// JSON preserves filter boundaries even if new accepted action characters
	// are introduced later. Equivalent timezone offsets share one UTC scope.
	data, _ := json.Marshal(struct{ Reader, Project, From, To, Action, Actor, Environment, Outcome, ResourcePrefix string }{
		reader, project, governanceTime(filter.From), governanceTime(filter.To), filter.Action, filter.ActorID,
		filter.Environment, filter.Outcome, filter.ResourceKeyPrefix,
	})
	digest := sha256.Sum256(data)
	return "project_audit:" + hex.EncodeToString(digest[:])
}

func (a *API) listAuditLogs(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "from", "to", "action", "actor_id", "environment", "outcome", "resource_key_prefix", "limit", "cursor")
	if err != nil {
		return err
	}
	from, fromErr := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, q.Get("to"))
	if fromErr != nil || toErr != nil {
		return fault.New(fault.Invalid, "INVALID_AUDIT_RANGE", "请提供有效的审计起止时间")
	}
	filter := access.AuditFilter{
		From: from.UTC(), To: to.UTC(), Action: q.Get("action"), ActorID: q.Get("actor_id"),
		Environment: q.Get("environment"), Outcome: q.Get("outcome"), ResourceKeyPrefix: q.Get("resource_key_prefix"),
	}
	if err = access.ValidateAuditQuery(actor.ID, project, filter, nil); err != nil {
		return err
	}
	limit, err := governanceLimit(q)
	if err != nil {
		return err
	}
	scope := auditCursorScope(actor.ID, project, filter)
	raw, err := a.Cursor.Decode(scope, q.Get("cursor"))
	if err != nil {
		return governanceCursorError()
	}
	var after *access.AuditPosition
	if raw != "" {
		var position auditPosition
		if governanceDecodePosition(raw, &position) != nil {
			return governanceCursorError()
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, position.OccurredAt)
		if err != nil {
			return governanceCursorError()
		}
		after = &access.AuditPosition{OccurredAt: occurredAt.UTC(), ID: position.ID}
		if err = access.ValidateAuditQuery(actor.ID, project, filter, after); err != nil {
			return err
		}
	}
	entries, err := a.AuditLogs.List(r.Context(), actor.ID, project, filter, after, limit)
	if err != nil {
		return err
	}
	out := wire.ProjectAuditList{Items: []wire.ProjectAuditEntry{}}
	if len(entries) > limit {
		entry := entries[limit-1]
		data, err := json.Marshal(auditPosition{OccurredAt: governanceTime(entry.OccurredAt), ID: entry.ID})
		if err != nil {
			return err
		}
		next := a.Cursor.Encode(scope, string(data))
		out.NextCursor = &next
		entries = entries[:limit]
	}
	for _, entry := range entries {
		out.Items = append(out.Items, wire.ProjectAuditEntry{
			ID: entry.ID, ProjectID: entry.ProjectID, ActorID: entry.ActorID, ActorKind: entry.ActorKind,
			CredentialID: entry.CredentialID, Environment: entry.Environment, Action: entry.Action,
			ResourceKey: entry.ResourceKey, Outcome: entry.Outcome, OccurredAt: governanceTime(entry.OccurredAt),
		})
	}
	return a.Boundary.JSON(w, 200, "ProjectAuditList", out)
}
