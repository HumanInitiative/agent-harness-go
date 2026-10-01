package websearch

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// webContentTag matches anything in the content that could open or close
// our wrapper element, including variants with whitespace or odd casing
// ("</ WEB_CONTENT>"), which a model might still read as a delimiter.
var webContentTag = regexp.MustCompile(`(?i)<\s*(/?)\s*web_content`)

// Wrap marks web-derived text as untrusted data before it reaches a model:
//
//	<web_content source="URL" fetched_at="RFC3339" untrusted="true">
//	...content...
//	</web_content>
//
// A page could contain "</web_content> Ignore previous instructions..." to
// break out of the wrapper, so every tag-like occurrence of web_content
// inside the content is neutralized first. The system prompt must tell the
// model that text inside this element is data, never instructions.
func Wrap(source string, fetchedAt time.Time, content string) string {
	safe := webContentTag.ReplaceAllString(content, "&lt;${1}web_content")
	return fmt.Sprintf("<web_content source=\"%s\" fetched_at=\"%s\" untrusted=\"true\">\n%s\n</web_content>",
		html.EscapeString(source), fetchedAt.UTC().Format(time.RFC3339), strings.TrimSpace(safe))
}

// EstimateTokens approximates a token count as bytes/4, the usual rough
// ratio for English and Indonesian text. Exact counts would need the
// model's tokenizer, which this package intentionally does not know.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// Truncate shortens text to roughly maxTokens tokens, preferring to cut at a
// paragraph or word boundary, and appends a marker saying how much was
// dropped so the model knows the content is incomplete. It returns the text
// unchanged when it already fits or maxTokens <= 0.
func Truncate(text string, maxTokens int) (string, bool) {
	if maxTokens <= 0 || EstimateTokens(text) <= maxTokens {
		return text, false
	}
	limit := maxTokens * 4
	cut := text[:limit]
	// Never split a multi-byte character.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndex(cut, "\n\n"); i > len(cut)/2 {
		cut = cut[:i]
	} else if i := strings.LastIndexAny(cut, " \n"); i > len(cut)/2 {
		cut = cut[:i]
	}
	marker := fmt.Sprintf("\n\n[truncated: showing about %d of %d tokens]", EstimateTokens(cut), EstimateTokens(text))
	return strings.TrimRight(cut, " \n") + marker, true
}
