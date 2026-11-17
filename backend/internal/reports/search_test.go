package reports

import (
	"strings"
	"testing"
)

func TestNormalizeTitleSearch(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"trims surrounding whitespace", "  周报  ", "周报"},
		{"blank becomes empty", "   ", ""},
		{"empty stays empty", "", ""},
		{"keeps inner whitespace", "a\tb  c", "a\tb  c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeTitleSearch(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeTitleSearch(%q) = %q, %v", tc.in, got, err)
			}
		})
	}
	atLimit := strings.Repeat("字", MaxTitleLength)
	if got, err := NormalizeTitleSearch(atLimit); err != nil || got != atLimit {
		t.Fatalf("boundary term rejected: %q %v", got, err)
	}
	if _, err := NormalizeTitleSearch(strings.Repeat("字", MaxTitleLength+1)); err == nil {
		t.Fatal("overlong term must be rejected")
	}
	emoji := strings.Repeat("😀", MaxTitleLength+1)
	if _, err := NormalizeTitleSearch(emoji); err == nil {
		t.Fatal("overlong emoji term must be rejected by rune count, not byte length")
	}
}

func TestEscapeLikePatternTreatsMetaCharactersAsLiterals(t *testing.T) {
	cases := map[string]string{
		"plain":      "plain",
		"100%match":  `100\%match`,
		"a_b":        `a\_b`,
		`back\slash`: `back\\slash`,
		`a%_\b`:      `a\%\_\\b`,
	}
	for in, want := range cases {
		if got := EscapeLikePattern(in); got != want {
			t.Fatalf("EscapeLikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}
