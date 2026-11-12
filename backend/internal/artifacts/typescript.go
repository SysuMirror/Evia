// Package artifacts turns immutable release manifests into deterministic
// source artifacts for client integration. The generator only accepts the
// bounded JSON Schema subset enforced at publication (flat objects, scalar or
// homogeneous scalar-array properties, no references or combinators), so the
// emitted TypeScript can never silently diverge from the runtime contract.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// LanguageVersion is the emitted TypeScript compatibility baseline.
const LanguageVersion = "5.0"

// EventSchema is one published event definition within a release manifest.
type EventSchema struct {
	Key                string
	SchemaVersion      int64
	DisplayName        string
	Schema             map[string]any
	AllowedSourceKinds []string
	FactKind           string
}

// ReleasePlan is the immutable input the generator renders.
type ReleasePlan struct {
	ReleaseID    string
	Revision     int64
	Environment  string
	ManifestHash string
	Events       []EventSchema
}

// EventCompatibility reports the tool-facing compatibility of one event.
type EventCompatibility struct {
	EventKey              string   `json:"event_key"`
	SchemaVersion         int64    `json:"schema_version"`
	InterfaceName         string   `json:"interface_name"`
	ValidIdentifier       bool     `json:"valid_identifier"`
	UnsupportedConstructs []string `json:"unsupported_constructs"`
}

// Artifact is the generated TypeScript declaration plus its integrity metadata.
type Artifact struct {
	Source        string
	Checksum      string
	ByteSize      int
	EventCount    int
	Compatibility []EventCompatibility
}

const maxProperties = 50

// Generate renders a deterministic TypeScript declaration for one release.
// The output depends only on the manifest content: no timestamps or host data.
func Generate(plan ReleasePlan) (Artifact, error) {
	if len(plan.Events) == 0 {
		return Artifact{}, fault.New(fault.Invalid, "ARTIFACT_EMPTY_RELEASE", "发布清单不包含任何事件，无法生成类型产物")
	}
	var b strings.Builder
	b.WriteString("// Code generated from an immutable Evia release manifest; DO NOT EDIT.\n")
	b.WriteString(fmt.Sprintf("// release_id: %s\n", plan.ReleaseID))
	b.WriteString(fmt.Sprintf("// environment: %s\n", plan.Environment))
	b.WriteString(fmt.Sprintf("// release_revision: %d\n", plan.Revision))
	b.WriteString(fmt.Sprintf("// manifest_hash: %s\n", plan.ManifestHash))
	b.WriteString(fmt.Sprintf("// language: typescript >= %s\n", LanguageVersion))
	b.WriteString("\n")

	names := map[string]string{}
	compatibility := make([]EventCompatibility, 0, len(plan.Events))
	keys := make([]string, 0, len(plan.Events))
	eventsByKey := map[string]EventSchema{}
	for _, event := range plan.Events {
		if _, dup := eventsByKey[event.Key]; dup {
			return Artifact{}, fault.New(fault.Invalid, "ARTIFACT_DUPLICATE_EVENT", "发布清单包含重复事件: "+event.Key)
		}
		eventsByKey[event.Key] = event
		keys = append(keys, event.Key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		event := eventsByKey[key]
		interfaceName, err := interfaceName(key, names)
		if err != nil {
			return Artifact{}, err
		}
		names[key] = interfaceName
		unsupported, err := renderInterface(&b, event, interfaceName)
		if err != nil {
			return Artifact{}, err
		}
		compatibility = append(compatibility, EventCompatibility{
			EventKey:              event.Key,
			SchemaVersion:         event.SchemaVersion,
			InterfaceName:         interfaceName,
			ValidIdentifier:       true,
			UnsupportedConstructs: unsupported,
		})
	}

	b.WriteString("export const eventKeys = [\n")
	for _, key := range keys {
		b.WriteString(fmt.Sprintf("  %q,\n", key))
	}
	b.WriteString("] as const;\n\n")

	b.WriteString("export type EventKey = (typeof eventKeys)[number];\n\n")

	b.WriteString("export interface EventPropertyMap {\n")
	for _, key := range keys {
		b.WriteString(fmt.Sprintf("  %q: %s;\n", key, names[key]))
	}
	b.WriteString("}\n")

	source := b.String()
	sum := sha256.Sum256([]byte(source))
	return Artifact{
		Source:        source,
		Checksum:      hex.EncodeToString(sum[:]),
		ByteSize:      len(source),
		EventCount:    len(keys),
		Compatibility: compatibility,
	}, nil
}

// interfaceName converts an event key to a unique PascalCase interface name.
func interfaceName(key string, taken map[string]string) (string, error) {
	parts := strings.Split(key, "_")
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			return "", fault.New(fault.Invalid, "ARTIFACT_INVALID_EVENT_KEY", "事件标识格式无效: "+key)
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	name := b.String() + "Properties"
	if existing, ok := taken[name]; ok && existing != key {
		return "", fault.New(fault.Invalid, "ARTIFACT_NAME_COLLISION", "事件标识生成的类型名冲突: "+name)
	}
	taken[name] = key
	return name, nil
}

func renderInterface(b *strings.Builder, event EventSchema, interfaceName string) ([]string, error) {
	if event.DisplayName != "" {
		b.WriteString(fmt.Sprintf("/** %s (schema v%d) */\n", sanitizeComment(event.DisplayName), event.SchemaVersion))
	}
	b.WriteString(fmt.Sprintf("export interface %s {\n", interfaceName))

	rootType, _ := event.Schema["type"].(string)
	if rootType != "object" {
		return nil, fault.New(fault.Invalid, "ARTIFACT_UNSUPPORTED_SCHEMA", "事件属性必须为 object: "+event.Key)
	}
	properties, ok := event.Schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		b.WriteString("}\n\n")
		return nil, nil
	}
	if len(properties) > maxProperties {
		return nil, fault.New(fault.Invalid, "ARTIFACT_SCHEMA_TOO_LARGE", "事件属性超过上限: "+event.Key)
	}
	required := map[string]bool{}
	if raw, ok := event.Schema["required"].([]any); ok {
		for _, item := range raw {
			if key, ok := item.(string); ok {
				required[key] = true
			}
		}
	}
	propertyNames := make([]string, 0, len(properties))
	for name := range properties {
		propertyNames = append(propertyNames, name)
	}
	sort.Strings(propertyNames)

	unsupported := []string{}
	for _, name := range propertyNames {
		prop, ok := properties[name].(map[string]any)
		if !ok {
			return nil, fault.New(fault.Invalid, "ARTIFACT_UNSUPPORTED_SCHEMA", "属性定义必须为对象: "+name)
		}
		typ, notes, err := renderType(prop)
		if err != nil {
			return nil, err
		}
		unsupported = append(unsupported, notes...)
		optional := ""
		if !required[name] {
			optional = "?"
		}
		b.WriteString(fmt.Sprintf("  %s%s: %s;\n", name, optional, typ))
	}
	b.WriteString("}\n\n")
	return unsupported, nil
}

