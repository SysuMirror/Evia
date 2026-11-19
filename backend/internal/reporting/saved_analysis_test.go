package reporting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func savedDefinition() SavedAnalysisDefinition {
	return SavedAnalysisDefinition{Title: "  Page visits  ", Query: analysis.Template{Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}}, Visualization: SavedAnalysisVisualization{Kind: "metric"}}
}
func savedSnapshot(t *testing.T, definition SavedAnalysisDefinition) SavedAnalysisSnapshot {
	t.Helper()
	normalized, e := NormalizeSavedAnalysisDefinition(definition)
	if e != nil {
		t.Fatal(e)
	}
	mode := "event_versions"
	selected := normalized.Query.EventVersions
	if normalized.Query.Kind == analysis.QueryKindFunnel {
		mode = "funnel_steps"
		selected = nil
		for _, step := range normalized.Query.Funnel.Steps {
			selected = append(selected, analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion})
		}
	}
	if normalized.Query.Kind == analysis.QueryKindRetention {
		mode = "retention_steps"
		selected = nil
		for _, step := range []analysis.Step{normalized.Query.Retention.StartEvent, normalized.Query.Retention.ReturnEvent} {
			selected = append(selected, analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion})
		}
	}
	if normalized.Query.Kind == analysis.QueryKindSegment {
		mode = "segment_steps"
		selected = nil
		step := normalized.Query.Segment.Event
		selected = append(selected, analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion})
	}
	if normalized.Query.Kind == analysis.QueryKindPath {
		selected = nil
		if normalized.Query.Path.StartEvent != nil {
			mode = "path_steps"
			step := *normalized.Query.Path.StartEvent
			selected = append(selected, analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion})
		} else {
			mode = "all_events"
		}
	}
	if normalized.Query.Kind == analysis.QueryKindAttribution {
		mode = "attribution_steps"
		selected = nil
		for _, step := range append([]analysis.Step{normalized.Query.Attribution.Conversion}, normalized.Query.Attribution.Touchpoints...) {
			selected = append(selected, analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion})
		}
	}
	dependencies := []Dependency{}
	seen := map[analysis.EventVersion]bool{}
	for _, version := range selected {
		if !seen[version] {
			dependencies = append(dependencies, Dependency{EventKey: version.EventKey, SchemaVersion: version.SchemaVersion, DefinitionHash: strings.Repeat("a", 64)})
			seen[version] = true
		}
	}
	return SavedAnalysisSnapshot{Draft: SavedAnalysisDraft{SavedAnalysis: SavedAnalysis{ID: "analysis-one", ProjectID: "project-one", Environment: "development", DraftRevision: 1, Title: normalized.Title, Kind: normalized.Query.Kind}, Definition: normalized}, Dependencies: DependencyManifest{ProjectID: "project-one", Environment: "development", SelectionMode: mode, UnfixedEventKeys: []string{}, EventVersions: dependencies}}
}
func savedErrorCode(t *testing.T, e error, code string) {
	t.Helper()
	if e == nil || fault.Public(e).Code != code {
		t.Fatalf("want %s, got %v", code, e)
	}
}
func savedValidator(t *testing.T) analysis.Validator {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestSavedDefinitionStrictParsingPreservesFalseZeroAndRejectsRuntimeFields(t *testing.T) {
	v := savedValidator(t)
	for _, scalar := range []string{"false", "0"} {
		raw := `{"title":"  Visits  ","query":{"kind":"trend","event_versions":[{"event_key":"page_opened","schema_version":1}],"filter":{"op":"eq","field":{"kind":"property","event_key":"page_opened","schema_version":1,"key":"enabled"},"value":` + scalar + `}},"visualization":{"kind":"metric"}}`
		result, e := ParseSavedAnalysisDefinition([]byte(raw), v)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(result)
		if e != nil {
			t.Fatal(e)
		}
		if result.Title != "Visits" || result.Description != "" || !strings.Contains(string(encoded), `"value":`+scalar) || result.Query.Limit != 200 || result.Query.Metrics[0].Accuracy != analysis.AccuracyExact {
			t.Fatalf("normalization changed scalar or defaults: %s", encoded)
		}
		for _, field := range []string{`"environment":"development"`, `"time_range":{}`, `"timezone":"UTC"`, `"identity_filter":{}`, `"identity_revision":1`, `"cursor":"x"`, `"event_keys":["page_opened"]`} {
			bad := strings.Replace(raw, `"kind":"trend"`, `"kind":"trend",`+field, 1)
			if _, e := ParseSavedAnalysisDefinition([]byte(bad), v); e == nil {
				t.Fatalf("runtime field accepted: %s", field)
			}
		}
		duplicate := strings.Replace(raw, `"title":`, `"title":"first","title":`, 1)
		if _, e := ParseSavedAnalysisDefinition([]byte(duplicate), v); e == nil {
			t.Fatal("duplicate accepted")
		}
		if _, e := ParseSavedAnalysisDefinition([]byte(raw+`{}`), v); e == nil {
			t.Fatal("trailing JSON accepted")
		}
		bad := strings.Replace(raw, `"key":"enabled"`, `"key":"enabled","resolved_type":"boolean"`, 1)
		if _, e := ParseSavedAnalysisDefinition([]byte(bad), v); e == nil {
			t.Fatal("resolved type accepted")
		}
	}
	_, e := ParseSavedAnalysisDefinition([]byte(strings.Repeat(" ", MaxSavedAnalysisDefinitionBytes+1)), v)
	savedErrorCode(t, e, "BODY_TOO_LARGE")
}
func TestSavedDefinitionNormalizationClonesAndValidatesCharts(t *testing.T) {
	in := savedDefinition()
	in.Query.EventVersions = append(in.Query.EventVersions, analysis.EventVersion{EventKey: "a_opened", SchemaVersion: 2})
	in.Query.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "enabled"}, Value: false}
	normalized, e := NormalizeSavedAnalysisDefinition(in)
	if e != nil {
		t.Fatal(e)
	}
	normalized.Query.Filter.Field.Key = "changed"
	normalized.Query.EventVersions[0].EventKey = "changed"
	if in.Title != "  Page visits  " || in.Query.Filter.Field.Key != "enabled" || in.Query.EventVersions[0].EventKey != "page_opened" || len(in.Query.Metrics) != 0 {
		t.Fatal("caller definition mutated")
	}
	for _, kind := range []string{"line", "bar", "funnel", "unknown"} {
		d := savedDefinition()
		d.Visualization.Kind = kind
		_, e := NormalizeSavedAnalysisDefinition(d)
		savedErrorCode(t, e, "INVALID_VISUALIZATION")
	}
	d := savedDefinition()
	d.Query.Interval = analysis.IntervalDay
	d.Visualization.Kind = "line"
	if _, e := NormalizeSavedAnalysisDefinition(d); e != nil {
		t.Fatal(e)
	}
	d.Visualization.Kind = "bar"
	if _, e := NormalizeSavedAnalysisDefinition(d); e != nil {
		t.Fatal(e)
	}
	d.Visualization.Kind = "metric"
	if _, e := NormalizeSavedAnalysisDefinition(d); e == nil {
		t.Fatal("bucketed metric card accepted")
	}
	d = savedDefinition()
	d.Title = " \t "
	_, e = NormalizeSavedAnalysisDefinition(d)
	savedErrorCode(t, e, "INVALID_TITLE")
	d = savedDefinition()
	d.Description = strings.Repeat("界", 2001)
	_, e = NormalizeSavedAnalysisDefinition(d)
	savedErrorCode(t, e, "INVALID_DESCRIPTION")
}
func TestSavedValidationHashesBindScopeRevisionsAndDependenciesWithoutClocks(t *testing.T) {
	snapshot := savedSnapshot(t, savedDefinition())
	first, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !first.Valid || first.ExecutionPerformed || len(first.Diff) != 1 || first.Diff[0].Category != "definition" {
		t.Fatal(first)
	}
	snapshot.Draft.CreatedAt = time.Now().Add(24 * time.Hour)
	snapshot.Draft.UpdatedAt = time.Now()
	same, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil || first.ValidationHash != same.ValidationHash {
		t.Fatal("clock changed digest", e)
	}
	snapshot.Draft.ID = "analysis-two"
	other, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil || first.ValidationHash == other.ValidationHash || first.DefinitionHash != other.DefinitionHash {
		t.Fatal("analysis scope missing", e)
	}
	snapshot.Draft.ID = "analysis-one"
	snapshot.Draft.DraftRevision++
	other, e = BuildSavedAnalysisValidation(snapshot)
	if e != nil || first.ValidationHash == other.ValidationHash {
		t.Fatal("draft revision missing", e)
	}
	snapshot.Draft.DraftRevision = 1
	snapshot.Dependencies.EventVersions[0].DefinitionHash = strings.Repeat("b", 64)
	other, e = BuildSavedAnalysisValidation(snapshot)
	if e != nil || first.ValidationHash == other.ValidationHash || first.DependencyHash == other.DependencyHash {
		t.Fatal("catalog dependency missing", e)
	}
	other.NormalizedDefinition.Query.EventVersions[0].EventKey = "mutated"
	if snapshot.Draft.Definition.Query.EventVersions[0].EventKey != "page_opened" {
		t.Fatal("result aliases snapshot")
	}
}
func TestSavedValidationDependencySetAndBaselineAreExact(t *testing.T) {
	for _, change := range []func(*SavedAnalysisSnapshot){
		func(s *SavedAnalysisSnapshot) { s.Dependencies.ProjectID = "other" }, func(s *SavedAnalysisSnapshot) { s.Dependencies.Environment = "production" },
		func(s *SavedAnalysisSnapshot) { s.Dependencies.SelectionMode = "event_keys" }, func(s *SavedAnalysisSnapshot) { s.Dependencies.UnfixedEventKeys = []string{"page_opened"} },
		func(s *SavedAnalysisSnapshot) { s.Dependencies.EventVersions = nil }, func(s *SavedAnalysisSnapshot) { s.Dependencies.EventVersions[0].SchemaVersion = 2 },
		func(s *SavedAnalysisSnapshot) { s.Dependencies.EventVersions[0].DefinitionHash = "fake" },
	} {
		snapshot := savedSnapshot(t, savedDefinition())
		change(&snapshot)
		_, e := BuildSavedAnalysisValidation(snapshot)
		savedErrorCode(t, e, "INVALID_ANALYSIS_DEPENDENCIES")
	}
	snapshot := savedSnapshot(t, savedDefinition())
	snapshot.Draft.PublishedVersion = 1
	_, e := BuildSavedAnalysisValidation(snapshot)
	savedErrorCode(t, e, "PUBLISHED_VERSION_CONFLICT")
	snapshot.Baseline = &SavedAnalysisVersion{AnalysisID: "different", ProjectID: snapshot.Draft.ProjectID, Environment: "development", Version: 1, Definition: snapshot.Draft.Definition}
	_, e = BuildSavedAnalysisValidation(snapshot)
	savedErrorCode(t, e, "PUBLISHED_VERSION_CONFLICT")
}
func TestSavedValidationDiffPreservesOrderedMeaningAndNormalizesOnlySets(t *testing.T) {
	definition := savedDefinition()
	definition.Query.EventVersions = append(definition.Query.EventVersions, analysis.EventVersion{EventKey: "a_opened", SchemaVersion: 1})
	snapshot := savedSnapshot(t, definition)
	baseline := snapshot.Draft.Definition
	snapshot.Draft.PublishedVersion = 1
	snapshot.Baseline = &SavedAnalysisVersion{AnalysisID: snapshot.Draft.ID, ProjectID: snapshot.Draft.ProjectID, Environment: snapshot.Draft.Environment, Version: 1, Definition: baseline}
	snapshot.Draft.Definition.Query.EventVersions = []analysis.EventVersion{baseline.Query.EventVersions[1], baseline.Query.EventVersions[0]}
	unchanged, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil || len(unchanged.Diff) != 0 {
		t.Fatal("set reordering changed meaning", unchanged, e)
	}
	snapshot.Draft.Definition.Title = "New title"
	snapshot.Draft.Definition.Query.Metrics = []analysis.Metric{{Op: "unique"}}
	snapshot.Draft.Definition.Query.Subject = analysis.SubjectUID
	changed, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if len(changed.Diff) != 3 || changed.Diff[0].Path != "/title" || changed.Diff[1].Path != "/query/subject" || changed.Diff[2].Path != "/query/metrics" {
		t.Fatal("unstable semantic paths", changed.Diff)
	}
	if unchanged.ValidationHash == changed.ValidationHash {
		t.Fatal("diff/content absent from acknowledgement")
	}
	funnel := SavedAnalysisDefinition{Title: "Conversion", Query: analysis.Template{Kind: analysis.QueryKindFunnel, Subject: analysis.SubjectUID, Funnel: &analysis.Funnel{Mode: "ordered_with_interleaving", WindowSeconds: 60, Steps: []analysis.Step{{EventKey: "opened", SchemaVersion: 1}, {EventKey: "closed", SchemaVersion: 1}}}}, Visualization: SavedAnalysisVisualization{Kind: analysis.QueryKindFunnel}}
	fs := savedSnapshot(t, funnel)
	b := fs.Draft.Definition
	fs.Draft.PublishedVersion = 1
	fs.Baseline = &SavedAnalysisVersion{AnalysisID: fs.Draft.ID, ProjectID: fs.Draft.ProjectID, Environment: fs.Draft.Environment, Version: 1, Definition: b}
	fs.Draft.Definition.Query.Funnel = &analysis.Funnel{Mode: "ordered_with_interleaving", WindowSeconds: 60, Steps: []analysis.Step{{EventKey: "closed", SchemaVersion: 1}, {EventKey: "opened", SchemaVersion: 1}}}
	f, e := BuildSavedAnalysisValidation(fs)
	if e != nil || len(f.Diff) != 1 || f.Diff[0].Path != "/query/funnel" {
		t.Fatal("funnel step ordering lost", f, e)
	}
}

