package reporting

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ParseSavedAnalysisDefinition enforces the public schema before normalization.
// Decode retains numeric/boolean scalar identity and rejects duplicate keys.
func ParseSavedAnalysisDefinition(raw []byte, validator analysis.Validator) (SavedAnalysisDefinition, error) {
	var out SavedAnalysisDefinition
	if len(raw) > MaxSavedAnalysisDefinitionBytes {
		return out, analysis.Invalid("BODY_TOO_LARGE", "保存分析定义超过大小限制")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析必须是单一且无重复字段的 JSON")
	}
	if validator == nil {
		return out, fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "保存分析验证器不可用")
	}
	if e = validator.Validate("SavedAnalysisDefinition", value); e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义不符合接口约定")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义字段无效")
	}
	return NormalizeSavedAnalysisDefinition(out)
}

// NormalizeSavedAnalysisDefinition returns independent canonical configuration.
// It checks structural policy; catalog publication and field types are checked
// by the authorized transactional adapter, never by fabricated runtime context.
func NormalizeSavedAnalysisDefinition(in SavedAnalysisDefinition) (SavedAnalysisDefinition, error) {
	data, e := json.Marshal(in)
	if e != nil {
		return SavedAnalysisDefinition{}, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义必须为有效 JSON")
	}
	if len(data) > MaxSavedAnalysisDefinitionBytes {
		return SavedAnalysisDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "保存分析定义超过大小限制")
	}
	var out SavedAnalysisDefinition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义字段无效")
	}
	out.Title = strings.TrimSpace(out.Title)
	if !utf8.ValidString(in.Title) || out.Title == "" || utf8.RuneCountInString(out.Title) > 128 || strings.ContainsRune(out.Title, 0) {
		return out, analysis.Invalid("INVALID_TITLE", "标题必须为 1 到 128 个字符", "/title")
	}
	if !utf8.ValidString(in.Description) || utf8.RuneCountInString(out.Description) > 2000 || strings.ContainsRune(out.Description, 0) {
		return out, analysis.Invalid("INVALID_DESCRIPTION", "描述格式无效或超过长度限制", "/description")
	}
	if e = analysis.ValidateTemplate(&out.Query); e != nil {
		return out, e
	}
	sort.Slice(out.Query.EventVersions, func(i, j int) bool { return out.Query.EventVersions[i].EventKey < out.Query.EventVersions[j].EventKey })
	q := out.Query
	valid := false
	switch out.Visualization.Kind {
	case "table":
		valid = true
	case "metric":
		valid = q.Kind == "trend" && len(q.Metrics) == 1 && len(q.GroupBy) == 0 && q.Interval == ""
	case "line":
		valid = q.Kind == "trend" && q.Interval != ""
	case "bar":
		valid = q.Kind == "trend" && (len(q.GroupBy) > 0 || q.Interval != "")
	case "funnel":
		valid = q.Kind == "funnel"
	}
	if !valid {
		return out, analysis.Invalid("INVALID_VISUALIZATION", "图表类型与分析配置不兼容", "/visualization/kind")
	}
	// Defaults can expand otherwise valid input. Bound the persisted canonical
	// configuration as well as the caller's representation, including callers
	// that invoke this helper directly from a storage transaction.
	normalized, e := json.Marshal(out)
	if e != nil {
		return SavedAnalysisDefinition{}, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义必须为有效 JSON")
	}
	if len(normalized) > MaxSavedAnalysisDefinitionBytes {
		return SavedAnalysisDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "保存分析定义超过大小限制")
	}
	return out, nil
}

func savedHash(value any) (string, error) {
	hash, e := canonical.Hash(value)
	if e != nil {
		return "", analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义包含不支持的 JSON 值")
	}
	return hash, nil
}
func savedEqual(a, b any) bool {
	x, e := canonical.JSON(a)
	if e != nil {
		return false
	}
	y, e := canonical.JSON(b)
	return e == nil && bytes.Equal(x, y)
}