func renderType(prop map[string]any) (string, []string, error) {
	kind, _ := prop["type"].(string)
	notes := []string{}
	switch kind {
	case "string":
		if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
			return literalUnion(enum, kind)
		}
		return "string", notes, nil
	case "integer", "number":
		if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
			return literalUnion(enum, kind)
		}
		// JSON integers stay within JavaScript safe range, so number is the
		// correct TS type; bigint would break normal JSON parsing.
		return "number", notes, nil
	case "boolean":
		if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
			return literalUnion(enum, kind)
		}
		return "boolean", notes, nil
	case "array":
		items, ok := prop["items"].(map[string]any)
		if !ok {
			return "", nil, fault.New(fault.Invalid, "ARTIFACT_UNSUPPORTED_SCHEMA", "数组属性缺少 items 定义")
		}
		itemType, itemNotes, err := renderType(items)
		if err != nil {
			return "", nil, err
		}
		notes = append(notes, itemNotes...)
		return "(" + itemType + ")[]", notes, nil
	default:
		return "", nil, fault.New(fault.Invalid, "ARTIFACT_UNSUPPORTED_SCHEMA", "不支持的属性类型: "+kind)
	}
}

func literalUnion(enum []any, kind string) (string, []string, error) {
	literals := make([]string, 0, len(enum))
	for _, value := range enum {
		switch v := value.(type) {
		case string:
			literals = append(literals, fmt.Sprintf("%q", v))
		case bool:
			literals = append(literals, fmt.Sprintf("%t", v))
		case float64:
			literals = append(literals, formatNumber(v))
		default:
			return "", nil, fault.New(fault.Invalid, "ARTIFACT_UNSUPPORTED_SCHEMA", "枚举值类型不受支持")
		}
	}
	if len(literals) == 0 {
		// An empty enum validates nothing; fall back to the base scalar type.
		return kind, nil, nil
	}
	sort.Strings(literals)
	return strings.Join(literals, " | "), nil, nil
}

func formatNumber(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%d", int64(n))
	}
	return fmt.Sprintf("%g", n)
}

func sanitizeComment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "*/", "* /"), "\n", " ")
}

// ManifestStore is the authorized persistence port for release manifests.
type ManifestStore interface {
	GetReleaseManifest(ctx context.Context, actor, project, releaseID string) (ReleasePlan, error)
}

// Service composes manifest loading with the deterministic generator.
type Service struct {
	store ManifestStore
}

func NewService(store ManifestStore) *Service { return &Service{store: store} }

// BuildTypeScript loads the authorized release manifest and renders its
// TypeScript artifact. All authorization happens inside the store transaction.
func (s *Service) BuildTypeScript(ctx context.Context, actor, project, releaseID string) (ReleasePlan, Artifact, error) {
	plan, err := s.store.GetReleaseManifest(ctx, actor, project, releaseID)
	if err != nil {
		return ReleasePlan{}, Artifact{}, err
	}
	artifact, err := Generate(plan)
	if err != nil {
		return ReleasePlan{}, Artifact{}, err
	}
	return plan, artifact, nil
}
