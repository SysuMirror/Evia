package analysis

// RequiresRawAccess identifies requests that expose or select individual
// identities. Aggregate subjects alone (UV and funnel counts) do not expose them.
func RequiresRawAccess(request Request) bool {
	if request.Kind == QueryKindEvents || request.Kind == QueryKindJourney || request.IdentityFilter != nil {
		return true
	}
	raw := false
	_ = WalkFields(&request, func(field *Field) error {
		if field.Kind == "system" {
			switch field.Key {
			case "uid", "anonymous_id", "session_id", "page_view_id", "operation_id":
				raw = true
			}
		}
		return nil
	})
	return raw
}
