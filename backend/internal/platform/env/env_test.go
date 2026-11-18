package env

import "testing"

func TestValidAcceptsOnlyClosedSet(t *testing.T) {
	for _, want := range Environments {
		if !Valid(want) {
			t.Fatalf("environment %q must be valid", want)
		}
	}
	for _, reject := range []string{"", "prod", "Production", "DEVELOPMENT", "qa", "staging "} {
		if Valid(reject) {
			t.Fatalf("environment %q must be rejected", reject)
		}
	}
}
