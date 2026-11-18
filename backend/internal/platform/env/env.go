// Package env is the single Go-side source of truth for the deployment
// environment closed set (development/staging/production). The same three
// values appear in roughly forty OpenAPI schemas and in the analysis_query_jobs
// style table constraints; every production validation must call Valid rather
// than restating the literals.
package env

const (
	Development = "development"
	Staging     = "staging"
	Production  = "production"
)

// Environments is the closed set in canonical order.
var Environments = []string{Development, Staging, Production}

// Valid reports whether value is one of the three deployment environments.
func Valid(value string) bool {
	for _, candidate := range Environments {
		if value == candidate {
			return true
		}
	}
	return false
}
