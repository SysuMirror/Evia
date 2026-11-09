package analysis

// necessaryIdentityEquality returns one equality every matching winner must
// satisfy. An AND child is necessary; an OR child is not. Selecting one
// condition also lets either identity projection cover the entire lookup.
func necessaryIdentityEquality(request Request) (key, value string) {
	if request.IdentityFilter != nil {
		if request.IdentityFilter.UID != nil {
			return "uid", *request.IdentityFilter.UID
		}
		if request.IdentityFilter.AnonymousID != nil {
			return "anonymous_id", *request.IdentityFilter.AnonymousID
		}
	}
	return filterIdentityEquality(request.Filter)
}

func filterIdentityEquality(filter *Filter) (key, value string) {
	if filter == nil {
		return "", ""
	}
	if filter.Op == "and" {
		for _, child := range filter.Args {
			if key, value := filterIdentityEquality(child); key != "" {
				return key, value
			}
		}
		return "", ""
	}
	if filter.Op != "eq" || filter.Field == nil || filter.Field.Kind != "system" {
		return "", ""
	}
	key = filter.Field.Key
	if key != "uid" && key != "anonymous_id" {
		return "", ""
	}
	value, ok := filter.Value.(string)
	if !ok {
		return "", ""
	}
	return key, value
}

// identityCandidatePredicate restricts deduplication to candidate grouping keys,
// never to candidate rows. A losing replay may satisfy the identity equality;
// all versions of that source/event pair must still participate in argMin.
// The complete winner predicate remains in Compile's outer query. Projections
// only accelerate this ordinary SELECT: absent/unmaterialized projections fall
// back to raw parts and retain the same results and execution resource limits.
func (p *planner) identityCandidatePredicate(database string, scope Scope, request Request) string {
	key, value := necessaryIdentityEquality(request)
	if key == "" {
		return ""
	}
	// Bind every lookup column to this raw scan explicitly. Winner payload
	// aliases and reconstructed scope constants in surrounding queries must not
	// capture any part of the candidate lookup's authorization or identity.
	return "(source_id,event_id) IN (SELECT candidate_facts.source_id,candidate_facts.event_id FROM `" + database + "`.`event_facts_raw` AS candidate_facts" +
		" WHERE candidate_facts.project_id=" + p.bind("String", scope.ProjectID) +
		" AND candidate_facts.environment=" + p.bind("String", scope.Environment) +
		" AND candidate_facts." + key + "=" + p.bind("String", value) + ")"
}

func (p *planner) identityFactsSource(database string, scope Scope, request Request) string {
	predicate := p.identityCandidatePredicate(database, scope, request)
	if predicate == "" {
		return "`" + database + "`.`event_facts`"
	}
	return "(SELECT * FROM `" + database + "`.`event_facts` WHERE project_id=" + p.bind("String", scope.ProjectID) +
		" AND environment=" + p.bind("String", scope.Environment) + " AND " + predicate + ")"
}
