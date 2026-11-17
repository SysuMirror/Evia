package reporting

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// MaxTextCardContentRunes bounds the Markdown payload of a text card widget.
// The whole dashboard definition still has its own 64 KiB envelope.
const MaxTextCardContentRunes = 4096

// TextCard is a presentation-only widget: it carries a bounded Markdown subset
// and never pins an executable source. It produces no child query at run time.
type TextCard struct {
	Content string `json:"content"`
}

var (
	// Any HTML tag, comment or doctype is rejected outright so the console
	// renderer never has to sanitize raw markup.
	textCardHTMLPattern = regexp.MustCompile(`(?i)</?[a-z!]`)
	// Images are excluded from the v1 subset; text cards are prose, not media.
	textCardImagePattern = regexp.MustCompile(`!\[`)
	// Dangerous URL schemes in any casing. http/https links are validated
	// separately through textCardLinkPattern.
	textCardBlockedScheme = regexp.MustCompile(`(?i)(javascript|vbscript|data)\s*:`)
	textCardFence         = "```"
	textCardLinkPattern   = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	textCardSafeURL       = regexp.MustCompile(`^https?://[^\s<>"]+$`)
)

// ValidateTextCardMarkdown enforces the fail-closed Markdown subset rendered by
// the console text card: no raw HTML/comments, no images, no non-http(s) link
// targets and balanced fenced code blocks. The check is intentionally
// conservative — anything not provably safe is rejected at draft admission.
func ValidateTextCardMarkdown(content string) error {
	length := utf8.RuneCountInString(content)
	if length == 0 || length > MaxTextCardContentRunes {
		return analysis.Invalid("INVALID_DASHBOARD_TEXT_CARD", "文本卡内容需为 1 至 4096 个字符", "/widgets")
	}
	if textCardHTMLPattern.MatchString(content) {
		return analysis.Invalid("INVALID_DASHBOARD_TEXT_CARD", "文本卡不允许原始 HTML 或注释", "/widgets")
	}
	if textCardImagePattern.MatchString(content) {
		return analysis.Invalid("INVALID_DASHBOARD_TEXT_CARD", "文本卡 v1 不支持图片语法", "/widgets")
	}
	if textCardBlockedScheme.MatchString(content) {
		return analysis.Invalid("INVALID_DASHBOARD_TEXT_CARD", "文本卡包含不允许的链接协议", "/widgets")
	}
	if strings.Count(content, textCardFence)%2 != 0 {
		return analysis.Invalid("INVALID_DASHBOARD_TEXT_CARD", "文本卡代码块必须闭合", "/widgets")
	}
	for _, match := range textCardLinkPattern.FindAllStringSubmatch(content, -1) {
		target := strings.TrimSpace(match[1])
		if !textCardSafeURL.MatchString(target) {
			return analysis.Invalid("INVALID_DASHBOARD_TEXT_CARD", "文本卡只允许 http/https 链接", "/widgets")
		}
	}
	return nil
}
