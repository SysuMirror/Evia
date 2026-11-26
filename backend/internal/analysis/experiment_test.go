package analysis

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestEvaluateExperimentConversionSignificanceAndSRM(t *testing.T) {
	req := Request{
		Kind:        QueryKindExperiment,
		Environment: "development",
		Subject:     SubjectUID,
		Experiment: &Experiment{
			ExposureEvent:  Step{EventKey: "experiment_exposed", SchemaVersion: 1},
			VariantField:   Field{Kind: "property", EventKey: "experiment_exposed", SchemaVersion: 1, Key: "variant", ResolvedType: "string"},
			ControlVariant: "control",
			OutcomeEvent:   Step{EventKey: "post_opened", SchemaVersion: 1},
			MetricMode:     ExperimentMetricModeConversion,
			WindowSeconds:  3600,
			ExpectedSplits: map[string]int{"control": 5000, "treatment": 5000},
		},
	}

	baseTime := int64(1_700_000_000_000_000_000)
	var rows []map[string]any
	// 100 control subjects: 20 convert
	for i := 0; i < 100; i++ {
		uid := fmt.Sprintf("c-%d", i)
		rows = append(rows, map[string]any{
			"subject_id":      uid,
			"event_time_ns":   baseTime,
			"is_exposure":     1,
			"variant_present": 1,
			"variant_value":   "control",
			"is_outcome":      0,
		})
		if i < 20 {
			rows = append(rows, map[string]any{
				"subject_id":    uid,
				"event_time_ns": baseTime + 600_000_000_000,
				"is_exposure":   0,
				"is_outcome":    1,
			})
		}
	}
	// 100 treatment subjects: 40 convert (strongly significant lift +20pp / +100%)
	// Subject t-0 also gets a second exposure to control -> counted as contaminated in treatment
	// Subject t-99 converts AFTER window -> must not count as converted
	for i := 0; i < 100; i++ {
		uid := fmt.Sprintf("t-%d", i)
		rows = append(rows, map[string]any{
			"subject_id":      uid,
			"event_time_ns":   baseTime,
			"is_exposure":     1,
			"variant_present": 1,
			"variant_value":   "treatment",
			"is_outcome":      0,
		})
		if i == 0 {
			rows = append(rows, map[string]any{
				"subject_id":      uid,
				"event_time_ns":   baseTime + 10_000_000_000,
				"is_exposure":     1,
				"variant_present": 1,
				"variant_value":   "control",
				"is_outcome":      0,
			})
		}
		if i < 40 {
			rows = append(rows, map[string]any{
				"subject_id":    uid,
				"event_time_ns": baseTime + 1200_000_000_000,
				"is_exposure":   0,
				"is_outcome":    1,
			})
		}
		if i == 99 {
			rows = append(rows, map[string]any{
				"subject_id":    uid,
				"event_time_ns": baseTime + 4000_000_000_000, // beyond 3600s window
				"is_exposure":   0,
				"is_outcome":    1,
			})
		}
	}

	out, err := EvaluateExperiment(req, rows)
	if err != nil {
		t.Fatalf("EvaluateExperiment failed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 variant rows, got %d", len(out))
	}
	ctrl, trt := out[0], out[1]
	if ctrl["variant"] != "control" || ctrl["is_control"] != true {
		t.Fatalf("expected control first, got %+v", ctrl)
	}
	if ctrl["exposed_subjects"] != 100 || ctrl["converted_subjects"] != 20 {
		t.Fatalf("unexpected control counts: %+v", ctrl)
	}
	if trt["variant"] != "treatment" || trt["exposed_subjects"] != 100 || trt["converted_subjects"] != 40 {
		t.Fatalf("unexpected treatment counts: %+v", trt)
	}
	if trt["contaminated_subjects"] != 1 {
		t.Fatalf("expected 1 contaminated subject in treatment, got %v", trt["contaminated_subjects"])
	}
	if trt["significant"] != true {
		t.Fatalf("expected treatment to be statistically significant, got p=%v", trt["p_value"])
	}
	if pVal := trt["p_value"].(float64); pVal >= 0.005 {
		t.Fatalf("expected p_value < 0.005 for 20%% vs 40%% (n=100), got %v", pVal)
	}
	if lift := trt["lift_absolute"].(float64); math.Abs(lift-0.2) > 1e-6 {
		t.Fatalf("expected lift_absolute=0.2, got %v", lift)
	}
	if srmAlert := trt["srm_alert"].(bool); srmAlert {
		t.Fatalf("expected no SRM alert on 100/100 split, srm_p=%v", trt["srm_p_value"])
	}
}

