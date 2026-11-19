package reporting

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestDiscoveryBudgetsMatchEnforcementAndContract keeps the advertised
// capabilities aligned with (1) the analysis package enforcement constants
// that ValidateConfiguration and query admission actually apply, and (2) the
// numeric minimum/maximum bounds published by the embedded contract. The
// contract only carries JSON safe-integer bounds, so layer 1 is the real
// product-limit drift guard while layer 2 catches overflow or negative
// budgets that would violate the published schema.
func TestDiscoveryBudgetsMatchEnforcementAndContract(t *testing.T) {
	discovery := discoveryForTest(t, &discoveryStoreFake{})
	capabilities, err := discovery.Capabilities(context.Background(), "actor", "project")
	if err != nil {
		t.Fatal(err)
	}
	budgets := capabilities.Budgets
	want := DiscoveryBudgets{
		TrendWindowSeconds:        analysis.BudgetTrendWindowSeconds,
		OtherWindowSeconds:        analysis.BudgetOtherWindowSeconds,
		EventSelections:           analysis.BudgetEventSelections,
		GroupFields:               analysis.BudgetGroupFields,
		Metrics:                   analysis.BudgetMetrics,
		ResultRows:                analysis.BudgetResultRows,
		FilterDepth:               analysis.BudgetFilterDepth,
		FilterNodes:               analysis.BudgetFilterNodes,
		FilterArguments:           analysis.BudgetFilterArguments,
		FilterValues:              analysis.BudgetFilterValues,
		FunnelSteps:               analysis.BudgetFunnelSteps,
		FunnelWindowSeconds:       analysis.BudgetFunnelWindowSeconds,
		ProjectRunningQueries:     analysis.BudgetProjectRunningQueries,
		ProjectQueuedQueries:      analysis.BudgetProjectQueuedQueries,
		RetentionMaxPeriods:       analysis.BudgetRetentionMaxPeriods,
		RetentionMaxWindowSeconds: analysis.BudgetRetentionMaxWindowSeconds,
	}
	if budgets != want {
		t.Fatalf("discovery budget drift: advertised %+v != enforcement constants %+v", budgets, want)
	}

	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	contractBudgets, ok := bundle.Defs["AnalysisCapabilities"]["properties"].(map[string]any)["budgets"].(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatal("AnalysisCapabilities.budgets schema missing")
	}
	advertised := map[string]int{
		"trend_window_seconds":         budgets.TrendWindowSeconds,
		"other_window_seconds":         budgets.OtherWindowSeconds,
		"event_selections":             budgets.EventSelections,
		"group_fields":                 budgets.GroupFields,
		"metrics":                      budgets.Metrics,
		"result_rows":                  budgets.ResultRows,
		"filter_depth":                 budgets.FilterDepth,
		"filter_nodes":                 budgets.FilterNodes,
		"filter_arguments":             budgets.FilterArguments,
		"filter_values":                budgets.FilterValues,
		"funnel_steps":                 budgets.FunnelSteps,
		"funnel_window_seconds":        budgets.FunnelWindowSeconds,
		"project_running_queries":      budgets.ProjectRunningQueries,
		"project_queued_queries":       budgets.ProjectQueuedQueries,
		"retention_max_periods":        budgets.RetentionMaxPeriods,
		"retention_max_window_seconds": budgets.RetentionMaxWindowSeconds,
	}
	if len(advertised) != len(contractBudgets) {
		t.Fatalf("budget coverage drift: Go advertises %d budgets, contract defines %d", len(advertised), len(contractBudgets))
	}
	for name, value := range advertised {
		node, ok := contractBudgets[name].(map[string]any)
		if !ok {
			t.Fatalf("contract budget %s missing", name)
		}
		minimum, _ := node["minimum"].(float64)
		maximum, _ := node["maximum"].(float64)
		if float64(value) < minimum || float64(value) > maximum {
			t.Fatalf("budget %s=%d outside contract bounds [%v,%v]", name, value, minimum, maximum)
		}
	}
}
