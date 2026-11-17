package reporting

import (
	"strings"
	"testing"
)

func TestNormalizeDashboardSearch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty stays empty", input: "", want: ""},
		{name: "whitespace only disables filter", input: "   ", want: ""},
		{name: "trims surrounding spaces", input: "  留存看板  ", want: "留存看板"},
		{name: "keeps inner spaces", input: "daily active", want: "daily active"},
		{name: "accepts boundary length", input: strings.Repeat("字", 128), want: strings.Repeat("字", 128)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, e := NormalizeDashboardSearch(tc.input)
			if e != nil {
				t.Fatalf("unexpected error: %v", e)
			}
			if got != tc.want {
				t.Fatalf("normalized term mismatch: got %q want %q", got, tc.want)
			}
		})
	}
	if _, e := NormalizeDashboardSearch(strings.Repeat("字", 129)); e == nil {
		t.Fatal("search term over 128 runes must be rejected")
	}
}

func TestEscapeLikePatternTreatsMetaCharactersAsLiterals(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{input: "plain", want: "plain"},
		{input: "100%match", want: `100\%match`},
		{input: "under_score", want: `under\_score`},
		{input: `back\slash`, want: `back\\slash`},
		{input: `%_\%`, want: `\%\_\\\%`},
	} {
		if got := EscapeLikePattern(tc.input); got != tc.want {
			t.Fatalf("escape(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
