// Package identity models the versioned identity graph folded from
// sys.identity_link facts. Browser sources emit one link per successful
// identify() carrying a uid and the anonymous device id; the fold collapses
// those bipartite edges into resolved persons (connected components) inside
// one project and environment. Person membership only grows: merges retire
// the younger component and redirect its id, revisions are immutable
// snapshots, and the erasure tombstone barrier keeps deleted identities out
// of the graph. The ClickHouse scan and the PostgreSQL persistence live in
// their own adapters; this package owns the vocabulary, the pure fold and the
// storage contract.
package identity

import (
	"context"
	"time"
)

const (
	PersonActive  = "active"
	PersonRetired = "retired"
)

// PersonStates is the Go-side closed set of person registry states, mirrored
// by the identity_persons CHECK constraint in migration 043.
var PersonStates = []string{PersonActive, PersonRetired}

const (
	RevisionBuilding  = "building"
	RevisionCompleted = "completed"
	RevisionFailed    = "failed"
)

// RevisionStates is the Go-side closed set of revision lifecycle states,
// mirrored by the identity_revisions CHECK constraint in migration 043. v1
// folds write completed (or failed) atomically; building is reserved for a
// future multi-phase build and must not be advertised until then.
var RevisionStates = []string{RevisionBuilding, RevisionCompleted, RevisionFailed}

const (
	MemberUID         = "uid"
	MemberAnonymousID = "anonymous_id"
)

// MemberKinds is the Go-side closed set of graph member kinds.
var MemberKinds = []string{MemberUID, MemberAnonymousID}

// Budgets and operational bounds, decided 2026-11-20 (generous defaults).
const (
	// MaxExpansionMembers bounds query-side member expansion per resolved
	// person; slice 2 rejects with IDENTITY_EXPANSION_EXCEEDED beyond it.
	MaxExpansionMembers = 1024
	// MaxScopeExpansionMembers bounds the whole-graph mapping one unpinned
	// aggregate may materialize; generous by decision, still bounded so plan
	// SQL and job payloads stay predictable.
	MaxScopeExpansionMembers = 65536
	// MaxRedirectDepth bounds person redirect-chain resolution before a
	// fail-closed rebuild is demanded.
	MaxRedirectDepth = 16
	// ScanOverlap re-reads facts this long behind the watermark so late or
	// replayed sys.identity_link rows are folded; edge upserts are idempotent.
	ScanOverlap = time.Hour
	// DefaultScanLimit bounds one fold round's fact batch.
	DefaultScanLimit = 50000
	// DefaultPollInterval spaces fold rounds; leases serialize writers.
	DefaultPollInterval = time.Minute
	// DefaultLease bounds one fold round's claim.
	DefaultLease = 5 * time.Minute
	// DefaultRetentionWindow retains completed revisions for at least 90 days.
	DefaultRetentionWindow = 90 * 24 * time.Hour
	// DefaultRetentionMinCount retains at least the newest 1000 completed
	// revisions even if older than DefaultRetentionWindow (whichever keeps more).
	DefaultRetentionMinCount = 1000
)

// Scope addresses one project/environment graph. Graphs never merge across
// scopes.
type Scope struct {
	ProjectID   string
	Environment string
}

// Edge is one folded uid/anonymous_id pair. Edges are aggregates, not event
// copies: repeated observations advance LastSeenAt and LastRevision only.
type Edge struct {
	UID           string
	AnonymousID   string
	FirstSeenAt   time.Time
	LastSeenAt    time.Time
	FirstRevision int64
	LastRevision  int64
}

// Member is one raw identity owned by a resolved person. Membership moves
// wholesale on merges, so one raw identity belongs to at most one person row
// per scope at any time.
type Member struct {
	ResolvedPersonID string
	Kind             string
	Value            string
	FirstSeenAt      time.Time
	JoinedRevision   int64
}

// Person is the resolved-person registry row. Retired persons keep only their
// redirect; active persons own the member rows.
type Person struct {
	ResolvedPersonID string
	State            string
	SupersededBy     *string
	CreatedAt        time.Time
	CreateRevision   int64
}

// Revision is the immutable snapshot catalog row. Completed rows are read
// only; failed rows keep their error code for diagnosis.
type Revision struct {
	Revision          int64
	Watermark         *time.Time
	EdgeCount         int64
	ActivePersonCount int64
	MemberCount       int64
	State             string
	BuiltAt           *time.Time
	ErrorCode         *string
	SnapshotAvailable bool
}

