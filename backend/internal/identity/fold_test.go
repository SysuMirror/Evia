package identity

import (
	"testing"
	"time"
)

var base = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

func allocateSeq(counter *int) func() string {
	return func() string {
		*counter++
		return string(rune('A'+*counter-1)) + "-person"
	}
}

// apply simulates the store commit so successive folds see persisted state.
func apply(graph *Graph, outcome FoldOutcome) {
	if outcome.Changed {
		graph.Edges = append(graph.Edges, outcome.EdgeUpserts...)
		graph.Persons = append(graph.Persons, outcome.NewPersons...)
		for _, retired := range outcome.Retire {
			for i := range graph.Persons {
				if graph.Persons[i].ResolvedPersonID == retired.ResolvedPersonID {
					graph.Persons[i] = retired
				}
			}
		}
		moved := make(map[string]Member)
		for _, member := range outcome.MovedMembers {
			moved[nodeKey(member.Kind, member.Value)] = member
		}
		var members []Member
		for _, member := range graph.Members {
			if replacement, ok := moved[nodeKey(member.Kind, member.Value)]; ok {
				members = append(members, replacement)
				continue
			}
			members = append(members, member)
		}
		graph.Members = append(members, outcome.NewMembers...)
	}
}

func TestFoldFirstRoundCreatesOnePersonPerComponent(t *testing.T) {
	graph := &Graph{}
	scanned := []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base.Add(time.Minute)},
		{UID: "u1", AnonymousID: "a2", FirstSeenAt: base, LastSeenAt: base.Add(2 * time.Minute)},
		{UID: "u9", AnonymousID: "a9", FirstSeenAt: base, LastSeenAt: base.Add(3 * time.Minute)},
	}
	counter := 0
	outcome := Fold(Scope{ProjectID: "p", Environment: "development"}, graph, scanned, 1, base, allocateSeq(&counter))
	if !outcome.Changed {
		t.Fatal("first round must change the graph")
	}
	if outcome.EdgeCount != 3 || len(outcome.EdgeUpserts) != 3 {
		t.Fatalf("edge bookkeeping wrong: count=%d upserts=%d", outcome.EdgeCount, len(outcome.EdgeUpserts))
	}
	if len(outcome.NewPersons) != 2 {
		t.Fatalf("two components must create two persons, got %d", len(outcome.NewPersons))
	}
	if outcome.MemberCount != 5 {
		t.Fatalf("u1,a1,a2,u9,a9 are five identities, got members=%d", outcome.MemberCount)
	}
	if outcome.ActivePersonCount != 2 {
		t.Fatalf("active persons=%d", outcome.ActivePersonCount)
	}
	if !outcome.Watermark.Equal(base.Add(3 * time.Minute)) {
		t.Fatalf("watermark must be the max seen, got %v", outcome.Watermark)
	}
	for _, edge := range outcome.EdgeUpserts {
		if edge.FirstRevision != 1 || edge.LastRevision != 1 {
			t.Fatalf("first round revisions wrong: %+v", edge)
		}
	}
}

func TestFoldMergeRetiresYoungerComponentAndMovesMembers(t *testing.T) {
	graph := &Graph{}
	counter := 0
	first := Fold(Scope{}, graph, []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
	}, 1, base, allocateSeq(&counter))
	apply(graph, first)
	second := Fold(Scope{}, graph, []LinkScan{
		{UID: "u2", AnonymousID: "a2", FirstSeenAt: base.Add(time.Minute), LastSeenAt: base.Add(time.Minute)},
	}, 2, base.Add(time.Minute), allocateSeq(&counter))
	apply(graph, second)
	personOf := func(kind, value string) Member {
		for _, member := range graph.Members {
			if member.Kind == kind && member.Value == value {
				return member
			}
		}
		t.Fatalf("member %s/%s missing", kind, value)
		return Member{}
	}
	firstPerson := personOf(MemberUID, "u1").ResolvedPersonID
	secondPerson := personOf(MemberUID, "u2").ResolvedPersonID
	if firstPerson == secondPerson {
		t.Fatal("disjoint components must not share a person")
	}
	// New edge a1..u2 joins both components; the older person survives.
	bridge := Fold(Scope{}, graph, []LinkScan{
		{UID: "u2", AnonymousID: "a1", FirstSeenAt: base.Add(2 * time.Minute), LastSeenAt: base.Add(2 * time.Minute)},
	}, 3, base.Add(2*time.Minute), allocateSeq(&counter))
	if len(bridge.Retire) != 1 {
		t.Fatalf("merge must retire exactly one person, got %d", len(bridge.Retire))
	}
	retired := bridge.Retire[0]
	if retired.ResolvedPersonID != secondPerson {
		t.Fatalf("younger person %s must retire, got %s", secondPerson, retired.ResolvedPersonID)
	}
	if retired.SupersededBy == nil || *retired.SupersededBy != firstPerson {
		t.Fatal("retired person must redirect to the survivor")
	}
	if len(bridge.MovedMembers) != 2 {
		t.Fatalf("u2 and a2 must move to the survivor, got %d moves", len(bridge.MovedMembers))
	}
	for _, moved := range bridge.MovedMembers {
		if moved.ResolvedPersonID != firstPerson {
			t.Fatalf("member %s must point at the survivor", moved.Value)
		}
		if moved.JoinedRevision != 2 || moved.FirstSeenAt.IsZero() {
			t.Fatalf("moves must keep provenance: %+v", moved)
		}
	}
	if len(bridge.NewMembers) != 0 {
		t.Fatalf("no new identities exist in a pure merge, got %d new members", len(bridge.NewMembers))
	}
	if bridge.ActivePersonCount != 1 || bridge.MemberCount != 4 {
		t.Fatalf("post-merge counts wrong: persons=%d members=%d", bridge.ActivePersonCount, bridge.MemberCount)
	}
}

