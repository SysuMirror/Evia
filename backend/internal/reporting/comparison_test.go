package reporting

import (
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func utcRange(t *testing.T, from, to string) analysis.TimeRange {
	t.Helper()
	parse := func(value string) time.Time {
		parsed, e := time.Parse(time.RFC3339, value)
		if e != nil {
			t.Fatal(e)
		}
		return parsed
	}
	return analysis.TimeRange{From: parse(from), To: parse(to)}
}

func TestComparisonSpecValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec ComparisonSpec
		want bool
	}{
		{"previous period ignores amount", ComparisonSpec{Unit: ComparisonPreviousPeriod}, true},
		{"one day", ComparisonSpec{Unit: ComparisonDay, Amount: 1}, true},
		{"max amount", ComparisonSpec{Unit: ComparisonMonth, Amount: MaxComparisonAmount}, true},
		{"zero amount", ComparisonSpec{Unit: ComparisonWeek, Amount: 0}, false},
		{"amount over cap", ComparisonSpec{Unit: ComparisonYear, Amount: MaxComparisonAmount + 1}, false},
		{"unknown unit", ComparisonSpec{Unit: "fortnight", Amount: 1}, false},
		{"empty unit", ComparisonSpec{Amount: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Valid(); got != tc.want {
				t.Fatalf("Valid()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestShiftPreviousPeriodMirrorsExactDuration(t *testing.T) {
	runner := utcRange(t, "2026-11-10T00:00:00Z", "2026-11-17T00:00:00Z")
	shifted, e := ShiftTimeRange(runner, "UTC", ComparisonSpec{Unit: ComparisonPreviousPeriod})
	if e != nil {
		t.Fatal(e)
	}
	want := utcRange(t, "2026-11-03T00:00:00Z", "2026-11-10T00:00:00Z")
	if !shifted.From.Equal(want.From) || !shifted.To.Equal(want.To) {
		t.Fatalf("previous period mismatch: %+v want %+v", shifted, want)
	}
}

func TestCalendarShiftsPreserveWallClock(t *testing.T) {
	// Shanghai has no DST: a one-month shift keeps the same local fields.
	runner := utcRange(t, "2026-03-31T16:00:00Z", "2026-04-01T16:00:00Z") // local 2026-04-01 00:00 -> 04-02 00:00
	shifted, e := ShiftTimeRange(runner, "Asia/Shanghai", ComparisonSpec{Unit: ComparisonMonth, Amount: 1})
	if e != nil {
		t.Fatal(e)
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	for _, pair := range [][2]time.Time{
		{runner.From, shifted.From},
		{runner.To, shifted.To},
	} {
		original, moved := pair[0].In(location), pair[1].In(location)
		if original.Hour() != moved.Hour() || original.Minute() != moved.Minute() || moved.Month() != original.Month()-1 {
			t.Fatalf("wall clock not preserved: original=%s moved=%s", original, moved)
		}
	}
}

func TestCalendarShiftSurvivesDSTTransition(t *testing.T) {
	// New York springs forward on 2026-03-08 (02:00 local -> 03:00). Shifting the
	// week after the transition back must keep 09:00 local even though the UTC
	// offset changes from UTC-4 (EDT) to UTC-5 (EST).
	location, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	runner := analysis.TimeRange{
		From: time.Date(2026, 3, 12, 9, 0, 0, 0, location).UTC(),
		To:   time.Date(2026, 3, 13, 9, 0, 0, 0, location).UTC(),
	}
	shifted, e := ShiftTimeRange(runner, "America/New_York", ComparisonSpec{Unit: ComparisonWeek, Amount: 1})
	if e != nil {
		t.Fatal(e)
	}
	movedFrom := shifted.From.In(location)
	if movedFrom.Year() != 2026 || movedFrom.Month() != time.March || movedFrom.Day() != 5 || movedFrom.Hour() != 9 {
		t.Fatalf("expected 2026-03-05 09:00 America/New_York, got %s", movedFrom.Format(time.RFC3339))
	}
	// The fixed-second offset would be 7*24h = 168h; the actual gap is 169h
	// because the comparison window crosses the spring-forward transition.
	gap := runner.From.Sub(shifted.From)
	if gap == 7*24*time.Hour {
		t.Fatal("DST-safe shift must not equal a fixed 168h offset across the transition")
	}
}

func TestNormalizeExecutionRejectsBadComparison(t *testing.T) {
	runner := utcRange(t, "2026-11-10T00:00:00Z", "2026-11-17T00:00:00Z")
	def := DashboardDefinition{
		Title:   "D",
		Widgets: []DashboardWidget{{ID: dashboardWidgetOne, AnalysisID: dashboardAnalysisID, AnalysisVersion: 1, Title: "C", Layout: DashboardLayout{Width: 12, Height: 2}}},
	}
	valid := DashboardExecute{TimeRange: runner, Timezone: "UTC", Comparison: &ComparisonSpec{Unit: ComparisonWeek, Amount: 1}}
	if _, e := NormalizeDashboardExecution(valid, def); e != nil {
		t.Fatalf("valid comparison should normalize: %v", e)
	}
	bad := valid
	bad.Comparison = &ComparisonSpec{Unit: "bogus", Amount: 1}
	if _, e := NormalizeDashboardExecution(bad, def); e == nil {
		t.Fatal("invalid comparison unit must be rejected")
	}
	bad.Comparison = &ComparisonSpec{Unit: ComparisonMonth, Amount: 99}
	if _, e := NormalizeDashboardExecution(bad, def); e == nil {
		t.Fatal("out-of-range comparison amount must be rejected")
	}
}

func TestDashboardRunStateCountsComparisonJobs(t *testing.T) {
	job := func(state string) analysis.Job {
		return analysis.Job{ID: "j", State: state}
	}
	widgets := []DashboardWidgetRun{
		{WidgetID: "a", Query: job("succeeded")},
		{WidgetID: "b", Query: job("succeeded"), Comparison: ptrJob(job("failed"))},
	}
	if got := DashboardRunState(widgets); got != "partial" {
		t.Fatalf("failed comparison must make the run partial, got %s", got)
	}
	widgets[1].Comparison = ptrJob(job("succeeded"))
	if got := DashboardRunState(widgets); got != "succeeded" {
		t.Fatalf("all jobs succeeded should be succeeded, got %s", got)
	}
	widgets[0].Query = job("queued")
	widgets[1].Query = job("queued")
	widgets[1].Comparison = ptrJob(job("queued"))
	if got := DashboardRunState(widgets); got != "queued" {
		t.Fatalf("all queued jobs should be queued, got %s", got)
	}
}

func ptrJob(job analysis.Job) *analysis.Job { return &job }

func TestShiftRejectsInvalidInput(t *testing.T) {
	runner := utcRange(t, "2026-11-10T00:00:00Z", "2026-11-17T00:00:00Z")
	if _, e := ShiftTimeRange(runner, "UTC", ComparisonSpec{Unit: "bogus", Amount: 1}); e == nil {
		t.Fatal("unknown unit must be rejected")
	}
	if _, e := ShiftTimeRange(runner, "Not/AZone", ComparisonSpec{Unit: ComparisonDay, Amount: 1}); e == nil {
		t.Fatal("invalid timezone must be rejected")
	}
	backwards := analysis.TimeRange{From: runner.To, To: runner.From}
	if _, e := ShiftTimeRange(backwards, "UTC", ComparisonSpec{Unit: ComparisonPreviousPeriod}); e == nil {
		t.Fatal("inverted range cannot derive a previous period")
	}
}
