package reporting

import (
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// maxDashboardSearchRunes bounds the title search term; dashboard titles are
// themselves capped at 128 runes, so a longer term can never match.
const maxDashboardSearchRunes = 128

// NormalizeDashboardSearch trims the optional title search term and bounds its
// length. An empty result disables the filter. The returned literal still
// contains the user's LIKE meta-characters; the store must pass it through
// EscapeLikePattern before wrapping it in a case-insensitive substring match.
func NormalizeDashboardSearch(term string) (string, error) {
	term = strings.TrimSpace(term)
	if utf8.RuneCountInString(term) > maxDashboardSearchRunes {
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
