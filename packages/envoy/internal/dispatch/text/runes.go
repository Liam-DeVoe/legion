package text

import (
	"strings"
	"unicode"
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

// HeadWords returns value cut at a word boundary to at most its first limit runes, and whether
// it left a character of value out. Its boundaries are those of Unicode's default word rules
// (UAX #29, words.go), which part a value into words and the white space between them: a
// boundary falls at white space, beside each character of a script written without spaces
// between its words, such as Chinese, Japanese or Thai, and beside each emoji, symbol and
// punctuation mark, but never inside a run of letters and digits, so a word of a script written
// with spaces is one word, at any length. A run with no white space that holds a URL is one word
// too (isURL), so a cut never falls inside a link: the cut keeps the URL whole or falls before
// it. The cut falls at the last end of a word at or before the limit, so a word that ends at the
// limit itself is kept, and white space is never kept at the end of a cut.
//
// A word is never cut in two, and opening punctuation that leads into a word goes with that
// word. The first word is the first word that holds a letter or a number, with whitespace and an
// opening backtick, bracket, parenthesis or quote before it. Where it runs past the limit, the
// answer is that whole first word, past the limit, with no bound but the value's own length.
// Where a value holds no letter or number before a word that runs past the limit, and no word ends
// inside the limit, that word, of any kind, is kept whole the same way. A later word of letters or
// numbers that runs past the limit is left out with opening punctuation before it from the start
// of its run with no white space, so the answer does not end with a backtick or a bracket that
// opens a word it leaves out. Other punctuation, such as a slash, is a word boundary rather than
// a lead-in.
//
// The answer is a prefix of value. It is a cut where it leaves out a character that is no white
// space, a mark alone among them (leftOut), and no cut where it leaves out white space alone; a
// value at or under the limit is returned unchanged and uncut. A caller that marks a cut (an
// ellipsis) reads the second answer rather than comparing lengths, which white space the cut
// removed would make wrong. The walk stops at the first word that ends past the limit, so a long
// value is read only to the end of the run with no white space that holds that word.
func HeadWords(value string, limit int) (string, bool) {
	// end is the byte offset after the last word that ends inside the limit, 0 while none has;
	// lettered is whether a word that holds a letter or a number is among those; count is the
	// number of runes read. lead is end as it stood where the current run with no white space
	// began, while that run holds whitespace and opening punctuation before a letter word, and -1
	// after any other word: a later letter word that runs past the limit is left out with the
	// opening punctuation before it.
	segments := wordSegments{s: value}
	end, count, lettered, lead, start := 0, 0, false, 0, 0
	for {
		next, runes, class, starts := segments.headSegment()
		if runes == 0 {
			break
		}
		segment := value[start:next]
		start = next
		if starts {
			lead = end
		}
		count += runes
		switch {
		case class < classSymbol:
			// White space, or marks with no character before them: no word.
		case count <= limit:
			end = next
			switch {
			case class == classLetter:
				lettered, lead = true, -1
			case class == classSymbol && !opensWord(segment):
				lead = -1
			}
		case end == 0:
			// No word ended inside the limit, so this first word is kept whole.
			return value[:next], leftOut(value[next:])
		case class == classLetter && !lettered && lead >= 0:
			// The first letter word runs past the limit: it is kept with opening punctuation.
			return value[:next], leftOut(value[next:])
		case class == classLetter && lead >= 0:
			// A later word runs past the limit: opening punctuation is left out with it.
			return value[:lead], true
		default:
			return value[:end], true
		}
	}
	if count <= limit || end == 0 {
		// The value is at or under the limit, or it holds no word.
		return value, false
	}
	// No word runs past the limit, but a mark with no character before it may.
	return value[:end], leftOut(value[end:])
}

// openingPunctuation is the punctuation that opens a word a reader normally reads with it: a
// code mark, bracket, parenthesis or quote. A slash, hyphen, full stop and the other punctuation
// that separates words is not here, so it remains a boundary inside the budget.
const openingPunctuation = "`[({\"'“‘"

// opensWord reports whether every rune of segment is opening punctuation. segment is one UAX #29
// segment that ends inside the budget (HeadWords).
func opensWord(segment string) bool {
	return segment != "" && strings.IndexFunc(segment, func(r rune) bool {
		return !strings.ContainsRune(openingPunctuation, r)
	}) < 0
}

// headSegment reads the next segment as HeadWords cuts a value, and reports whether it starts a
// run with no white space: it is at the start of the value or after white space. Where it does,
// and that run holds a URL (isURL), the segment is the whole run, one word of the highest class of
// a rune it holds, and the word rules read each of its runes as segment does. Any other segment is
// segment's.
func (w *wordSegments) headSegment() (end, runes int, class runeClass, starts bool) {
	before, _ := utf8.DecodeLastRuneInString(w.s[:w.next])
	starts = w.next == 0 || unicode.IsSpace(before)
	if starts {
		run := w.s[w.next:]
		if space := strings.IndexFunc(run, unicode.IsSpace); space >= 0 {
			run = run[:space]
		}
		if isURL(run) {
			for _, r := range run {
				value, _ := wordBreakOf(r)
				w.read(value)
				runes++
				class = max(class, classOf(r, value))
			}
			w.next += len(run)
			return w.next, runes, class, starts
		}
	}
	end, runes, class = w.segment()
	return end, runes, class, starts
}

// isURL reports whether run, a run with no white space, holds a URL: a "://", as a check's words
// drop one (blockWords in the api package), or a "www." in any case at the run's start or after
// one of ( * _ [ ] ~, where GitHub's markdown starts a link with no scheme. GitHub's markdown
// makes a link of a URL up to the white space after it.
func isURL(run string) bool {
	if strings.Contains(run, "://") {
		return true
	}
	for at := 0; at+len("www.") <= len(run); at++ {
		if (at == 0 || strings.IndexByte("(*_[]~", run[at-1]) >= 0) && strings.EqualFold(run[at:at+len("www.")], "www.") {
			return true
		}
	}
	return false
}

// leftOut reports whether rest, what a cut leaves out of a value, holds a character of it: a rune
// above white space (classOf), a mark alone among them.
func leftOut(rest string) bool {
	return strings.IndexFunc(rest, func(r rune) bool {
		value, _ := wordBreakOf(r)
		return classOf(r, value) > classSpace
	}) >= 0
}
