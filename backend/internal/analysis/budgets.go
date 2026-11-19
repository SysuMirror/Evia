package analysis

// Query budget single source of truth. These constants are the enforcement
// limits applied by ValidateConfiguration and the query admission path; the
// reporting discovery capabilities advertise the same values to clients.
// Keeping one named constant per budget prevents the advertised capabilities
// from drifting away from the actual validation behavior. Guard:
// reporting.TestDiscoveryBudgetsMatchEnforcementAndContract.
const (
	BudgetTrendWindowSeconds    = 31 * 24 * 60 * 60
	BudgetOtherWindowSeconds    = 7 * 24 * 60 * 60
	BudgetEventSelections       = 20
	BudgetGroupFields           = 3
	BudgetMetrics               = 10
	BudgetResultRows            = 500
	BudgetFilterDepth           = 8
	BudgetFilterNodes           = 100
	BudgetFilterArguments       = 20
	BudgetFilterValues          = 20
	BudgetFunnelSteps           = 8
	BudgetFunnelWindowSeconds   = 604800
	BudgetProjectRunningQueries = 4
	BudgetProjectQueuedQueries  = 32
	BudgetRetentionMaxPeriods   = 12
	// BudgetRetentionMaxWindowSeconds is the conservative 31-day-per-period
	// upper bound: max periods (12) times 31 days, matching retentionWindow.
	BudgetRetentionMaxWindowSeconds = BudgetRetentionMaxPeriods * 31 * 24 * 60 * 60
)
