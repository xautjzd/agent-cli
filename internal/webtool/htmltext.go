// Package webtool provides the agent's web tools — web_search (find current
// information) and web_fetch (read a page) — so it can look up the latest
// documentation, API references, and error explanations on its own.
package webtool

import (
	"html"
	"regexp"
	"strings"
)

// HTML→text extraction good enough to feed a page to an LLM: strip the
// non-content elements, turn block boundaries into newlines, drop the
// remaining tags, decode entities, and collapse whitespace. It is regex-based
// (RE2, no backreferences) rather than a full parser — imperfect, but robust
// and dependency-free for the "give me the readable text" use case.
var (
	reTitle  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reStyle  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	reNo     = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	reHead   = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`)
	reSvg    = regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`)
	reBlock  = regexp.MustCompile(`(?i)</?(br|p|div|li|ul|ol|tr|table|h[1-6]|section|article|header|footer|nav|blockquote|pre)\b[^>]*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces = regexp.MustCompile(`[ \t\f\v]{2,}`)
	reBlanks = regexp.MustCompile(`\n{3,}`)
)

// htmlToText extracts the page title and readable text from HTML.
func htmlToText(h string) (title, text string) {
	if m := reTitle.FindStringSubmatch(h); m != nil {
		title = normalizeInline(m[1])
	}
	for _, re := range []*regexp.Regexp{reScript, reStyle, reNo, reHead, reSvg} {
		h = re.ReplaceAllString(h, " ")
	}
	h = reBlock.ReplaceAllString(h, "\n")
	h = reTag.ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	h = strings.ReplaceAll(h, "\u00a0", " ") // non-breaking space \u2192 regular space

	lines := strings.Split(h, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	text = reBlanks.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return title, strings.TrimSpace(text)
}

// Markdown conversion, matching how Claude Code / opencode surface a page:
// links, headings, lists, and code survive as Markdown so the model keeps the
// structure (a heading it can cite, a URL it can follow) instead of a flat text
// blob. Still regex-based and imperfect, but far more useful than plain text.
var (
	reHeading = regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>(.*?)</h[1-6]>`)
	reLink    = regexp.MustCompile(`(?is)<a\b[^>]*\bhref="([^"]+)"[^>]*>(.*?)</a>`)
	reBold    = regexp.MustCompile(`(?is)</?(strong|b)\b[^>]*>`)
	reItalic  = regexp.MustCompile(`(?is)</?(em|i)\b[^>]*>`)
	rePre     = regexp.MustCompile(`(?is)</?pre\b[^>]*>`)
	reCode    = regexp.MustCompile(`(?is)</?code\b[^>]*>`)
	reLi      = regexp.MustCompile(`(?i)<li\b[^>]*>`)
)

// htmlToMarkdown extracts the title and a Markdown rendering of the page body.
func htmlToMarkdown(h string) (title, md string) {
	if m := reTitle.FindStringSubmatch(h); m != nil {
		title = normalizeInline(m[1])
	}
	for _, re := range []*regexp.Regexp{reScript, reStyle, reNo, reHead, reSvg} {
		h = re.ReplaceAllString(h, " ")
	}
	// Headings → "## text" (the inner text is later stripped of any tags).
	h = reHeading.ReplaceAllStringFunc(h, func(s string) string {
		m := reHeading.FindStringSubmatch(s)
		level := int(m[1][0] - '0') // "1".."6"
		return "\n\n" + strings.Repeat("#", level) + " " + normalizeInline(m[2]) + "\n"
	})
	// Links → [text](url).
	h = reLink.ReplaceAllStringFunc(h, func(s string) string {
		m := reLink.FindStringSubmatch(s)
		text := normalizeInline(m[2])
		if text == "" {
			text = m[1]
		}
		return "[" + text + "](" + m[1] + ")"
	})
	h = reBold.ReplaceAllString(h, "**")
	h = reItalic.ReplaceAllString(h, "*")
	h = rePre.ReplaceAllString(h, "\n```\n")
	h = reCode.ReplaceAllString(h, "`")
	h = reLi.ReplaceAllString(h, "\n- ")
	h = reBlock.ReplaceAllString(h, "\n")
	h = reTag.ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	h = strings.ReplaceAll(h, "\u00a0", " ")

	lines := strings.Split(h, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(reSpaces.ReplaceAllString(l, " "), " ")
	}
	md = reBlanks.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return title, strings.TrimSpace(md)
}

// normalizeInline strips tags and entities from a short inline fragment (a
// title or snippet) and collapses whitespace to a single line.
func normalizeInline(s string) string {
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reSpaces.ReplaceAllString(strings.Join(strings.Fields(s), " "), " "))
}

// looksLikeHTML reports whether a body is HTML (so plain text/JSON responses
// are returned verbatim rather than stripped).
func looksLikeHTML(contentType, body string) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := strings.ToLower(body)
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html")
}
