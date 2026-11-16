package text

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// HeadRunes returns value cut to its first limit runes; a value at or under the
// limit is returned unchanged.
func HeadRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// HeadWords returns value cut to its first limit runes at a word boundary, and whether it cut:
// the cut falls back to the last space inside the limit, so the answer ends at a whole word,
// and a value whose first limit runes hold no space is cut at the limit itself, since there is
// no boundary to fall back to. A value at or under the limit is returned unchanged and uncut.
// A caller that marks a cut (an ellipsis) reads the second answer rather than comparing
// lengths, which a trailing space the cut removed would make wrong.
func HeadWords(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	head := string(runes[:limit])
	if space := strings.LastIndexAny(head, " \t\n"); space > 0 {
		head = head[:space]
	}
	return strings.TrimRight(head, " \t\n"), true
}

// HeadRefs is value cut to at most limit runes, ellipsis included, at a place no reference
// crosses: the cut of a text a reader is shown with its references drawn (an Inbox subject, a
// candidate's first line, an excerpt), where HeadRunes would leave half of one - `CORE-12` of
// `CORE-1234`, a working link to another issue, or `[@Planner](dispatch://agent/` drawn raw. A
// value at or under the limit is returned unchanged. One over it is cut to its first limit runes
// less the ellipsis's and the ellipsis is appended: `…` for a line, as contracts.OneLineSummary
// marks its cut, and "" for an excerpt, which marks none.
//
// The references are the grammar's own (referenceExtents): an address or a URL as Extract reads
// its span, a bare issue key and a `#id` as Mentions reads its candidates, and an inline link
// around any of them taken whole. Where one starts before the cut and ends after it, the cut
// moves back to its start - the link's `[` where a link wraps it - and the spaces and tabs before
// it are trimmed. The cut moves back the same way where the head, with the ellipsis after it,
// would read a reference the text does not write: `CORE-12` of `CORE-12x4`, which is no key, or
// an address the ellipsis joins (`dispatch://agent/s1` and `…` read as the session `s1…`). A cut
// that moves nothing is HeadRunes' own, so a text with no reference at the cut is cut as it always
// was, and a text that opens with a reference longer than the bound is cut to the ellipsis alone.
// No reference holds a line break, so only the line holding the cut is read.
func HeadRefs(value string, limit int, ellipsis string) string {
	keep := max(limit-utf8.RuneCountInString(ellipsis), 0)
	cut, count := 0, 0
	for index := range value {
		if count == keep {
			cut = index
		}
		if count == limit {
			lineStart := strings.LastIndexByte(value[:cut], '\n') + 1
			lineEnd := len(value)
			if newline := strings.IndexByte(value[cut:], '\n'); newline >= 0 {
				lineEnd = cut + newline
			}
			line := value[lineStart:lineEnd]
			return value[:lineStart+headCut(line, cut-lineStart, ellipsis)] + ellipsis
		}
		count++
	}
	return value
}

// headCut is where the head of line cut at at ends: at itself while no reference line writes
// crosses it and the head, with ellipsis after it, reads no reference line does not write; else
// the start of the first such reference, less the spaces and tabs before it, checked again from
// there. Every move is back, so it ends.
func headCut(line string, at int, ellipsis string) int {
	written := referenceExtents(line)
	for {
		back := at
		for _, reference := range written {
			if reference.start < at && at < reference.end {
				back = min(back, reference.start)
			}
		}
		for _, reference := range referenceExtents(line[:at] + ellipsis) {
			if reference.start < at && !slices.Contains(written, reference) {
				back = min(back, reference.start)
			}
		}
		if back == at {
			return at
		}
		at = len(strings.TrimRight(line[:back], " \t"))
	}
}

// extent is the bytes [start, end) of one reference in a line.
type extent struct{ start, end int }

// referenceExtents is every reference line writes as a reader sees it: each address and URL span
// (referencePattern, less what trimReference drops, and ending where a link around it closes,
// linkAddress), and each bare key and `#id` candidate (bareCandidates), whether or not it names
// anything, each widened over the inline link whose address it is (linkAround). None is parsed:
// a cut that splits a reference changes what the text says, whatever the reference names.
func referenceExtents(line string) []extent {
	spans := referencePattern.FindAllStringIndex(line, -1)
	found := make([]extent, 0, len(spans))
	for _, span := range spans {
		address := linkAddress(line, span[0], trimReference(line[span[0]:span[1]]))
		found = append(found, linkAround(line, span[0], span[0]+len(address)))
	}
	candidates, _, _ := bareCandidates(line, spans)
	for _, candidate := range candidates {
		end := candidate.offset + len(candidate.key)
		if candidate.key == "" {
			end = candidate.offset + 1 + len(candidate.id)
		}
		found = append(found, linkAround(line, candidate.offset, end))
	}
	return found
}

// linkAddress is raw, the reference span at start, ended where a markdown link's address ends
// when a link's `](` opens right before it: at the first `)` no `(` in it opens, which closes the
// link whatever follows it, so `[w](dispatch://x)…` is the link and the ellipsis after it. Any
// other span is raw as trimReference left it.
func linkAddress(line string, start int, raw string) string {
	if start < 2 || line[start-2:start] != "](" {
		return raw
	}
	depth := 0
	for index := range len(raw) {
		switch raw[index] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return raw[:index]
			}
			depth--
		}
	}
	return raw
}

// linkAround is the reference at [start, end) widened over the inline link whose address it is,
// where one wraps it: from the `[` that opens the link's words, or the `!` before it for a
// picture, through the `)` that closes the link. It reads the words back as CommonMark reads
// them, as the dashboard's inlineLinks does (web/src/features/refs/party-references.ts): a
// backslash takes the character after it as a literal one, and brackets that balance stand
// inside the words, so `[@Alice [x]](dispatch://agent/s1)` opens at the first `[` rather than at
// the `[x]` inside it. A line holds no line break, which ends the search.
func linkAround(line string, start, end int) extent {
	if end >= len(line) || line[end] != ')' || start < 2 || line[start-2:start] != "](" {
		return extent{start, end}
	}
	depth := 0
	for at := start - 3; at >= 0; at-- {
		if line[at] != '[' && line[at] != ']' || escapedAt(line, at) {
			continue
		}
		if line[at] == ']' {
			depth++
			continue
		}
		if depth > 0 {
			depth--
			continue
		}
		if at > 0 && line[at-1] == '!' && !escapedAt(line, at-1) {
			at--
		}
		return extent{at, end + 1}
	}
	return extent{start, end}
}

// escapedAt reports whether the character at at is the literal one a backslash takes it for: an
// odd number of backslashes stands before it.
func escapedAt(line string, at int) bool {
	slashes := 0
	for at--; at >= 0 && line[at] == '\\'; at-- {
		slashes++
	}
	return slashes%2 == 1
}
