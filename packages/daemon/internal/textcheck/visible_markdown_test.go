package textcheck

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// errHidden is why a check refuses a line it reads the guide by: only an HTML comment holds it.
var errHidden = errors.New("only an HTML comment holds it, and the rendered guide shows none of it")

// errCommented is why a check refuses a Markdown text whose two readings disagree on it
// (heldAsEachReaderReads): an HTML comment holds what the check reads, or stands inside it.
var errCommented = errors.New("the text as written and as the rendered page shows it disagree on " +
	"it, so an HTML comment decides it: take the comment out of what the check reads")

// heldAsEachReaderReads holds check to text, the file called name, as each of its readers reads it:
// as written, which an agent or an editor reads, and, for a Markdown file, as the rendered page
// shows it (visibleMarkdown), which a person reads. A check that passes in one reading and fails in
// the other, or fails differently in each, reads what an HTML comment hides from the page or what
// a comment stands inside of, and is refused for it (errCommented), naming each reading that
// fails. Where both fail alike, the written one answers, so a text whose comments stand outside
// what the check reads fails as it always did. Outside Markdown an HTML comment hides nothing, and
// the text has the one reading.
func heldAsEachReaderReads(name, text string, check func(text string) error) error {
	written := check(text)
	if path.Ext(name) != ".md" {
		return written
	}
	shown := check(visibleMarkdown(text))
	const asWritten, asShown = "as written, HTML comments and all", "as the rendered page shows it, its HTML comments hidden"
	switch {
	case written == nil && shown == nil:
		return nil
	case written == nil:
		return fmt.Errorf("%s: %w: %w", asShown, shown, errCommented)
	case shown == nil:
		return fmt.Errorf("%s: %w: %w", asWritten, written, errCommented)
	case written.Error() != shown.Error():
		return fmt.Errorf("%s: %w; %s: %w: %w", asWritten, written, asShown, shown, errCommented)
	}
	return written
}

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
