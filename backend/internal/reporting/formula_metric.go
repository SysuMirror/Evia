package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// FormulaMetricDefinition is the stored composite metric configuration. It
// carries no direct event selection or aggregation: every operand is an
// immutable atomic metric version reached through the formula.
type FormulaMetricDefinition struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Formula     FormulaDefinition `json:"formula"`
}

// FormulaDependencyManifest pins the distinct immutable atomic metric versions
// referenced by one published composite metric. It is the formula analogue of
// the atomic event-version manifest and is hashed into the validation digest.
type FormulaDependencyManifest struct {
	ProjectID   string             `json:"project_id"`
	Environment string             `json:"environment"`
	Metrics     []MetricDependency `json:"metrics"`
}

type FormulaMetricRecord struct {
	ID               string     `json:"metric_id"`
	ProjectID        string     `json:"project_id"`
	Environment      string     `json:"environment"`
	Title            string     `json:"title"`
	DraftRevision    int64      `json:"draft_revision"`
	PublishedVersion int64      `json:"published_version"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
	ArchivedBy       string     `json:"archived_by,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type FormulaMetricDraft struct {
	FormulaMetricRecord
	Definition FormulaMetricDefinition `json:"definition"`
}

type FormulaMetricVersion struct {
	MetricID       string                    `json:"metric_id"`
	ProjectID      string                    `json:"project_id"`
	Environment    string                    `json:"environment"`
	Version        int64                     `json:"version"`
	DraftRevision  int64                     `json:"draft_revision"`
	Definition     FormulaMetricDefinition   `json:"definition"`
	Dependencies   FormulaDependencyManifest `json:"dependencies"`
	DefinitionHash string                    `json:"definition_hash"`
	DependencyHash string                    `json:"dependency_hash"`
	ValidationHash string                    `json:"validation_hash"`
	CreatedAt      time.Time                 `json:"created_at"`
	CreatedBy      string                    `json:"created_by"`
}

type FormulaMetricChange struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Before   any    `json:"before,omitempty"`
	After    any    `json:"after,omitempty"`
}

type FormulaMetricValidation struct {
	Valid                bool                      `json:"valid"`
	ExecutionPerformed   bool                      `json:"execution_performed"`
	DraftRevision        int64                     `json:"draft_revision"`
	PublishedVersion     int64                     `json:"published_version"`
	NormalizedDefinition FormulaMetricDefinition   `json:"normalized_definition"`
	Dependencies         FormulaDependencyManifest `json:"dependencies"`
	DefinitionHash       string                    `json:"definition_hash"`
	DependencyHash       string                    `json:"dependency_hash"`
	Diff                 []FormulaMetricChange     `json:"diff"`
	ValidationHash       string                    `json:"validation_hash"`
}

type FormulaMetricCreate struct {
	Environment string                  `json:"environment"`
	Definition  FormulaMetricDefinition `json:"definition"`
}
type FormulaMetricUpdate struct {
	Definition FormulaMetricDefinition `json:"definition"`
}
type FormulaMetricValidate struct {
	DraftRevision            int64 `json:"draft_revision"`
	ExpectedPublishedVersion int64 `json:"expected_published_version"`
}
type FormulaMetricPublish struct {
	DraftRevision            int64  `json:"draft_revision"`
	ExpectedPublishedVersion int64  `json:"expected_published_version"`
	ValidationHash           string `json:"validation_hash"`
}

// FormulaMetricSnapshot carries the mutable draft, optional immutable baseline
// and the dependency graph resolved inside the authorizing transaction. The
// graph is built while the storage transaction is open so Build remains pure.
type FormulaMetricSnapshot struct {
	Draft    FormulaMetricDraft
	Baseline *FormulaMetricVersion
	Graph    FormulaGraph
}

// FormulaMetricStore mirrors the atomic lifecycle minus execution, which lands
// with the query worker integration in a later vertical.
type FormulaMetricStore interface {
	CreateFormulaMetric(context.Context, string, string, string, FormulaMetricCreate) (FormulaMetricDraft, error)
	ListFormulaMetrics(context.Context, string, string, string, string, int, bool) ([]FormulaMetricRecord, error)
	GetFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error)
	GetFormulaMetricDraft(context.Context, string, string, string) (FormulaMetricDraft, error)
	SaveFormulaMetricDraft(context.Context, string, string, string, int64, FormulaMetricDefinition) (FormulaMetricDraft, error)
	FormulaMetricValidationSnapshot(context.Context, string, string, string, FormulaMetricValidate) (FormulaMetricSnapshot, error)
	PublishFormulaMetric(context.Context, string, string, string, string, FormulaMetricPublish) (FormulaMetricVersion, error)
	ListFormulaMetricVersions(context.Context, string, string, string, int64, int) ([]FormulaMetricVersion, error)
	GetFormulaMetricVersion(context.Context, string, string, string, int64) (FormulaMetricVersion, error)
	EnqueueFormulaMetricVersion(context.Context, string, string, string, int64, string, MetricExecute) (analysis.Job, error)
	ArchiveFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error)
	UnarchiveFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error)
}