func TestFoldEqualCreateRevisionsBreakTiesByID(t *testing.T) {
	graph := &Graph{
		Persons: []Person{
			{ResolvedPersonID: "b", State: PersonActive, CreateRevision: 1},
			{ResolvedPersonID: "a", State: PersonActive, CreateRevision: 1},
		},
		Members: []Member{
			{ResolvedPersonID: "b", Kind: MemberUID, Value: "u1", JoinedRevision: 1},
			{ResolvedPersonID: "a", Kind: MemberUID, Value: "u2", JoinedRevision: 1},
		},
		Edges: []Edge{
			{UID: "u1", AnonymousID: "x", FirstRevision: 1, LastRevision: 1, FirstSeenAt: base, LastSeenAt: base},
			{UID: "u2", AnonymousID: "y", FirstRevision: 1, LastRevision: 1, FirstSeenAt: base, LastSeenAt: base},
		},
	}
	counter := 0
	outcome := Fold(Scope{}, graph, []LinkScan{
		{UID: "u2", AnonymousID: "x", FirstSeenAt: base.Add(time.Minute), LastSeenAt: base.Add(time.Minute)},
	}, 2, base.Add(time.Minute), allocateSeq(&counter))
	if len(outcome.Retire) != 1 || outcome.Retire[0].ResolvedPersonID != "b" {
		t.Fatalf("lexicographically smaller id must survive: %+v", outcome.Retire)
	}
}

func TestFoldOverlapRescanChangesNothing(t *testing.T) {
	graph := &Graph{}
	counter := 0
	scanned := []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
	}
	first := Fold(Scope{}, graph, scanned, 1, base, allocateSeq(&counter))
	apply(graph, first)
	again := Fold(Scope{}, graph, scanned, 2, base.Add(time.Hour), allocateSeq(&counter))
	if again.Changed {
		t.Fatal("rescanning already folded pairs must not change the graph")
	}
	if again.EdgeCount != 1 || again.ActivePersonCount != 1 || again.MemberCount != 2 {
		t.Fatalf("counts drifted on rescan: %+v", again)
	}
	if !again.MaxSeen.Equal(base) {
		t.Fatalf("max seen must still be reported: %v", again.MaxSeen)
	}
}

func TestFoldAdvancesEdgeKeepingFirstSeen(t *testing.T) {
	graph := &Graph{}
	counter := 0
	first := Fold(Scope{}, graph, []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
	}, 1, base, allocateSeq(&counter))
	apply(graph, first)
	later := base.Add(2 * time.Hour)
	second := Fold(Scope{}, graph, []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base.Add(time.Hour), LastSeenAt: later},
	}, 2, later, allocateSeq(&counter))
	if len(second.EdgeUpserts) != 1 {
		t.Fatalf("one advanced edge expected, got %d", len(second.EdgeUpserts))
	}
	edge := second.EdgeUpserts[0]
	if edge.FirstSeenAt.Equal(base.Add(time.Hour)) {
		t.Fatal("first_seen must keep the original observation")
	}
	if !edge.LastSeenAt.Equal(later) || edge.LastRevision != 2 || edge.FirstRevision != 1 {
		t.Fatalf("edge advance wrong: %+v", edge)
	}
	if len(second.NewPersons) != 0 || len(second.NewMembers) != 0 {
		t.Fatal("no structural change expected on a repeated pair")
	}
	if !second.Watermark.Equal(later) {
		t.Fatalf("watermark must advance to the later observation: %v", second.Watermark)
	}
}

func TestFoldEmptyScanIsANoOp(t *testing.T) {
	counter := 0
	outcome := Fold(Scope{}, &Graph{}, nil, 1, base, allocateSeq(&counter))
	if outcome.Changed || !outcome.MaxSeen.IsZero() || outcome.Watermark.IsZero() == false {
		t.Fatalf("empty scan must be a no-op: %+v", outcome)
	}
}

func TestFoldSkipsIncompleteCandidates(t *testing.T) {
	counter := 0
	outcome := Fold(Scope{}, &Graph{}, []LinkScan{
		{UID: "", AnonymousID: "a1", LastSeenAt: base},
		{UID: "u1", AnonymousID: "", LastSeenAt: base},
	}, 1, base, allocateSeq(&counter))
	if outcome.Changed {
		t.Fatal("incomplete pairs must be skipped, not folded")
	}
}