// LinkScan is one scanned identity-link pair candidate for folding.
// FirstSeenAt/LastSeenAt are the earliest and latest warehouse reception
// times observed for the pair inside one scan window.
type LinkScan struct {
	UID         string
	AnonymousID string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// Graph is the persisted state of one scope loaded for a fold.
type Graph struct {
	Edges   []Edge
	Persons []Person
	Members []Member
}

// FoldClaim is a leased scope handed to the fold worker with its graph.
// NextRevision is allocated under the lease as max(revision)+1 across all
// states, so failed diagnostics rows never collide with later snapshots.
type FoldClaim struct {
	Scope
	Worker                string
	Generation            int64
	Watermark             time.Time
	LastCompletedRevision int64
	NextRevision          int64
	Graph                 *Graph
}

// FoldOutcome reports what one fold round should persist.
type FoldOutcome struct {
	// Changed is false when the scanned candidates add nothing (pure overlap
	// rescan): no revision is written and the watermark stays.
	Changed bool
	// MaxSeen is the greatest scanned received_at; callers advance the
	// watermark monotonically when committing a changed fold.
	MaxSeen time.Time
	// Watermark is the committed watermark for the new revision.
	Watermark time.Time
	// NextRevision is the revision number the commit must allocate.
	NextRevision int64
	// EdgeUpserts lists only new or advanced edges, deterministically ordered.
	EdgeUpserts []Edge
	NewPersons  []Person
	Retire      []Person
	// MovedMembers re-points absorbed members to their surviving person
	// keeping the original first_seen and joined revision.
	MovedMembers      []Member
	NewMembers        []Member
	EdgeCount         int64
	ActivePersonCount int64
	MemberCount       int64
}

// ReadFilter constrains cursor pagination over a scope's revisions.
type ReadFilter struct {
	AfterRevision int64
	Limit         int
}

// Page is a revision collection ordered newest first. NextRevision is the
// continuation position (0 when exhausted); the HTTP boundary binds it into a
// signed cursor.
type Page struct {
	Items        []Revision
	NextRevision int64
}

// Resolution is the forward lookup answer for one raw identity against one
// pinned revision, with redirects already resolved.
type Resolution struct {
	Revision         int64
	ResolvedPersonID string
	Members          []Member
}

// LinkFacts scans deduplicated identity-link pairs from the event warehouse.
// Implementations must return pairs with received_at strictly after the given
// instant, ordered by reception so batches partition a scan window; the
// boolean reports whether the scan window is exhausted (fewer rows than the
// limit).
type LinkFacts interface {
	ScanLinks(ctx context.Context, scope Scope, after time.Time, limit int) ([]LinkScan, bool, error)
}

// Tombstones reports which candidate identities carry an erasure barrier
// row. Fold callers treat any lookup error as fail-closed and skip the round.
type Tombstones interface {
	Erased(ctx context.Context, scope Scope, uids, anonymousIDs []string) (ErasedSet, error)
}

// ErasedSet mirrors erasure.ErasedSet for the fold barrier without importing
// the erasure domain; adapters share one SQL predicate.
type ErasedSet struct {
	UIDs         map[string]struct{}
	AnonymousIDs map[string]struct{}
}

func (s ErasedSet) Contains(kind, value string) bool {
	switch kind {
	case MemberUID:
		_, ok := s.UIDs[value]
		return ok
	case MemberAnonymousID:
		_, ok := s.AnonymousIDs[value]
		return ok
	}
	return false
}

// Store is the persistence contract implemented by PostgreSQL.
type Store interface {
	// FoldScopes lists scopes eligible for folding: distinct targets that
	// ever activated a release, so only scopes that can carry published
	// sys.identity_link facts are polled.
	FoldScopes(ctx context.Context) ([]Scope, error)
	// ClaimScope leases one scope for a fold round and loads its graph. The
	// boolean is false when a live lease is held elsewhere.
	ClaimScope(ctx context.Context, scope Scope, worker string, lease time.Duration) (*FoldClaim, bool, error)
	// CommitFold persists a changed fold atomically: edge upserts, person
	// allocation, retires, member moves, the completed revision and the
	// advanced watermark, then releases the lease.
	CommitFold(ctx context.Context, claim *FoldClaim, outcome FoldOutcome) error
	// ReleaseFold drops the lease without writing a revision (scan or
	// barrier failure fail-closed, or an unchanged overlap rescan).
	ReleaseFold(ctx context.Context, claim *FoldClaim) error
	// RecordFoldFailure parks a failed revision for diagnosis without moving
	// the watermark, then releases the lease.
	RecordFoldFailure(ctx context.Context, claim *FoldClaim, code string) error
	// PruneRevisions deletes revisions older than both retentionWindow and the
	// newest minCompleted completed revisions (whichever retains more), advancing
	// the scope's pruned_through_revision watermark so pinned lookups fail with
	// IDENTITY_REVISION_PRUNED.
	PruneRevisions(ctx context.Context, scope Scope, now time.Time, retentionWindow time.Duration, minCompleted int) (int64, error)
	// Read APIs (authorization enforced by the adapter).
	ListRevisions(ctx context.Context, actor, project, environment string, filter ReadFilter) (Page, error)
	GetRevision(ctx context.Context, actor, project, environment string, revision int64) (Revision, error)
	ResolveMember(ctx context.Context, actor, project, environment string, revision int64, kind, value string) (Resolution, error)
}

// MemberCatalog serves query-plan identity expansion (slice 2). It is an
// internal engine port, not an actor-facing API: permission enforcement
// happens at query submission, and expansion itself must stay bounded and
// deterministic for one revision.
type MemberCatalog interface {
	// LatestCompletedRevision returns the newest completed revision of the
	// scope, or 0 when no fold has completed yet.
	LatestCompletedRevision(ctx context.Context, scope Scope) (int64, error)
	// ExpansionMembers returns the full member set of one resolved person for
	// a query pin. Consistent with the resolve API, only the latest completed
	// revision is expandable until historical member snapshots exist; pins of
	// older revisions fail closed instead of answering from a state the
	// revision never captured.
	ExpansionMembers(ctx context.Context, scope Scope, revision int64, resolvedPersonID string) ([]Member, error)
}
