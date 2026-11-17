package textcheck

import (
	"errors"
	"strings"
)

// errHidden is why a check refuses a line it reads the guide by: only an HTML comment holds it.
var errHidden = errors.New("only an HTML comment holds it, and the rendered guide shows none of it")

// visibleMarkdown is text as the rendered page shows it: each HTML comment outside a fenced block
// is blanked, its newlines kept, so that every line keeps its number. A comment never closed runs
// to the end of the text, as one that opens a line does on the page. A fenced block, from a line
// opening "```" to the next, shows its lines as written, a "<!--" among them, and a fence inside a
// comment opens nothing. A check reads the guide through this before it selects a line, so that a
// comment opened before that line still hides it.
func visibleMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	fenced, commented := false, false
	for i, line := range lines {
		if !commented && strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		var shown strings.Builder
		for line != "" {
			if commented {
				var closed bool
				_, line, closed = strings.Cut(line, "-->")
				commented = !closed
				continue
			}
			var before string
			before, line, commented = strings.Cut(line, "<!--")
			shown.WriteString(before)
		}
		lines[i] = shown.String()
	}
	return strings.Join(lines, "\n")
}
