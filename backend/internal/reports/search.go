package reports

import (
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// NormalizeTitleSearch trims an optional title search term and bounds its
// length to the same rune limit as a report title, so a term longer than any
// stored title is rejected rather than silently matching nothing. An empty
// result disables the filter. The package stays independent of the reporting
// bounded context, hence the local helper rather than a cross-package import.
func NormalizeTitleSearch(term string) (string, error) {
	term = trimSpace(term)
	if utf8.RuneCountInString(term) > MaxTitleLength {
		return "", fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "搜索关键词不能超过 128 个字符")
	}
	return term, nil
}

// EscapeLikePattern escapes the LIKE meta-characters so user input matches as
// a literal substring rather than a pattern. The paired SQL predicate must
// declare ESCAPE '\' and wrap the term in '%' || term || '%'.
func EscapeLikePattern(term string) string {
	var builder strings.Builder
	builder.Grow(len(term))
	for _, r := range term {
		switch r {
		case '\\', '%', '_':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
