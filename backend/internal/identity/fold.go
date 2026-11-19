package identity

import (
	"sort"
	"strings"
	"time"
)

// Fold computes the next graph state for one scope from its persisted state
// plus scanned link candidates (candidates must already be filtered by the
// erasure barrier). The fold is a pure function of its inputs except for
// fresh person ids, which the allocate callback supplies once and the store
// persists; replays over identical inputs and state therefore converge to
// isomorphic graphs. Merges follow the decided rule: the person whose
// component was created in the older revision survives, ties broken by
// resolved_person_id order, and absorbed persons retire with a redirect.
func Fold(scope Scope, graph *Graph, scanned []LinkScan, nextRevision int64, now time.Time, allocate func() string) FoldOutcome {
	outcome := FoldOutcome{NextRevision: nextRevision}
	type pairKey struct{ uid, anon string }
	type pairInfo struct {
		edge      Edge
		exists    bool
		advanced  bool
		candidate LinkScan
	}
	edges := make(map[pairKey]*pairInfo, len(graph.Edges))
	for _, edge := range graph.Edges {
		edges[pairKey{edge.UID, edge.AnonymousID}] = &pairInfo{edge: edge, exists: true}
	}
	// Union-find over raw identity nodes; keys are kind-prefixed values.
	parent := make(map[string]string)
	var find func(string) string
	find = func(node string) string {
		for {
			if up, ok := parent[node]; !ok || up == node {
				parent[node] = node
				return node
			}
			parent[node] = parent[parent[node]]
			node = parent[node]
		}
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if ra > rb {
			ra, rb = rb, ra
		}
		parent[rb] = ra
	}
	nodeSeen := make(map[string]time.Time)
	link := func(uid, anon string, first time.Time) {
		uidNode, anonNode := "uid|"+uid, "anon|"+anon
		union(uidNode, anonNode)
		if prior, ok := nodeSeen[uidNode]; !ok || first.Before(prior) {
			nodeSeen[uidNode] = first
		}
		if prior, ok := nodeSeen[anonNode]; !ok || first.Before(prior) {
			nodeSeen[anonNode] = first
		}
	}
	for _, edge := range graph.Edges {
		link(edge.UID, edge.AnonymousID, edge.FirstSeenAt)
	}
	// Candidate pairs that add or advance an edge drive the whole round;
	// pure overlap rescans change nothing.
	for _, scanned := range scanned {
		// The metadata store keeps microsecond timestamps; warehouse scans
		// carry nanoseconds. Normalize at the domain boundary so an identical
		// overlap rescan never looks like an advance below storage precision.
		candidate := LinkScan{
			UID:         scanned.UID,
			AnonymousID: scanned.AnonymousID,
			FirstSeenAt: scanned.FirstSeenAt.Truncate(time.Microsecond),
			LastSeenAt:  scanned.LastSeenAt.Truncate(time.Microsecond),
		}
		if candidate.UID == "" || candidate.AnonymousID == "" {
			continue
		}
		if candidate.LastSeenAt.After(outcome.MaxSeen) {
			outcome.MaxSeen = candidate.LastSeenAt
		}
		key := pairKey{candidate.UID, candidate.AnonymousID}
		info, ok := edges[key]
		if !ok {
			info = &pairInfo{candidate: candidate}
			edges[key] = info
		} else if candidate.LastSeenAt.After(info.edge.LastSeenAt) {
			info.candidate = candidate
		} else {
			continue
		}
		info.advanced = true
		first, firstRevision := candidate.FirstSeenAt, nextRevision
		if info.exists {
			first = info.edge.FirstSeenAt
			firstRevision = info.edge.FirstRevision
		}
		info.edge = Edge{
			UID:           key.uid,
			AnonymousID:   key.anon,
			FirstSeenAt:   first,
			LastSeenAt:    candidate.LastSeenAt,
			FirstRevision: firstRevision,
			LastRevision:  nextRevision,
		}
		link(key.uid, key.anon, first)
	}
	// Group nodes by component root; deterministic orders keep the output
	// stable for identical inputs.
	roots := make(map[string][]string)
	for node := range parent {
		root := find(node)
		roots[root] = append(roots[root], node)
	}
	rootOrder := make([]string, 0, len(roots))
	for root := range roots {
		rootOrder = append(rootOrder, root)
	}
	sort.Strings(rootOrder)
	for _, nodes := range roots {
		sort.Strings(nodes)
	}

	persons := make(map[string]Person, len(graph.Persons))
	for _, person := range graph.Persons {
		persons[person.ResolvedPersonID] = person
	}
	owner := make(map[string]Member) // identity node -> current membership
	for _, member := range graph.Members {
		if person, ok := persons[member.ResolvedPersonID]; ok && person.State == PersonActive {
			owner[nodeKey(member.Kind, member.Value)] = member
		}
	}
	for _, root := range rootOrder {
		var owners []string
		seen := make(map[string]struct{})
		for _, node := range roots[root] {
			if member, ok := owner[node]; ok {
				if _, dup := seen[member.ResolvedPersonID]; dup {
					continue
				}
				seen[member.ResolvedPersonID] = struct{}{}
				owners = append(owners, member.ResolvedPersonID)
			}
		}
		sort.Slice(owners, func(i, j int) bool {
			a, b := persons[owners[i]], persons[owners[j]]
			if a.CreateRevision != b.CreateRevision {
				return a.CreateRevision < b.CreateRevision
			}
			return a.ResolvedPersonID < b.ResolvedPersonID
		})
		var survivor string
		if len(owners) > 0 {
			survivor = owners[0]
			for _, absorbed := range owners[1:] {
				redirect := survivor
				retired := persons[absorbed]
				retired.State = PersonRetired
				retired.SupersededBy = &redirect
				persons[absorbed] = retired
				outcome.Retire = append(outcome.Retire, retired)
			}
		} else {
			survivor = allocate()
			persons[survivor] = Person{
				ResolvedPersonID: survivor,
				State:            PersonActive,
				CreatedAt:        now,
				CreateRevision:   nextRevision,
			}
			outcome.NewPersons = append(outcome.NewPersons, persons[survivor])
		}
		for _, node := range roots[root] {
			member, ok := owner[node]
			if !ok {
				kind, value := splitNode(node)
				fresh := Member{
					ResolvedPersonID: survivor,
					Kind:             kind,
					Value:            value,
					FirstSeenAt:      nodeSeen[node],
					JoinedRevision:   nextRevision,
				}
				owner[node] = fresh
				outcome.NewMembers = append(outcome.NewMembers, fresh)
				continue
			}
			if member.ResolvedPersonID == survivor {
				continue
			}
			member.ResolvedPersonID = survivor
			owner[node] = member
			outcome.MovedMembers = append(outcome.MovedMembers, member)
		}
	}
	for _, info := range edges {
		if info.advanced {
			outcome.EdgeUpserts = append(outcome.EdgeUpserts, info.edge)
		}
		outcome.EdgeCount++
	}
	sort.Slice(outcome.EdgeUpserts, func(i, j int) bool {
		if outcome.EdgeUpserts[i].UID != outcome.EdgeUpserts[j].UID {
			return outcome.EdgeUpserts[i].UID < outcome.EdgeUpserts[j].UID
		}
		return outcome.EdgeUpserts[i].AnonymousID < outcome.EdgeUpserts[j].AnonymousID
	})
	for _, person := range persons {
		if person.State == PersonActive {
			outcome.ActivePersonCount++
		}
	}
	outcome.MemberCount = int64(len(owner))
	outcome.Changed = len(outcome.EdgeUpserts) > 0 ||
		len(outcome.NewPersons) > 0 ||
		len(outcome.Retire) > 0 ||
		len(outcome.NewMembers) > 0 ||
		len(outcome.MovedMembers) > 0
	if !outcome.MaxSeen.IsZero() {
		outcome.Watermark = outcome.MaxSeen
	}
	return outcome
}

func nodeKey(kind, value string) string {
	if kind == MemberUID {
		return "uid|" + value
	}
	return "anon|" + value
}

func splitNode(node string) (string, string) {
	if rest, ok := strings.CutPrefix(node, "uid|"); ok {
		return MemberUID, rest
	}
	rest, _ := strings.CutPrefix(node, "anon|")
	return MemberAnonymousID, rest
}
