//go:build !sqliteonly

package element

import (
	"html"
	"regexp"
	"strings"
)

// Minimal markdown → Matrix-safe HTML converter.
// Matrix custom HTML format accepts a wide tag whitelist; we emit a small
// subset (b, i, code, pre, a, br, ul, li) that covers typical agent output.
// Anything not recognized is HTML-escaped — no raw user input reaches the DOM.

var (
	reFence  = regexp.MustCompile("(?s)```([a-zA-Z0-9_-]*)\\n(.*?)```")
	reInline = regexp.MustCompile("`([^`]+)`")
	reBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic = regexp.MustCompile(`(?:^|[^*])\*([^*\n]+)\*`)
	reLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// markdownToHTML converts a markdown chunk to a Matrix-friendly HTML fragment.
// Order matters: code blocks first (their contents must not be re-processed),
// then inline code, then bold/italic/links, then line breaks.
func markdownToHTML(md string) string {
	if md == "" {
		return ""
	}

	// Extract fenced code blocks before escaping so their content survives verbatim.
	type slot struct{ open, body, close string }
	var slots []slot
	md = reFence.ReplaceAllStringFunc(md, func(m string) string {
		sub := reFence.FindStringSubmatch(m)
		lang := sub[1]
		body := html.EscapeString(sub[2])
		open := "<pre><code"
		if lang != "" {
			open += ` class="language-` + html.EscapeString(lang) + `"`
		}
		open += ">"
		slots = append(slots, slot{open: open, body: body, close: "</code></pre>"})
		return placeholder(len(slots) - 1)
	})

	// Extract inline code likewise.
	var inlineSlots []string
	md = reInline.ReplaceAllStringFunc(md, func(m string) string {
		sub := reInline.FindStringSubmatch(m)
		inlineSlots = append(inlineSlots, html.EscapeString(sub[1]))
		return inlinePlaceholder(len(inlineSlots) - 1)
	})

	// Now safe to escape the rest of the text.
	md = html.EscapeString(md)

	// Bold, italic, links operate on escaped text — their delimiters survive escaping.
	md = reBold.ReplaceAllString(md, `<b>$1</b>`)
	md = reItalic.ReplaceAllStringFunc(md, func(m string) string {
		// Preserve the leading non-* boundary char.
		idx := strings.Index(m, "*")
		prefix := m[:idx]
		inner := m[idx+1 : len(m)-1]
		return prefix + "<i>" + inner + "</i>"
	})
	md = reLink.ReplaceAllString(md, `<a href="$2">$1</a>`)

	// Newline → <br>.
	md = strings.ReplaceAll(md, "\n", "<br>")

	// Restore placeholders.
	for i, s := range slots {
		md = strings.Replace(md, placeholder(i), s.open+s.body+s.close, 1)
	}
	for i, s := range inlineSlots {
		md = strings.Replace(md, inlinePlaceholder(i), "<code>"+s+"</code>", 1)
	}
	return md
}

func placeholder(i int) string       { return "\x00ELMD_BLOCK_" + itoa(i) + "\x00" }
func inlinePlaceholder(i int) string { return "\x00ELMD_INLINE_" + itoa(i) + "\x00" }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