func TestEvaluateExperimentNumericMeanWelchAndSRMAlert(t *testing.T) {
	req := Request{
		Kind:        QueryKindExperiment,
		Environment: "development",
		Subject:     SubjectUID,
		Experiment: &Experiment{
			ExposureEvent:     Step{EventKey: "experiment_exposed", SchemaVersion: 1},
			VariantField:      Field{Kind: "property", EventKey: "experiment_exposed", SchemaVersion: 1, Key: "variant", ResolvedType: "string"},
			ControlVariant:    "time",
			OutcomeEvent:      Step{EventKey: "post_exited", SchemaVersion: 1},
			MetricMode:        ExperimentMetricModeNumericMean,
			OutcomeValueField: &Field{Kind: "property", EventKey: "post_exited", SchemaVersion: 1, Key: "active_dwell_ms", ResolvedType: "integer"},
			WindowSeconds:     3600,
			ExpectedSplits:    map[string]int{"time": 5000, "recommend": 5000},
		},
	}

	baseTime := int64(1_700_000_000_000_000_000)
	var rows []map[string]any
	// Deliberate SRM: 80 time vs 20 recommend (expected 50/50 -> chi2=36, p << 0.001)
	for i := 0; i < 80; i++ {
		uid := fmt.Sprintf("u-time-%d", i)
		rows = append(rows,
			map[string]any{"subject_id": uid, "event_time_ns": baseTime, "is_exposure": 1, "variant_present": 1, "variant_value": "time", "is_outcome": 0},
			map[string]any{"subject_id": uid, "event_time_ns": baseTime + 100_000_000_000, "is_exposure": 0, "is_outcome": 1, "outcome_value_present": 1, "outcome_value": float64(1000 + (i%5)*50)},
		)
	}
	for i := 0; i < 20; i++ {
		uid := fmt.Sprintf("u-rec-%d", i)
		rows = append(rows,
			map[string]any{"subject_id": uid, "event_time_ns": baseTime, "is_exposure": 1, "variant_present": 1, "variant_value": "recommend", "is_outcome": 0},
			map[string]any{"subject_id": uid, "event_time_ns": baseTime + 100_000_000_000, "is_exposure": 0, "is_outcome": 1, "outcome_value_present": 1, "outcome_value": float64(2500 + (i%5)*50)},
		)
	}

	out, err := EvaluateExperiment(req, rows)
	if err != nil {
		t.Fatalf("EvaluateExperiment numeric_mean failed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(out))
	}
	trt := out[1]
	if trt["variant"] != "recommend" {
		t.Fatalf("expected recommend row, got %+v", trt)
	}
	if trt["srm_alert"] != true {
		t.Fatalf("expected SRM alert on 80/20 split, got srm_p=%v", trt["srm_p_value"])
	}
	if trt["significant"] != true {
		t.Fatalf("expected Welch t-test significant for 1100ms vs 2600ms, got p=%v", trt["p_value"])
	}
	lift := trt["lift_absolute"].(float64)
	if math.Abs(lift-1500) > 1e-3 {
		t.Fatalf("expected lift_absolute ~1500, got %v", lift)
	}
	if ciLow, ciHigh := trt["ci_lower"].(float64), trt["ci_upper"].(float64); !(ciLow > 1400 && ciHigh < 1600 && ciLow < lift && lift < ciHigh) {
		t.Fatalf("unexpected 95%% CI: [%v, %v]", ciLow, ciHigh)
	}
}

func TestExperimentCompileAndValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	req := Request{
		Kind:        QueryKindExperiment,
		Environment: "development",
		Subject:     SubjectUID,
		Timezone:    "UTC",
		TimeRange:   TimeRange{From: now.Add(-14 * 24 * time.Hour), To: now},
		Experiment: &Experiment{
			ExposureEvent:  Step{EventKey: "experiment_exposed", SchemaVersion: 1},
			VariantField:   Field{Kind: "property", EventKey: "experiment_exposed", SchemaVersion: 1, Key: "variant"},
			ControlVariant: "control",
			OutcomeEvent:   Step{EventKey: "post_opened", SchemaVersion: 1},
			MetricMode:     ExperimentMetricModeConversion,
			WindowSeconds:  86400,
		},
	}
	if err := Validate(&req); err != nil {
		t.Fatalf("expected valid experiment request, got: %v", err)
	}
	req.Experiment.VariantField.ResolvedType = "string"
	plan, err := Compile("evia", "event_facts", Scope{ProjectID: "00000000-0000-4000-8000-000000000001", Environment: "development"}, req)
	if err != nil {
		t.Fatalf("Compile experiment failed: %v", err)
	}
	if !strings.Contains(plan.SQL, "is_exposure") || !strings.Contains(plan.SQL, "is_outcome") || !strings.Contains(plan.SQL, "variant_value") {
		t.Fatalf("unexpected experiment SQL: %s", plan.SQL)
	}
	if len(plan.Columns) != 16 {
		t.Fatalf("expected 16 output columns, got %d", len(plan.Columns))
	}
}