func retentionSavedDefinition() SavedAnalysisDefinition {
	return SavedAnalysisDefinition{
		Title: "Signup retention",
		Query: analysis.Template{
			Kind:    "retention",
			Subject: analysis.SubjectUID,
			Retention: &analysis.Retention{
				StartEvent:  analysis.Step{EventKey: "sign_up", SchemaVersion: 1},
				ReturnEvent: analysis.Step{EventKey: "login", SchemaVersion: 2},
				Granularity: analysis.RetentionGranularityDay,
				Periods:     4,
			},
		},
		Visualization: SavedAnalysisVisualization{Kind: "table"},
	}
}

func TestSavedValidationRetentionPinsBoundarySteps(t *testing.T) {
	snapshot := savedSnapshot(t, retentionSavedDefinition())
	out, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !out.Valid || out.Dependencies.SelectionMode != "retention_steps" {
		t.Fatalf("retention dependency mode wrong: %+v", out.Dependencies)
	}
	if len(out.Dependencies.EventVersions) != 2 {
		t.Fatalf("want two boundary step pins, got %d", len(out.Dependencies.EventVersions))
	}
	want := map[analysis.EventVersion]bool{
		{EventKey: "sign_up", SchemaVersion: 1}: true,
		{EventKey: "login", SchemaVersion: 2}:   true,
	}
	for _, dep := range out.Dependencies.EventVersions {
		if !want[analysis.EventVersion{EventKey: dep.EventKey, SchemaVersion: dep.SchemaVersion}] {
			t.Fatalf("unexpected retention dependency: %+v", dep)
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(*SavedAnalysisSnapshot)
	}{
		{"wrong selection mode", func(s *SavedAnalysisSnapshot) { s.Dependencies.SelectionMode = "event_versions" }},
		{"dropped boundary pin", func(s *SavedAnalysisSnapshot) { s.Dependencies.EventVersions = s.Dependencies.EventVersions[:1] }},
		{"malformed definition digest", func(s *SavedAnalysisSnapshot) {
			s.Dependencies.EventVersions[0].DefinitionHash = "not-a-hex-digest"
		}},
		{"unfixed event present", func(s *SavedAnalysisSnapshot) { s.Dependencies.UnfixedEventKeys = []string{"sign_up"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := savedSnapshot(t, retentionSavedDefinition())
			tc.mutate(&broken)
			_, err := BuildSavedAnalysisValidation(broken)
			savedErrorCode(t, err, "INVALID_ANALYSIS_DEPENDENCIES")
		})
	}
}

func segmentSavedDefinition() SavedAnalysisDefinition {
	return SavedAnalysisDefinition{
		Title: "Plan segments",
		Query: analysis.Template{
			Kind:    "segment",
			Subject: analysis.SubjectUID,
			Segment: &analysis.Segment{
				Event: analysis.Step{EventKey: "sign_up", SchemaVersion: 1},
				Field: analysis.Field{Kind: "property", EventKey: "sign_up", SchemaVersion: 1, Key: "plan"},
			},
		},
		Visualization: SavedAnalysisVisualization{Kind: "table"},
	}
}

func TestSavedValidationSegmentPinsEvent(t *testing.T) {
	snapshot := savedSnapshot(t, segmentSavedDefinition())
	out, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !out.Valid || out.Dependencies.SelectionMode != "segment_steps" || len(out.Dependencies.EventVersions) != 1 {
		t.Fatalf("segment dependency wrong: %+v", out.Dependencies)
	}
	if out.Dependencies.EventVersions[0].EventKey != "sign_up" {
		t.Fatalf("segment pin lost: %+v", out.Dependencies.EventVersions[0])
	}
	for _, tc := range []struct {
		name   string
		mutate func(*SavedAnalysisSnapshot)
	}{
		{"wrong selection mode", func(s *SavedAnalysisSnapshot) { s.Dependencies.SelectionMode = "event_versions" }},
		{"malformed digest", func(s *SavedAnalysisSnapshot) {
			s.Dependencies.EventVersions[0].DefinitionHash = "not-a-hex-digest"
		}},
		{"unfixed event present", func(s *SavedAnalysisSnapshot) { s.Dependencies.UnfixedEventKeys = []string{"sign_up"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := savedSnapshot(t, segmentSavedDefinition())
			tc.mutate(&broken)
			_, err := BuildSavedAnalysisValidation(broken)
			savedErrorCode(t, err, "INVALID_ANALYSIS_DEPENDENCIES")
		})
	}
}

func pathSavedDefinition(start *analysis.Step) SavedAnalysisDefinition {
	return SavedAnalysisDefinition{
		Title: "Onboarding paths",
		Query: analysis.Template{
			Kind:    "path",
			Subject: analysis.SubjectUID,
			Path:    &analysis.Path{Depth: 3, StartEvent: start},
		},
		Visualization: SavedAnalysisVisualization{Kind: "table"},
	}
}

func TestSavedValidationPathPinsStartOrReadsAllEvents(t *testing.T) {
	// With a pinned start event the manifest carries one path_steps dependency.
	started := savedSnapshot(t, pathSavedDefinition(&analysis.Step{EventKey: "open", SchemaVersion: 1}))
	out, e := BuildSavedAnalysisValidation(started)
	if e != nil {
		t.Fatal(e)
	}
	if !out.Valid || out.Dependencies.SelectionMode != "path_steps" || len(out.Dependencies.EventVersions) != 1 {
		t.Fatalf("pinned path dependency wrong: %+v", out.Dependencies)
	}
	// Without a start event the path reads every in-window event: all_events, no pins.
	unpinned := savedSnapshot(t, pathSavedDefinition(nil))
	all, e := BuildSavedAnalysisValidation(unpinned)
	if e != nil {
		t.Fatal(e)
	}
	if !all.Valid || all.Dependencies.SelectionMode != "all_events" || len(all.Dependencies.EventVersions) != 0 {
		t.Fatalf("unpinned path must be all_events with no pins: %+v", all.Dependencies)
	}
	broken := savedSnapshot(t, pathSavedDefinition(&analysis.Step{EventKey: "open", SchemaVersion: 1}))
	broken.Dependencies.SelectionMode = "all_events"
	_, err := BuildSavedAnalysisValidation(broken)
	savedErrorCode(t, err, "INVALID_ANALYSIS_DEPENDENCIES")
}

func attributionSavedDefinition() SavedAnalysisDefinition {
	return SavedAnalysisDefinition{
		Title: "Conversion attribution",
		Query: analysis.Template{
			Kind:    "attribution",
			Subject: analysis.SubjectUID,
			Attribution: &analysis.Attribution{
				Conversion:    analysis.Step{EventKey: "buy", SchemaVersion: 1},
				Touchpoints:   []analysis.Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 2}},
				WindowSeconds: 86400,
				Model:         analysis.AttributionModelLastTouch,
			},
		},
		Visualization: SavedAnalysisVisualization{Kind: "table"},
	}
}

func TestSavedValidationAttributionPinsConversionAndTouchpoints(t *testing.T) {
	snapshot := savedSnapshot(t, attributionSavedDefinition())
	out, e := BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !out.Valid || out.Dependencies.SelectionMode != "attribution_steps" || len(out.Dependencies.EventVersions) != 3 {
		t.Fatalf("attribution dependency wrong: %+v", out.Dependencies)
	}
	want := map[analysis.EventVersion]bool{
		{EventKey: "buy", SchemaVersion: 1}:        true,
		{EventKey: "ad_click", SchemaVersion: 1}:   true,
		{EventKey: "email_open", SchemaVersion: 2}: true,
	}
	for _, dep := range out.Dependencies.EventVersions {
		if !want[analysis.EventVersion{EventKey: dep.EventKey, SchemaVersion: dep.SchemaVersion}] {
			t.Fatalf("unexpected attribution dependency: %+v", dep)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*SavedAnalysisSnapshot)
	}{
		{"wrong selection mode", func(s *SavedAnalysisSnapshot) { s.Dependencies.SelectionMode = "event_versions" }},
		{"dropped touchpoint pin", func(s *SavedAnalysisSnapshot) { s.Dependencies.EventVersions = s.Dependencies.EventVersions[:2] }},
		{"malformed digest", func(s *SavedAnalysisSnapshot) {
			s.Dependencies.EventVersions[0].DefinitionHash = "not-a-hex-digest"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := savedSnapshot(t, attributionSavedDefinition())
			tc.mutate(&broken)
			_, err := BuildSavedAnalysisValidation(broken)
			savedErrorCode(t, err, "INVALID_ANALYSIS_DEPENDENCIES")
		})
	}
}

type savedStoreFake struct {
	SavedAnalysisStore
	calls    int
	snapshot SavedAnalysisSnapshot
	created  SavedAnalysisCreate
	limit    int
}

func (f *savedStoreFake) CreateSavedAnalysis(_ context.Context, _, _, _ string, in SavedAnalysisCreate) (SavedAnalysisDraft, error) {
	f.calls++
	f.created = in
	return SavedAnalysisDraft{}, nil
}
func (f *savedStoreFake) SavedAnalysisValidationSnapshot(context.Context, string, string, string, SavedAnalysisValidate) (SavedAnalysisSnapshot, error) {
	f.calls++
	return f.snapshot, nil
}
func (f *savedStoreFake) ListSavedAnalyses(_ context.Context, _, _, _, _ string, limit int, _ bool, _ string) ([]SavedAnalysis, error) {
	f.calls++
	f.limit = limit
	return nil, nil
}
func (f *savedStoreFake) ListSavedAnalysisVersions(_ context.Context, _, _, _ string, _ int64, limit int) ([]SavedAnalysisVersion, error) {
	f.calls++
	f.limit = limit
	return nil, nil
}
func (f *savedStoreFake) EnqueueSavedAnalysisVersion(context.Context, string, string, string, int64, string, SavedAnalysisExecute) (analysis.Job, error) {
	f.calls++
	return analysis.Job{}, nil
}
func TestSavedServiceValidatesBeforePersistenceAndPreservesBoundedLookahead(t *testing.T) {
	store := &savedStoreFake{}
	service := NewSavedAnalyses(store, savedValidator(t), false)
	ctx := context.Background()
	in := SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()}
	if _, e := service.CreateSavedAnalysis(ctx, "actor", "project", "key", in); e != nil {
		t.Fatal(e)
	}
	if store.calls != 1 || store.created.Definition.Title != "Page visits" || len(in.Definition.Query.Metrics) != 0 {
		t.Fatal("service failed normalization/isolation")
	}
	in.Definition.Visualization.Kind = "unknown"
	if _, e := service.CreateSavedAnalysis(ctx, "actor", "project", "key", in); e == nil || store.calls != 1 {
		t.Fatal("invalid definition reached store")
	}
	if _, e := service.ListSavedAnalyses(ctx, "actor", "project", "development", "", 100, false, ""); e != nil || store.limit != 101 {
		t.Fatal(store.limit, e)
	}
	if _, e := service.ListSavedAnalysisVersions(ctx, "actor", "project", "id", 0, 50); e != nil || store.limit != 51 {
		t.Fatal(store.limit, e)
	}
	before := store.calls
	if _, e := service.ListSavedAnalyses(ctx, "actor", "project", "development", "", 101, false, ""); e == nil || store.calls != before {
		t.Fatal("unbounded page admitted")
	}
	from := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	_, e := service.EnqueueSavedAnalysisVersion(ctx, "actor", "project", "id", 1, "run-key", SavedAnalysisExecute{TimeRange: analysis.TimeRange{From: from, To: from.Add(time.Hour)}, Timezone: "UTC"})
	savedErrorCode(t, e, "QUERY_STORAGE_UNAVAILABLE")
	if store.calls != before {
		t.Fatal("disabled execution reached store")
	}
	service = NewSavedAnalyses(store, savedValidator(t), true)
	if _, e := service.EnqueueSavedAnalysisVersion(ctx, "actor", "project", "id", 1, "run-key", SavedAnalysisExecute{TimeRange: analysis.TimeRange{From: from, To: from}, Timezone: "UTC"}); e == nil || store.calls != before {
		t.Fatal("invalid real time reached store")
	}
}
func TestSavedServiceAcceptsMetricReferenceWriteInput(t *testing.T) {
	store := &savedStoreFake{}
	service := NewSavedAnalyses(store, savedValidator(t), false)
	// A metric-reference write input omits the inline query; the value-typed
	// domain Query must not rematerialize as an empty object during the
	// service's re-marshal and re-validation.
	input := SavedAnalysisCreate{
		Environment: "development",
		Definition: SavedAnalysisDefinition{
			Title:         "Metric card",
			MetricRef:     &MetricReference{MetricID: testMetricID, Version: 1},
			Visualization: SavedAnalysisVisualization{Kind: "metric"},
		},
	}
	if _, e := service.CreateSavedAnalysis(context.Background(), "actor", "project", "metric-key", input); e != nil {
		t.Fatalf("metric reference write input rejected: %v", e)
	}
	if store.calls != 1 {
		t.Fatal("valid metric reference did not reach the store")
	}
	if store.created.Definition.MetricRef == nil || store.created.Definition.MetricRef.Version != 1 {
		t.Fatalf("store did not receive the metric pin: %+v", store.created.Definition)
	}
	if store.created.Definition.Query.Kind != "" {
		t.Fatalf("write input must not carry a derived query: %+v", store.created.Definition.Query)
	}
	// A metric reference with a non-metric visualization is rejected before the store.
	bad := input
	bad.Definition.Visualization = SavedAnalysisVisualization{Kind: "line"}
	if _, e := service.CreateSavedAnalysis(context.Background(), "actor", "project", "bad-key", bad); e == nil {
		t.Fatal("non-metric visualization with a metric reference was accepted")
	}
}

