package catalog

// Compatibility is conservative: compatible means every previously valid value
// remains accepted, and source/fact semantics retain their prior meaning.
func Compatibility(previous *DraftInput, next DraftInput) string {
	if previous == nil {
		return "initial"
	}
	// Until semantic metadata has a typed contract, any metadata change may
	// alter interpretation (for example duration milliseconds versus seconds).
	// Compare canonical JSON, so map iteration/key order has no effect. Missing
	// and explicitly empty metadata retain their existing distinct semantics.
	beforeMetadata, beforeError := Hash(previous.FieldMetadata)
	afterMetadata, afterError := Hash(next.FieldMetadata)
	if beforeError != nil || afterError != nil || beforeMetadata != afterMetadata {
		return "breaking"
	}
	for _, kind := range previous.AllowedSourceKinds {
		found := false
		for _, v := range next.AllowedSourceKinds {
			found = found || v == kind
		}
		if !found {
			return "breaking"
		}
	}
	if text(previous.FactKind) != text(next.FactKind) {
		return "breaking"
	}
	oldProperties, _ := object(previous.Schema["properties"])
	newProperties, _ := object(next.Schema["properties"])
	oldRequired := map[string]bool{}
	a, _ := array(previous.Schema["required"])
	for _, v := range a {
		k, _ := v.(string)
		oldRequired[k] = true
	}
	a, _ = array(next.Schema["required"])
	for _, v := range a {
		k, _ := v.(string)
		if !oldRequired[k] {
			return "breaking"
		}
	}
	if bound(next.Schema, "maxProperties", 50) < bound(previous.Schema, "maxProperties", 50) {
		return "breaking"
	}
	for k, v := range oldProperties {
		before, _ := object(v)
		after, ok := object(newProperties[k])
		if !ok || !propertyCompatible(before, after) {
			return "breaking"
		}
	}
	return "compatible"
}
func propertyCompatible(a, b map[string]any) bool {
	if a["type"] != b["type"] && !(a["type"] == "integer" && b["type"] == "number") {
		return false
	}
	for _, pair := range [][2]string{{"minLength", "maxLength"}, {"minItems", "maxItems"}, {"minimum", "maximum"}} {
		min, max := float64(0), float64(1024)
		if pair[0] == "minItems" {
			max = 20
		}
		if pair[0] == "minimum" {
			min = -float64(MaxInteger)
			max = float64(MaxInteger)
		}
		if bound(b, pair[0], min) > bound(a, pair[0], min) || bound(b, pair[1], max) < bound(a, pair[1], max) {
			return false
		}
	}
	if values, ok := array(b["enum"]); ok {
		old, had := array(a["enum"])
		if !had {
			return false
		}
		for _, v := range old {
			h, _ := Hash(v)
			found := false
			for _, n := range values {
				nh, _ := Hash(n)
				found = found || h == nh
			}
			if !found {
				return false
			}
		}
	}
	if a["type"] == "array" {
		ai, _ := object(a["items"])
		bi, _ := object(b["items"])
		return propertyCompatible(ai, bi)
	}
	return true
}
