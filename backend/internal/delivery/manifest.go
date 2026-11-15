package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type manifest struct {
	Format      string          `json:"format"`
	Status      string          `json:"status"`
	ProjectID   string          `json:"project_key"`
	Environment string          `json:"environment"`
	Revision    int64           `json:"release_revision"`
	Events      []manifestEvent `json:"events"`
}
type manifestEvent struct {
	FieldMetadata map[string]any `json:"field_metadata,omitzero"`
	Key           string         `json:"event_key"`
	Version       int64          `json:"schema_version"`
	Kinds         []string       `json:"allowed_source_kinds"`
	FactKind      string         `json:"fact_kind"`
	Schema        map[string]any `json:"schema"`
}

type manifestCache struct {
	plan         manifest
	bytes, rules int
}
type manifestFlight struct {
	done chan struct{}
	plan manifest
	err  error
}

func (s *Service) checkManifest(ctx context.Context, candidate Candidate) (manifest, error) {
	if !uuid.MatchString(candidate.ReleaseID) || !uuid.MatchString(candidate.ProjectID) || !validEnvironment(candidate.Environment) || candidate.Revision < 1 || candidate.Revision > MaxRevision || !digestPattern.MatchString(candidate.ManifestHash) {
		return manifest{}, Unavailable("RELEASE_MANIFEST_INVALID")
	}
	s.mu.Lock()
	if cached, ok := s.cache[candidate.ManifestHash]; ok {
		s.mu.Unlock()
		if cached.plan.ProjectID != candidate.ProjectID || cached.plan.Environment != candidate.Environment || cached.plan.Revision != candidate.Revision {
			return manifest{}, Unavailable("RELEASE_SCOPE_MISMATCH")
		}
		return cached.plan, nil
	}
	if flight, ok := s.flights[candidate.ManifestHash]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return manifest{}, Unavailable("RELEASE_VALIDATION_TIMEOUT")
		case <-flight.done:
			if flight.err == nil && (flight.plan.ProjectID != candidate.ProjectID || flight.plan.Environment != candidate.Environment || flight.plan.Revision != candidate.Revision) {
				return manifest{}, Unavailable("RELEASE_SCOPE_MISMATCH")
			}
			return flight.plan, flight.err
		}
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.Unlock()
		return manifest{}, Unavailable("RELEASE_VALIDATION_BUSY")
	}
	flight := &manifestFlight{done: make(chan struct{})}
	s.flights[candidate.ManifestHash] = flight
	s.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	plan, e := s.loadManifest(bounded, candidate)
	s.mu.Lock()
	flight.plan, flight.err = plan, e
	rules := len(plan.Events)
	for _, event := range plan.Events {
		properties, _ := event.Schema["properties"].(map[string]any)
		rules += len(properties)
	}
	size := len(candidate.Manifest)
	if e == nil && size <= 8*1024*1024 && rules <= 4096 {
		for len(s.cache) >= 64 || s.cacheBytes+size > 8*1024*1024 || s.cacheRules+rules > 4096 {
			for key, entry := range s.cache {
				delete(s.cache, key)
				s.cacheBytes -= entry.bytes
				s.cacheRules -= entry.rules
				break
			}
		}
		s.cache[candidate.ManifestHash] = manifestCache{plan: plan, bytes: size, rules: rules}
		s.cacheBytes += size
		s.cacheRules += rules
	}
	delete(s.flights, candidate.ManifestHash)
	close(flight.done)
	s.mu.Unlock()
	<-s.slots
	return plan, e
}
func (s *Service) loadManifest(ctx context.Context, candidate Candidate) (manifest, error) {
	if len(candidate.Manifest) > contract.MaxReleaseBytes {
		return manifest{}, Unavailable("RELEASE_MANIFEST_INVALID")
	}
	value, e := contract.Decode(candidate.Manifest)
	if e != nil || s.validator.Validate("EventPlan", value) != nil {
		return manifest{}, Unavailable("RELEASE_MANIFEST_INVALID")
	}
	hash, e := canonical.Hash(value)
	if e != nil || hash != candidate.ManifestHash {
		return manifest{}, Unavailable("RELEASE_MANIFEST_HASH_MISMATCH")
	}
	var plan manifest
	if e = json.Unmarshal(candidate.Manifest, &plan); e != nil || plan.Format != "activity.event-plan.v1" || plan.Status != "published" || plan.ProjectID != candidate.ProjectID || plan.Environment != candidate.Environment || plan.Revision != candidate.Revision || len(plan.Events) < 1 || len(plan.Events) > ProjectReleaseEventLimit {
		return manifest{}, Unavailable("RELEASE_MANIFEST_INVALID")
	}
	seen := map[string]bool{}
	for _, event := range plan.Events {
		if ctx.Err() != nil {
			return manifest{}, Unavailable("RELEASE_VALIDATION_TIMEOUT")
		}
		if seen[event.Key] || !localSchema(event.Schema, 0) {
			return manifest{}, Unavailable("RELEASE_SCHEMA_INVALID")
		}
		seen[event.Key] = true
		compiler := jsonschema.NewCompiler()
		const uri = "https://schemas.evia.invalid/delivery/event"
		if e = compiler.AddResource(uri, event.Schema); e != nil {
			return manifest{}, Unavailable("RELEASE_SCHEMA_INVALID")
		}
		if _, e = compiler.Compile(uri); e != nil {
			return manifest{}, Unavailable("RELEASE_SCHEMA_INVALID")
		}
	}

	if ctx.Err() != nil {
		return manifest{}, Unavailable("RELEASE_VALIDATION_TIMEOUT")
	}
	return plan, nil
}

