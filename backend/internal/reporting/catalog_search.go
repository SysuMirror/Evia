package reporting

import (
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// maxCatalogSearchRunes bounds every catalog title search term; the managed
// resource titles (saved analyses, metrics and dashboards) are themselves
// capped at 128 runes, so a longer term can never match.
const maxCatalogSearchRunes = 128

// NormalizeCatalogSearch trims an optional title search term and bounds its
// length. An empty result disables the filter. The returned literal still
// contains the user's LIKE meta-characters; the store must pass it through
// EscapeLikePattern before wrapping it in a case-insensitive substring match.
func NormalizeCatalogSearch(term string) (string, error) {
	term = strings.TrimSpace(term)
	if utf8.RuneCountInString(term) > maxCatalogSearchRunes {
		return "", analysis.Invalid("INVALID_QUERY", "搜索关键词不能超过 128 个字符", "/q")
	}
	return term, nil
}

// EscapeLikePattern escapes the LIKE meta-characters so user input matches as
// a literal substring rather than a pattern. The paired SQL predicate must
// declare ESCAPE '\' and ILIKE the wrapped '%' || term || '%' expression.
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