func TestSavedServiceRejectsMismatchedValidationSnapshot(t *testing.T) {
	store := &savedStoreFake{snapshot: savedSnapshot(t, savedDefinition())}
	service := NewSavedAnalyses(store, savedValidator(t), false)
	_, e := service.ValidateSavedAnalysis(context.Background(), "actor", "project-one", "analysis-one", SavedAnalysisValidate{DraftRevision: 2, ExpectedPublishedVersion: 0})
	savedErrorCode(t, e, "SAVED_ANALYSIS_REVISION_CONFLICT")
	_, e = service.ValidateSavedAnalysis(context.Background(), "actor", "project-one", "analysis-one", SavedAnalysisValidate{DraftRevision: 1, ExpectedPublishedVersion: 1})
	savedErrorCode(t, e, "PUBLISHED_VERSION_CONFLICT")
	_, e = service.ValidateSavedAnalysis(context.Background(), "actor", "other-project", "analysis-one", SavedAnalysisValidate{DraftRevision: 1, ExpectedPublishedVersion: 0})
	savedErrorCode(t, e, "INVALID_SAVED_ANALYSIS")
}

func TestSavedDefinitionSizeIncludesDefaultsAtPersistenceBoundary(t *testing.T) {
	definition := SavedAnalysisDefinition{Title: "t", Description: strings.Repeat("d", 1037), Query: analysis.Template{Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: "opened", SchemaVersion: 1}}}, Visualization: SavedAnalysisVisualization{Kind: "metric"}}
	definition.Query.Filter = &analysis.Filter{Op: "and"}
	for i := 0; i < 7; i++ {
		values := make([]any, 18)
		for j := range values {
			values[j] = strings.Repeat("x", 1024)
		}
		definition.Query.Filter.Args = append(definition.Query.Filter.Args, &analysis.Filter{Op: "in", Field: &analysis.Field{Kind: "system", Key: "uid"}, Value: values})
	}
	raw, e := json.Marshal(definition)
	if e != nil {
		t.Fatal(e)
	}
	expanded := definition
	expanded.Query.Metrics = []analysis.Metric{{Op: "count", Accuracy: analysis.AccuracyExact}}
	expanded.Query.Limit = 200
	expected, e := json.Marshal(expanded)
	if e != nil {
		t.Fatal(e)
	}
	if len(raw) > MaxSavedAnalysisDefinitionBytes || len(expected) <= MaxSavedAnalysisDefinitionBytes {
		t.Fatalf("fixture must fit before defaults and exceed after them: input=%d normalized=%d", len(raw), len(expected))
	}
	// Storage invokes normalization independently of the HTTP/schema boundary.
	_, e = NormalizeSavedAnalysisDefinition(definition)
	savedErrorCode(t, e, "BODY_TOO_LARGE")
	_, e = ParseSavedAnalysisDefinition(raw, savedValidator(t))
	savedErrorCode(t, e, "BODY_TOO_LARGE")
	// Accept an exact fully normalized boundary, and reject one additional byte.
	over := len(expected) - MaxSavedAnalysisDefinitionBytes
	definition.Description = strings.Repeat("d", 1037-over)
	normalized, e := NormalizeSavedAnalysisDefinition(definition)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := json.Marshal(normalized)
	if e != nil || len(encoded) != MaxSavedAnalysisDefinitionBytes {
		t.Fatalf("exact normalized boundary changed: bytes=%d err=%v", len(encoded), e)
	}
	definition.Description += "d"
	_, e = NormalizeSavedAnalysisDefinition(definition)
	savedErrorCode(t, e, "BODY_TOO_LARGE")
}
