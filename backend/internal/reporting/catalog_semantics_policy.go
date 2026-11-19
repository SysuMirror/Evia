package reporting

// Analysis catalog field semantic markers. Both vocabularies are single-value
// enums today; they are named so the discovery builder and the contract guard
// TestCatalogFieldSemanticsVocabularyContract share one source of truth before
// any additional semantic is introduced.
const (
	CatalogMissingSemanticsAbsentKey             = "absent_key"
	CatalogNullSemanticsPresentButNotATypedValue = "present_but_not_a_typed_value"
)

// CatalogMissingSemantics and CatalogNullSemantics mirror the single-value
// AnalysisCatalogField enums in the published contract.
var (
	CatalogMissingSemantics = []string{CatalogMissingSemanticsAbsentKey}
	CatalogNullSemantics    = []string{CatalogNullSemanticsPresentButNotATypedValue}
)
