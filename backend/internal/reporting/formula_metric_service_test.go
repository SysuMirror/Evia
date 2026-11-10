package reporting

import (
	"context"
	"strings"
	"testing"
)

type fakeFormulaMetricStore struct {
	created   FormulaMetricCreate
	savedDef  FormulaMetricDefinition
	snapshot  FormulaMetricSnapshot
	listLimit int
}

func (f *fakeFormulaMetricStore) CreateFormulaMetric(_ context.Context, _, _, _ string, in FormulaMetricCreate) (FormulaMetricDraft, error) {
	f.created = in
	return FormulaMetricDraft{FormulaMetricRecord: FormulaMetricRecord{ID: "formula-one", ProjectID: "project-one", Environment: in.Environment, DraftRevision: 1}}, nil
}
func (f *fakeFormulaMetricStore) ListFormulaMetrics(_ context.Context, _, _, _, _ string, limit int) ([]FormulaMetricRecord, error) {
	f.listLimit = limit
	return []FormulaMetricRecord{}, nil
}
func (f *fakeFormulaMetricStore) GetFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error) {
	return FormulaMetricRecord{}, nil
}
func (f *fakeFormulaMetricStore) GetFormulaMetricDraft(context.Context, string, string, string) (FormulaMetricDraft, error) {
	return FormulaMetricDraft{}, nil
}
func (f *fakeFormulaMetricStore) SaveFormulaMetricDraft(_ context.Context, _, _, _ string, _ int64, in FormulaMetricDefinition) (FormulaMetricDraft, error) {
	f.savedDef = in
	return FormulaMetricDraft{}, nil
}
func (f *fakeFormulaMetricStore) FormulaMetricValidationSnapshot(context.Context, string, string, string, FormulaMetricValidate) (FormulaMetricSnapshot, error) {
	return f.snapshot, nil
}
func (f *fakeFormulaMetricStore) PublishFormulaMetric(context.Context, string, string, string, string, FormulaMetricPublish) (FormulaMetricVersion, error) {
	return FormulaMetricVersion{}, nil
}
func (f *fakeFormulaMetricStore) ListFormulaMetricVersions(context.Context, string, string, string, int64, int) ([]FormulaMetricVersion, error) {
	return nil, nil
}
func (f *fakeFormulaMetricStore) GetFormulaMetricVersion(context.Context, string, string, string, int64) (FormulaMetricVersion, error) {
	return FormulaMetricVersion{}, nil
}

func formulaServiceDef() FormulaMetricDefinition {
	id := "11111111-1111-4111-8111-111111111111"
	ref := MetricReference{MetricID: id, Version: 1}
	expr := FormulaExpression{Op: FormulaMetric, Metric: &ref}
	return FormulaMetricDefinition{Title: "Formula one", Description: "single operand", Formula: FormulaDefinition{Expression: expr, Unit: "event"}}
}

func TestFormulaMetricServiceCreateDelegatesNormalized(t *testing.T) {
	store := &fakeFormulaMetricStore{}
	svc := NewFormulaMetrics(store, savedValidator(t))
	in := FormulaMetricCreate{Environment: "development", Definition: formulaServiceDef()}
	out, e := svc.CreateFormulaMetric(context.Background(), "actor", "project-one", "create-key", in)
	if e != nil {
		t.Fatal(e)
	}
	if out.ID != "formula-one" || store.created.Definition.Title != "Formula one" {
		t.Fatalf("delegation %+v", out)
	}
}

func TestFormulaMetricServiceCreateRejectsBadInput(t *testing.T) {
	svc := NewFormulaMetrics(&fakeFormulaMetricStore{}, savedValidator(t))
	ctx := context.Background()
	if _, e := svc.CreateFormulaMetric(ctx, "actor", "project-one", "", FormulaMetricCreate{Environment: "development", Definition: formulaServiceDef()}); e == nil {
		t.Fatal("expected idempotency key rejection")
	}
	if _, e := svc.CreateFormulaMetric(ctx, "actor", "project-one", "key", FormulaMetricCreate{Environment: "qa", Definition: formulaServiceDef()}); e == nil {
		t.Fatal("expected environment rejection")
	}
	bad := formulaServiceDef()
	bad.Formula.Expression.Op = "pow"
	if _, e := svc.CreateFormulaMetric(ctx, "actor", "project-one", "key", FormulaMetricCreate{Environment: "development", Definition: bad}); e == nil {
		t.Fatal("expected contract rejection for unknown op")
	}
}

func TestFormulaMetricServiceSaveRejectsRevisionAndContract(t *testing.T) {
	svc := NewFormulaMetrics(&fakeFormulaMetricStore{}, savedValidator(t))
	ctx := context.Background()
	if _, e := svc.SaveFormulaMetricDraft(ctx, "actor", "project-one", "formula-one", 0, formulaServiceDef()); e == nil {
		t.Fatal("expected revision rejection")
	}
	bad := formulaServiceDef()
	bad.Title = ""
	if _, e := svc.SaveFormulaMetricDraft(ctx, "actor", "project-one", "formula-one", 1, bad); e == nil {
		t.Fatal("expected empty title rejection")
	}
}

func TestFormulaMetricServiceListRejectsLimit(t *testing.T) {
	svc := NewFormulaMetrics(&fakeFormulaMetricStore{}, savedValidator(t))
	if _, e := svc.ListFormulaMetrics(context.Background(), "actor", "project-one", "development", "", 0); e == nil {
		t.Fatal("expected limit rejection")
	}
	if _, e := svc.ListFormulaMetrics(context.Background(), "actor", "project-one", "nope", "", 10); e == nil {
		t.Fatal("expected environment rejection")
	}
}

func TestFormulaMetricServiceValidateBuilds(t *testing.T) {
	def := formulaServiceDef()
	ref := *def.Formula.Expression.Metric
	key := formulaRefKey(ref)
	store := &fakeFormulaMetricStore{
		snapshot: FormulaMetricSnapshot{
			Draft: FormulaMetricDraft{
				FormulaMetricRecord: FormulaMetricRecord{ID: "formula-one", ProjectID: "project-one", Environment: "development", DraftRevision: 1, PublishedVersion: 0},
				Definition:          def,
			},
			Graph: FormulaGraph{
				Direct:       []MetricReference{ref},
				AtomicLeaves: []MetricReference{ref},
				Units:        map[string]string{key: "event"},
				resolved: map[string]FormulaResolvedMetric{
					key: {Ref: ref, Unit: "event", DefinitionHash: strings.Repeat("a", 64)},
				},
			},
		},
	}
	svc := NewFormulaMetrics(store, savedValidator(t))
	validation, e := svc.ValidateFormulaMetric(context.Background(), "actor", "project-one", "formula-one", FormulaMetricValidate{DraftRevision: 1, ExpectedPublishedVersion: 0})
	if e != nil {
		t.Fatal(e)
	}
	if !validation.Valid || validation.ExecutionPerformed || len(validation.Dependencies.Metrics) != 1 {
		t.Fatalf("validation %+v", validation)
	}
}

func TestFormulaMetricServicePublishRejectsBadHash(t *testing.T) {
	svc := NewFormulaMetrics(&fakeFormulaMetricStore{}, savedValidator(t))
	in := FormulaMetricPublish{DraftRevision: 1, ExpectedPublishedVersion: 0, ValidationHash: "nope"}
	if _, e := svc.PublishFormulaMetric(context.Background(), "actor", "project-one", "formula-one", "publish-key", in); e == nil {
		t.Fatal("expected validation hash rejection")
	}
}