// NormalizeFormulaMetricDefinition returns an independent canonical composite
// definition. Graph resolution and authorization stay in the storage adapter.
func NormalizeFormulaMetricDefinition(in FormulaMetricDefinition) (FormulaMetricDefinition, error) {
	data, e := json.Marshal(in)
	if e != nil {
		return FormulaMetricDefinition{}, analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标定义必须为有效 JSON")
	}
	if len(data) > MaxMetricDefinitionBytes {
		return FormulaMetricDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "复合指标定义超过大小限制")
	}
	var out FormulaMetricDefinition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return FormulaMetricDefinition{}, analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标定义字段无效")
	}
	out.Title = strings.TrimSpace(out.Title)
	if !utf8.ValidString(in.Title) || out.Title == "" || utf8.RuneCountInString(out.Title) > 128 || strings.ContainsRune(out.Title, 0) {
		return out, analysis.Invalid("INVALID_TITLE", "标题必须为 1 到 128 个字符", "/title")
	}
	if !utf8.ValidString(in.Description) || utf8.RuneCountInString(out.Description) > 2000 || strings.ContainsRune(out.Description, 0) {
		return out, analysis.Invalid("INVALID_DESCRIPTION", "描述格式无效或超过长度限制", "/description")
	}
	formula, e := NormalizeFormulaDefinition(in.Formula)
	if e != nil {
		return out, e
	}
	out.Formula = formula
	normalized, e := json.Marshal(out)
	if e != nil {
		return FormulaMetricDefinition{}, analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标定义必须为有效 JSON")
	}
	if len(normalized) > MaxMetricDefinitionBytes {
		return FormulaMetricDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "复合指标定义超过大小限制")
	}
	return out, nil
}

// BuildFormulaMetricValidation is pure: the storage adapter resolves the
// dependency graph and units inside its authorizing transaction, then this
// derives the immutable manifest, diff and validation digests.
func BuildFormulaMetricValidation(snapshot FormulaMetricSnapshot) (FormulaMetricValidation, error) {
	var out FormulaMetricValidation
	draft := snapshot.Draft
	if draft.ID == "" || draft.ProjectID == "" || !metricEnvironment(draft.Environment) || draft.DraftRevision < 1 || draft.DraftRevision > analysis.MaxSafeInteger || draft.PublishedVersion < 0 || draft.PublishedVersion > analysis.MaxSafeInteger {
		return out, analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标快照范围或修订无效")
	}
	if len(snapshot.Graph.Direct) == 0 {
		return out, fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "指标依赖图尚未解析")
	}
	definition, e := NormalizeFormulaMetricDefinition(draft.Definition)
	if e != nil {
		return out, e
	}
	var baseline *FormulaMetricDefinition
	if draft.PublishedVersion == 0 {
		if snapshot.Baseline != nil {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
	} else {
		version := snapshot.Baseline
		if version == nil || version.MetricID != draft.ID || version.ProjectID != draft.ProjectID || version.Environment != draft.Environment || version.Version != draft.PublishedVersion {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
		normalized, e := NormalizeFormulaMetricDefinition(version.Definition)
		if e != nil {
			return out, e
		}
		baseline = &normalized
	}
	graph := snapshot.Graph
	if e = VerifyFormulaUnit(definition.Formula, graph); e != nil {
		return out, e
	}
	manifest := FormulaDependencyManifest{ProjectID: draft.ProjectID, Environment: draft.Environment, Metrics: make([]MetricDependency, 0, len(graph.Direct))}
	for _, ref := range graph.Direct {
		resolved := graph.resolved[formulaRefKey(ref)]
		manifest.Metrics = append(manifest.Metrics, MetricDependency{MetricID: ref.MetricID, Version: ref.Version, DefinitionHash: resolved.DefinitionHash})
	}
	sortMetricDependencies(manifest.Metrics)
	definitionHash, e := metricHash(definition)
	if e != nil {
		return out, e
	}
	dependencyHash, e := metricHash(manifest)
	if e != nil {
		return out, e
	}
	diff := formulaMetricDiff(baseline, definition)
	out = FormulaMetricValidation{Valid: true, DraftRevision: draft.DraftRevision, PublishedVersion: draft.PublishedVersion, NormalizedDefinition: definition, Dependencies: manifest, DefinitionHash: definitionHash, DependencyHash: dependencyHash, Diff: diff}
	out.ValidationHash, e = metricHash(struct {
		MetricID         string                    `json:"metric_id"`
		ProjectID        string                    `json:"project_id"`
		Environment      string                    `json:"environment"`
		DraftRevision    int64                     `json:"draft_revision"`
		PublishedVersion int64                     `json:"published_version"`
		DefinitionHash   string                    `json:"definition_hash"`
		DependencyHash   string                    `json:"dependency_hash"`
		Definition       FormulaMetricDefinition   `json:"definition"`
		Dependencies     FormulaDependencyManifest `json:"dependencies"`
		Diff             []FormulaMetricChange     `json:"diff"`
	}{draft.ID, draft.ProjectID, draft.Environment, draft.DraftRevision, draft.PublishedVersion, definitionHash, dependencyHash, definition, manifest, diff})
	return out, e
}

func sortMetricDependencies(deps []MetricDependency) {
	for i := 1; i < len(deps); i++ {
		for j := i; j > 0; j-- {
			if deps[j].MetricID < deps[j-1].MetricID || deps[j].MetricID == deps[j-1].MetricID && deps[j].Version < deps[j-1].Version {
				deps[j], deps[j-1] = deps[j-1], deps[j]
			} else {
				break
			}
		}
	}
}

func formulaMetricDiff(before *FormulaMetricDefinition, after FormulaMetricDefinition) []FormulaMetricChange {
	if before == nil {
		return []FormulaMetricChange{{Category: "definition", Path: "/definition", After: after}}
	}
	changes := []FormulaMetricChange{}
	add := func(category, path string, a, b any) {
		if !metricEqual(a, b) {
			changes = append(changes, FormulaMetricChange{Category: category, Path: path, Before: a, After: b})
		}
	}
	add("metadata", "/title", before.Title, after.Title)
	add("metadata", "/description", before.Description, after.Description)
	add("formula", "/formula", before.Formula, after.Formula)
	return changes
}
