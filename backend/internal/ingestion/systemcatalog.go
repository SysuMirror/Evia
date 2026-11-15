package ingestion

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// systemSchemaURI mirrors the per-rule resource URI used by collector.loadPlan;
// each rule gets a fresh compiler so reusing the URI never aliases schemas.
const systemSchemaURI = "https://schemas.evia.invalid/ingestion/system-properties"

// systemKeyPattern is the reserved-key half of the envelope contract pattern
// `^(sys\.)?[a-z][a-z0-9_]{0,63}$`: a system key always carries the prefix.
var systemKeyPattern = regexp.MustCompile(`^sys\.[a-z][a-z0-9_]{0,63}$`)

// IsSystemKey reports whether key belongs to the reserved sys.* namespace.
// Custom event keys can never match, so the collector can branch on this without
// consulting the project catalog.
func IsSystemKey(key string) bool {
	return systemKeyPattern.MatchString(key)
}

type compiledSystemRule struct {
	rule   EventRule
	schema *jsonschema.Schema
}

// SystemCatalog is the immutable, platform-built catalog of reserved sys.*
// event rules. Unlike project plans it has no project, environment, release
// revision, activation lease or readiness horizon: the binary build is the
// release, and every rule is therefore always eligible while the process runs.
type SystemCatalog struct {
	rules map[string]compiledSystemRule
}

// LoadSystemCatalog compiles the embedded generated fixture once. A malformed or
// empty embedded catalog is a build/deployment fault, so construction fails
// closed instead of silently collecting without any system rule.
func LoadSystemCatalog() (*SystemCatalog, error) {
	return loadSystemCatalog(wire.SystemEvents)
}

func loadSystemCatalog(raw []byte) (*SystemCatalog, error) {
	var fixture struct {
		Format string `json:"format"`
		Events []struct {
			Key     string         `json:"event_key"`
			Version int64          `json:"schema_version"`
			Kinds   []string       `json:"allowed_source_kinds"`
			Schema  map[string]any `json:"schema"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return nil, fmt.Errorf("system catalog fixture is not valid JSON: %w", err)
	}
	if fixture.Format != "activity.system-events.v1" {
		return nil, fmt.Errorf("system catalog fixture has unexpected format %q", fixture.Format)
	}
	if len(fixture.Events) == 0 {
		return nil, fmt.Errorf("system catalog fixture contains no events")
	}
	catalog := &SystemCatalog{rules: make(map[string]compiledSystemRule, len(fixture.Events))}
	for _, event := range fixture.Events {
		if !IsSystemKey(event.Key) {
			return nil, fmt.Errorf("system catalog fixture contains non-reserved key %q", event.Key)
		}
		if _, exists := catalog.rules[event.Key]; exists {
			return nil, fmt.Errorf("system catalog fixture contains duplicate key %q", event.Key)
		}
		if event.Version < 1 {
			return nil, fmt.Errorf("system event %q must publish schema_version >= 1", event.Key)
		}
		if len(event.Kinds) == 0 {
			return nil, fmt.Errorf("system event %q must declare allowed source kinds", event.Key)
		}
		if event.Schema == nil {
			return nil, fmt.Errorf("system event %q is missing its property schema", event.Key)
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(systemSchemaURI, event.Schema); err != nil {
			return nil, fmt.Errorf("system event %q schema cannot be registered: %w", event.Key, err)
		}
		schema, err := compiler.Compile(systemSchemaURI)
		if err != nil {
			return nil, fmt.Errorf("system event %q schema cannot be compiled: %w", event.Key, err)
		}
		catalog.rules[event.Key] = compiledSystemRule{
			rule: EventRule{
				Key:                event.Key,
				Version:            event.Version,
				AllowedSourceKinds: append([]string(nil), event.Kinds...),
				Schema:             event.Schema,
			},
			schema: schema,
		}
	}
	return catalog, nil
}

// Rule returns the reserved rule and its compiled property schema. An unknown
// key fails closed with ok=false; callers must reject rather than fall through
// to project plan lookup, which can never contain a sys.* key.
func (c *SystemCatalog) Rule(key string) (EventRule, *jsonschema.Schema, bool) {
	compiled, ok := c.rules[key]
	if !ok {
		return EventRule{}, nil, false
	}
	return compiled.rule, compiled.schema, true
}

// Keys lists every reserved event key in lexical order, for diagnostics and
// exhaustive contract tests.
func (c *SystemCatalog) Keys() []string {
	keys := make([]string, 0, len(c.rules))
	for key := range c.rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
