package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Observer reports fold-round outcomes. Label values are closed sets per
// method; adapters must never place business data (uids, anonymous ids) into
// labels.
type Observer interface {
	FoldScopeListFailed()
	FoldScopeClaimed(scope Scope)
	FoldScopeSkipped(scope Scope)
	FoldScanFailed(scope Scope)
	FoldBarrierFailed(scope Scope)
	FoldUnchanged(scope Scope)
	FoldCompleted(scope Scope, edges, persons, members int64)
	FoldFailed(scope Scope, code string)
	FoldDuration(scope Scope, duration time.Duration)
}

// NoopObserver discards every report.
type NoopObserver struct{}

func (NoopObserver) FoldScopeListFailed()                     {}
func (NoopObserver) FoldScopeClaimed(Scope)                   {}
func (NoopObserver) FoldScopeSkipped(Scope)                   {}
func (NoopObserver) FoldScanFailed(Scope)                     {}
func (NoopObserver) FoldBarrierFailed(Scope)                  {}
func (NoopObserver) FoldUnchanged(Scope)                      {}
func (NoopObserver) FoldCompleted(Scope, int64, int64, int64) {}
func (NoopObserver) FoldFailed(Scope, string)                 {}
func (NoopObserver) FoldDuration(Scope, time.Duration)        {}

// Worker folds identity graphs. One worker instance claims scopes through the
// store lease, so replicas partition work without coordinating in memory.
type Worker struct {
	store      Store
	facts      LinkFacts
	tombstones Tombstones
	observer   Observer
	worker     string
	scanLimit  int
	lease      time.Duration
	interval   time.Duration
}

type WorkerOptions struct {
	Worker    string
	ScanLimit int
	Lease     time.Duration
	Interval  time.Duration
}

func NewWorker(store Store, facts LinkFacts, tombstones Tombstones, observer Observer, options WorkerOptions) *Worker {
	if options.Worker == "" {
		options.Worker = "identity-" + randomToken()
	}
	if options.ScanLimit <= 0 {
		options.ScanLimit = DefaultScanLimit
	}
	if options.Lease <= 0 {
		options.Lease = DefaultLease
	}
	if options.Interval <= 0 {
		options.Interval = DefaultPollInterval
	}
	if observer == nil {
		observer = NoopObserver{}
	}
	return &Worker{
		store:      store,
		facts:      facts,
		tombstones: tombstones,
		observer:   observer,
		worker:     options.Worker,
		scanLimit:  options.ScanLimit,
		lease:      options.Lease,
		interval:   options.Interval,
	}
}

// Run polls until the context is cancelled; each tick folds every eligible
// scope once. Errors never stop the loop, they only report through the
// observer so one unhealthy scope cannot starve the others.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce performs one fold round over every eligible scope.
func (w *Worker) RunOnce(ctx context.Context) {
	scopes, err := w.store.FoldScopes(ctx)
	if err != nil {
		w.observer.FoldScopeListFailed()
		return
	}
	for _, scope := range scopes {
		w.foldScope(ctx, scope)
	}
}

func (w *Worker) foldScope(ctx context.Context, scope Scope) {
	started := time.Now()
	claim, ok, err := w.store.ClaimScope(ctx, scope, w.worker, w.lease)
	if err != nil || !ok || claim == nil {
		w.observer.FoldScopeSkipped(scope)
		return
	}
	w.observer.FoldScopeClaimed(scope)
	defer func() {
		w.observer.FoldDuration(scope, time.Since(started))
	}()
	after := claim.Watermark.Add(-ScanOverlap)
	links, _, err := w.facts.ScanLinks(ctx, scope, after, w.scanLimit)
	if err != nil {
		// Scan failures fail closed: release the lease without writing.
		w.observer.FoldScanFailed(scope)
		w.release(claim)
		return
	}
	if len(links) == 0 {
		w.observer.FoldUnchanged(scope)
		w.release(claim)
		return
	}
	uids := make([]string, 0, len(links))
	anons := make([]string, 0, len(links))
	seenUID := make(map[string]struct{}, len(links))
	seenAnon := make(map[string]struct{}, len(links))
	for _, link := range links {
		if _, dup := seenUID[link.UID]; !dup {
			seenUID[link.UID] = struct{}{}
			uids = append(uids, link.UID)
		}
		if _, dup := seenAnon[link.AnonymousID]; !dup {
			seenAnon[link.AnonymousID] = struct{}{}
			anons = append(anons, link.AnonymousID)
		}
	}
	erased, err := w.tombstones.Erased(ctx, scope, uids, anons)
	if err != nil {
		// Barrier lookups fail closed: never fold blind behind a broken
		// tombstone check.
		w.observer.FoldBarrierFailed(scope)
		w.release(claim)
		return
	}
	candidates := make([]LinkScan, 0, len(links))
	for _, link := range links {
		if erased.Contains(MemberUID, link.UID) || erased.Contains(MemberAnonymousID, link.AnonymousID) {
			continue
		}
		candidates = append(candidates, link)
	}
	outcome := Fold(scope, claim.Graph, candidates, claim.NextRevision, time.Now(), NewPersonID)
	if !outcome.Changed {
		w.observer.FoldUnchanged(scope)
		w.release(claim)
		return
	}
	if outcome.Watermark.Before(claim.Watermark) {
		outcome.Watermark = claim.Watermark
	}
	if err := w.store.CommitFold(ctx, claim, outcome); err != nil {
		w.observer.FoldFailed(scope, "IDENTITY_FOLD_COMMIT_FAILED")
		return
	}
	w.observer.FoldCompleted(scope, outcome.EdgeCount, outcome.ActivePersonCount, outcome.MemberCount)
}

func (w *Worker) release(claim *FoldClaim) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = w.store.ReleaseFold(releaseCtx, claim)
}

// NewPersonID allocates one security-random resolved person id. Ids are
// assigned once and never reused; merges redirect instead of reallocating.
func NewPersonID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand never fails on supported platforms; a failure is not
		// recoverable by retrying with weaker entropy.
		panic("identity: secure random unavailable")
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(raw[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

func randomToken() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "worker"
	}
	return hex.EncodeToString(raw[:])
}
