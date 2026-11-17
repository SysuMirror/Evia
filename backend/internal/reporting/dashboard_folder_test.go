package reporting

import (
	"strings"
	"testing"
)

func TestNormalizeDashboardFolderCreate(t *testing.T) {
	root, e := NormalizeDashboardFolderCreate(DashboardFolderCreate{
		Environment: "development",
		Title:       "  Forum  ",
	})
	if e != nil {
		t.Fatalf("root folder should normalize: %v", e)
	}
	if root.Title != "Forum" || root.ParentID != "" {
		t.Fatalf("unexpected root normalization: %+v", root)
	}
	child, e := NormalizeDashboardFolderCreate(DashboardFolderCreate{
		Environment: "staging",
		ParentID:    dashboardMetricID,
		Title:       "Readers",
	})
	if e != nil || child.ParentID != dashboardMetricID {
		t.Fatalf("child folder should normalize: %+v %v", child, e)
	}
}

func TestNormalizeDashboardFolderCreateRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   DashboardFolderCreate
	}{
		{"bad environment", DashboardFolderCreate{Environment: "prod", Title: "X"}},
		{"empty title", DashboardFolderCreate{Environment: "development", Title: "   "}},
		{"oversized title", DashboardFolderCreate{Environment: "development", Title: strings.Repeat("中", 129)}},
		{"bad parent id", DashboardFolderCreate{Environment: "development", ParentID: "not-a-uuid", Title: "X"}},
		{"nul in title", DashboardFolderCreate{Environment: "development", Title: "bad\x00name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := NormalizeDashboardFolderCreate(tc.in); e == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestNormalizeDashboardFolderUpdate(t *testing.T) {
	out, e := NormalizeDashboardFolderUpdate(DashboardFolderUpdate{Title: "  Renamed "})
	if e != nil || out.Title != "Renamed" {
		t.Fatalf("update should trim title: %+v %v", out, e)
	}
	if _, e = NormalizeDashboardFolderUpdate(DashboardFolderUpdate{Title: ""}); e == nil {
		t.Fatal("empty rename must be rejected")
	}
}

func TestNormalizeDashboardFolderScope(t *testing.T) {
	for _, scope := range []string{"", "root", dashboardMetricID} {
		if _, e := NormalizeDashboardFolderScope(scope); e != nil {
			t.Fatalf("scope %q should be accepted", scope)
		}
	}
	for _, scope := range []string{"bogus", "ROOT", "not-a-uuid"} {
		if _, e := NormalizeDashboardFolderScope(scope); e == nil {
			t.Fatalf("scope %q should be rejected", scope)
		}
	}
}
