package mail

import (
	"html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// cidPattern matches an inline reference to a part of the same message. Only
// these image sources are kept.
var cidPattern = regexp.MustCompile(`^cid:[^\s"'<>]+$`)

// htmlPolicy is an allowlist: unknown elements and attributes are removed,
// and scripts, styles, forms, frames and event handlers never pass. Remote
// resources are blocked: an <img> keeps its src only when it is a cid: URL, so
// mail cannot load trackers or remote images. Link schemes are limited to
// http, https and mailto. Built once; bluemonday policies are safe for
// concurrent use.
var htmlPolicy = newHTMLPolicy()

func newHTMLPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowStandardAttributes()
	p.AllowLists()
	p.AllowTables()
	p.AllowElements(
		"p", "br", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6",
		"b", "i", "u", "em", "strong", "s", "del", "ins", "mark", "small",
		"sub", "sup", "blockquote", "pre", "code", "hr", "address", "center",
	)

	// cid is listed so that cid: image sources pass the scheme check; img src
	// is further restricted to cidPattern below, so remote http(s) images are
	// still removed.
	p.AllowURLSchemes("http", "https", "mailto", "cid")
	p.RequireParseableURLs(true)
	p.AllowAttrs("href").OnElements("a")
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	p.AllowAttrs("src").Matching(cidPattern).OnElements("img")
	p.AllowAttrs("alt", "title").OnElements("img")
	p.AllowAttrs("width", "height").Matching(bluemonday.Integer).OnElements("img", "table", "td", "th")
	p.AllowAttrs("colspan", "rowspan").Matching(bluemonday.Integer).OnElements("td", "th")
	return p
}

// sanitizeHTML returns display-safe HTML for an untrusted message body.
func sanitizeHTML(raw string) string {
	if raw == "" {
		return ""
	}
	return htmlPolicy.Sanitize(raw)
}

var (
	textBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|tr|h[1-6])\s*>`)
	strictPolicy     = bluemonday.StrictPolicy()
)

// htmlToText derives plain text from HTML for messages that have no text/plain
// part. Tags, scripts and styles are dropped and block ends become newlines.
func htmlToText(raw string) string {
	withBreaks := textBreakPattern.ReplaceAllString(raw, "\n")
	text := html.UnescapeString(strictPolicy.Sanitize(withBreaks))
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