// Only the published local schema vocabulary is accepted. In particular no
// remote reference can trigger network I/O while a release is being activated.
func localSchema(schema map[string]any, depth int) bool {
	if depth > 3 || len(schema) > 16 {
		return false
	}
	allowed := map[string]bool{"$schema": true, "type": true, "properties": true, "required": true, "additionalProperties": true, "maxProperties": true, "title": true, "description": true, "minLength": true, "maxLength": true, "enum": true, "minimum": true, "maximum": true, "items": true, "minItems": true, "maxItems": true, "default": true, "examples": true, "$comment": true}
	for key, value := range schema {
		if !allowed[key] {
			return false
		}
		switch key {
		case "$schema":
			if value != "https://json-schema.org/draft/2020-12/schema" {
				return false
			}
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok || len(properties) > 50 {
				return false
			}
			for _, property := range properties {
				child, ok := property.(map[string]any)
				if !ok || !localSchema(child, depth+1) {
					return false
				}
			}
		case "items":
			child, ok := value.(map[string]any)
			if !ok || !localSchema(child, depth+1) {
				return false
			}
		}
	}
	return true
}
func publicSchema(schema map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range schema {
		switch key {
		case "title", "description", "default", "examples", "$comment":
			continue
		case "properties":
			children, _ := value.(map[string]any)
			clean := map[string]any{}
			for name, child := range children {
				object, _ := child.(map[string]any)
				clean[name] = publicSchema(object)
			}
			out[key] = clean
		case "items":
			object, _ := value.(map[string]any)
			out[key] = publicSchema(object)
		default:
			out[key] = value
		}
	}
	return out
}
func canUpgrade(prior, next manifest, kind string) bool {
	byKey := map[string]manifestEvent{}
	for _, event := range next.Events {
		if allows(event.Kinds, kind) {
			byKey[event.Key] = event
		}
	}
	for _, event := range prior.Events {
		if !allows(event.Kinds, kind) {
			continue
		}
		after, ok := byKey[event.Key]
		beforeMetadata, _ := canonical.Hash(event.FieldMetadata)
		afterMetadata, _ := canonical.Hash(after.FieldMetadata)
		if !ok || beforeMetadata != afterMetadata || event.FactKind != after.FactKind || !schemaCompatible(publicSchema(event.Schema), publicSchema(after.Schema)) {
			return false
		}
	}
	return true
}
func schemaCompatible(a, b map[string]any) bool {
	if a["type"] != b["type"] && !(a["type"] == "integer" && b["type"] == "number") {
		return false
	}
	for _, pair := range [][2]string{{"minLength", "maxLength"}, {"minItems", "maxItems"}, {"minimum", "maximum"}} {
		min, max := float64(0), float64(1024)
		if pair[0] == "minItems" {
			max = 20
		}
		if pair[0] == "minimum" {
			min = -float64(MaxRevision)
			max = float64(MaxRevision)
		}
		if bound(b, pair[0], min) > bound(a, pair[0], min) || bound(b, pair[1], max) < bound(a, pair[1], max) {
			return false
		}
	}
	if after, ok := b["enum"].([]any); ok {
		before, ok := a["enum"].([]any)
		if !ok {
			return false
		}
		for _, value := range before {
			found := false
			for _, next := range after {
				h, _ := canonical.Hash(value)
				n, _ := canonical.Hash(next)
				found = found || h == n
			}
			if !found {
				return false
			}
		}
	}
	if a["type"] == "object" {
		oldRequired := map[string]bool{}
		if values, ok := a["required"].([]any); ok {
			for _, v := range values {
				k, _ := v.(string)
				oldRequired[k] = true
			}
		}
		if values, ok := b["required"].([]any); ok {
			for _, v := range values {
				k, _ := v.(string)
				if !oldRequired[k] {
					return false
				}
			}
		}
		if bound(b, "maxProperties", 50) < bound(a, "maxProperties", 50) {
			return false
		}
		before, _ := a["properties"].(map[string]any)
		after, _ := b["properties"].(map[string]any)
		for key, value := range before {
			left, _ := value.(map[string]any)
			right, ok := after[key].(map[string]any)
			if !ok || !schemaCompatible(left, right) {
				return false
			}
		}
	}
	if a["type"] == "array" {
		left, _ := a["items"].(map[string]any)
		right, _ := b["items"].(map[string]any)
		return schemaCompatible(left, right)
	}
	// Unknown constraints are rejected during preparation rather than assumed safe.
	return true
}
func bound(schema map[string]any, key string, fallback float64) float64 {
	switch v := schema[key].(type) {
	case json.Number:
		n, e := v.Float64()
		if e == nil {
			return n
		}
	case float64:
		return v
	case int:
		return float64(v)
	}
	return fallback
}
