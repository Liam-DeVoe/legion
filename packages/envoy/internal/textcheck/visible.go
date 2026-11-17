package textcheck

import "strings"

// VisibleMarkdown is doc as a reader of the rendered page sees it, for a check that holds a
// guide's text to something: every HTML comment (`<!-- … -->`, on one line or across several) is
// gone, so a passage moved inside one no longer counts as the guide stating it. A check that read
// the raw bytes passed while the page showed nothing, since a comment is in the file and on no
// page.
//
// A comment's newlines stay, so a line number a caller reports from the result is the line in the
// file. What is not a comment stays as written: a `<!--` inside a fenced code block or inside a
// code span is text a reader sees (packages/envoy/AGENTS.md shows the `<!-- legion:` footer in
// one), and a `-->` with no comment open is text too. A comment that never closes runs to the end
// of the document, as CommonMark's HTML block does. It reads no other HTML, and it does not
// model a comment that a list item or a block quote around it would end early.
func VisibleMarkdown(doc string) string {
	var out strings.Builder
	out.Grow(len(doc))
	fence := ""
	for len(doc) > 0 {
		line, rest, newline := strings.Cut(doc, "\n")
		if marker := fenceMarker(line); fence == "" && marker != "" {
			fence = marker
		} else if fence != "" {
			if marker != "" && strings.HasPrefix(marker, fence[:1]) && len(marker) >= len(fence) &&
				strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), fence[:1])) == "" {
				fence = ""
			}
		} else {
			// Outside a fence, a line is read with the rest of its paragraph, since a code span
			// and a comment may both run across lines.
			paragraph, after := cutParagraph(doc)
			out.WriteString(withoutComments(paragraph, &after))
			doc = after
			continue
		}
		out.WriteString(line)
		if newline {
			out.WriteByte('\n')
		}
		doc = rest
	}
	return out.String()
}

// fenceMarker is the run of backticks or tildes, three or more, that opens or closes a fenced
// code block on line, indented by at most three spaces; "" when line is no fence.
func fenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return ""
	}
	run := len(trimmed) - len(strings.TrimLeft(trimmed, trimmed[:1]))
	if run < 3 {
		return ""
	}
	return trimmed[:run]
}

// cutParagraph splits doc before the first line, after its first, that is blank or opens a fence:
// the span a code span can run across.
func cutParagraph(doc string) (paragraph, after string) {
	at := 0
	for first := true; at < len(doc); first = false {
		end := strings.IndexByte(doc[at:], '\n')
		line := doc[at:]
		if end >= 0 {
			line = doc[at : at+end]
		}
		if !first && (strings.TrimSpace(line) == "" || fenceMarker(line) != "") {
			break
		}
		if end < 0 {
			at = len(doc)
			break
		}
		at += end + 1
	}
	return doc[:at], doc[at:]
}

// withoutComments is paragraph with each comment that opens in it replaced by the newlines it
// held. A comment that runs past the paragraph takes what it needs of *after, through its `-->`,
// and *after is left holding the rest.
func withoutComments(paragraph string, after *string) string {
	var out strings.Builder
	for i := 0; i < len(paragraph); {
		switch {
		case paragraph[i] == '`':
			run := len(paragraph[i:]) - len(strings.TrimLeft(paragraph[i:], "`"))
			closing := codeSpanEnd(paragraph[i+run:], run)
			if closing < 0 {
				out.WriteString(paragraph[i : i+run])
				i += run
				continue
			}
			end := i + run + closing + run
			out.WriteString(paragraph[i:end])
			i = end
		case strings.HasPrefix(paragraph[i:], "<!--"):
			body := paragraph[i+len("<!--"):]
			if end := strings.Index(body, "-->"); end >= 0 {
				out.WriteString(strings.Repeat("\n", strings.Count(body[:end], "\n")))
				i += len("<!--") + end + len("-->")
				continue
			}
			// The comment runs past this paragraph: through the `-->` in what follows, or to the end.
			out.WriteString(strings.Repeat("\n", strings.Count(body, "\n")))
			end := strings.Index(*after, "-->")
			if end < 0 {
				out.WriteString(strings.Repeat("\n", strings.Count(*after, "\n")))
				*after = ""
				return out.String()
			}
			out.WriteString(strings.Repeat("\n", strings.Count((*after)[:end], "\n")))
			rest := (*after)[end+len("-->"):]
			*after = ""
			// What follows the comment's end on its line, and the paragraph it is in, are read
			// again, since they may hold a comment or a code span of their own.
			restParagraph, restAfter := cutParagraph(rest)
			out.WriteString(withoutComments(restParagraph, &restAfter))
			*after = restAfter
			return out.String()
		default:
			out.WriteByte(paragraph[i])
			i++
		}
	}
	return out.String()
}

// codeSpanEnd is where in text the run of exactly run backticks that closes a code span starts,
// or -1 when none does.
func codeSpanEnd(text string, run int) int {
	for at := 0; at < len(text); {
		next := strings.IndexByte(text[at:], '`')
		if next < 0 {
			return -1
		}
		start := at + next
		length := len(text[start:]) - len(strings.TrimLeft(text[start:], "`"))
		if length == run {
			return start
		}
		at = start + length
	}
	return -1
}