// BuildSavedAnalysisValidation is pure and safe to recompute inside publication.
// Dependencies must describe exactly the pinned immutable configuration. Its
// digest is an acknowledgement of this content and baseline, never authority.
func BuildSavedAnalysisValidation(snapshot SavedAnalysisSnapshot) (SavedAnalysisValidation, error) {
	var out SavedAnalysisValidation
	draft := snapshot.Draft
	if draft.ID == "" || draft.ProjectID == "" || !savedEnvironment(draft.Environment) || draft.DraftRevision < 1 || draft.DraftRevision > analysis.MaxSafeInteger || draft.PublishedVersion < 0 || draft.PublishedVersion > analysis.MaxSafeInteger {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析快照范围或修订无效")
	}
	definition, e := NormalizeSavedAnalysisDefinition(draft.Definition)
	if e != nil {
		return out, e
	}
	var baseline *SavedAnalysisDefinition
	if draft.PublishedVersion == 0 {
		if snapshot.Baseline != nil {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
	} else {
		version := snapshot.Baseline
		if version == nil || version.AnalysisID != draft.ID || version.ProjectID != draft.ProjectID || version.Environment != draft.Environment || version.Version != draft.PublishedVersion {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
		normalized, e := NormalizeSavedAnalysisDefinition(version.Definition)
		if e != nil {
			return out, e
		}
		baseline = &normalized
	}
	dependencies, e := savedDependencies(draft, definition.Query, snapshot.Dependencies)
	if e != nil {
		return out, e
	}
	definitionHash, e := savedHash(definition)
	if e != nil {
		return out, e
	}
	dependencyHash, e := savedHash(dependencies)
	if e != nil {
		return out, e
	}
	diff := savedAnalysisDiff(baseline, definition)
	out = SavedAnalysisValidation{Valid: true, DraftRevision: draft.DraftRevision, PublishedVersion: draft.PublishedVersion, NormalizedDefinition: definition, Dependencies: dependencies, DefinitionHash: definitionHash, DependencyHash: dependencyHash, Diff: diff}
	out.ValidationHash, e = savedHash(struct {
		AnalysisID       string                  `json:"analysis_id"`
		ProjectID        string                  `json:"project_id"`
		Environment      string                  `json:"environment"`
		DraftRevision    int64                   `json:"draft_revision"`
		PublishedVersion int64                   `json:"published_version"`
		Definition       SavedAnalysisDefinition `json:"definition"`
		Dependencies     DependencyManifest      `json:"dependencies"`
		Diff             []SavedAnalysisChange   `json:"diff"`
	}{draft.ID, draft.ProjectID, draft.Environment, draft.DraftRevision, draft.PublishedVersion, definition, dependencies, diff})
	return out, e
}

func savedDependencies(draft SavedAnalysisDraft, template analysis.Template, in DependencyManifest) (DependencyManifest, error) {
	invalid := func() (DependencyManifest, error) {
		return DependencyManifest{}, analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "保存分析依赖与固定事件版本不匹配")
	}
	mode := "event_versions"
	selected := map[analysis.EventVersion]bool{}
	for _, v := range template.EventVersions {
		selected[v] = true
	}
	if template.Kind == "funnel" {
		mode = "funnel_steps"
		for _, step := range template.Funnel.Steps {
			selected[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if in.ProjectID != draft.ProjectID || in.Environment != draft.Environment || in.SelectionMode != mode || len(in.UnfixedEventKeys) > 0 || len(in.EventVersions) != len(selected) {
		return invalid()
	}
	out := DependencyManifest{ProjectID: in.ProjectID, Environment: in.Environment, SelectionMode: mode, UnfixedEventKeys: []string{}, EventVersions: make([]Dependency, 0, len(selected))}
	seen := map[analysis.EventVersion]bool{}
	for _, d := range in.EventVersions {
		key := analysis.EventVersion{EventKey: d.EventKey, SchemaVersion: d.SchemaVersion}
		if !selected[key] || seen[key] || !savedDigest(d.DefinitionHash) {
			return invalid()
		}
		seen[key] = true
		out.EventVersions = append(out.EventVersions, d)
	}
	sort.Slice(out.EventVersions, func(i, j int) bool {
		a, b := out.EventVersions[i], out.EventVersions[j]
		return a.EventKey < b.EventKey || a.EventKey == b.EventKey && a.SchemaVersion < b.SchemaVersion
	})
	return out, nil
}
func savedAnalysisDiff(before *SavedAnalysisDefinition, after SavedAnalysisDefinition) []SavedAnalysisChange {
	if before == nil {
		return []SavedAnalysisChange{{Category: "definition", Path: "/definition", After: after}}
	}
	changes := []SavedAnalysisChange{}
	add := func(category, path string, a, b any) {
		if !savedEqual(a, b) {
			changes = append(changes, SavedAnalysisChange{Category: category, Path: path, Before: a, After: b})
		}
	}
	add("metadata", "/title", before.Title, after.Title)
	add("metadata", "/description", before.Description, after.Description)
	add("kind", "/query/kind", before.Query.Kind, after.Query.Kind)
	add("event_versions", "/query/event_versions", before.Query.EventVersions, after.Query.EventVersions)
	add("filter", "/query/filter", before.Query.Filter, after.Query.Filter)
	add("subject", "/query/subject", before.Query.Subject, after.Query.Subject)
	add("metrics", "/query/metrics", before.Query.Metrics, after.Query.Metrics)
	add("group_by", "/query/group_by", before.Query.GroupBy, after.Query.GroupBy)
	add("interval", "/query/interval", before.Query.Interval, after.Query.Interval)
	add("funnel", "/query/funnel", before.Query.Funnel, after.Query.Funnel)
	add("limit", "/query/limit", before.Query.Limit, after.Query.Limit)
	add("visualization", "/visualization", before.Visualization, after.Visualization)
	return changes
}
func savedEnvironment(value string) bool {
	return value == "development" || value == "staging" || value == "production"
}
func savedDigest(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' && r < 'a' || r > 'f' }) < 0
}
