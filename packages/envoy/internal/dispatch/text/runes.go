package text

import (
	"strings"
	"unicode"
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
// it left a word out. Its boundaries are those of Unicode's default word rules (UAX #29,
// words.go), which part a value into words and the white space between them: a boundary falls at
// white space, beside each character of a script written without spaces between its words, such
// as Chinese, Japanese or Thai, and beside each emoji, symbol and punctuation mark, but never
// inside a run of letters and digits, so a word of a script written with spaces is one word, at
// any length. The cut falls at the last end of a word at or before the limit, so a word that ends
// at the limit itself is kept, and white space is never kept at the end of a cut. A word is never
// cut in two: where no word ends inside the limit, because the first word alone is longer, the
// answer is that whole first word, past the limit, with no bound but the value's own length. The
// answer is a prefix of value, and a cut only where a word follows it, not where white space alone
// does; a value at or under the limit is returned unchanged and uncut. A caller that marks a cut
// (an ellipsis) reads the second answer rather than comparing lengths, which white space the cut
// removed would make wrong. The walk stops at the first word that ends past the limit, so a long
// value is read only that far.
func HeadWords(value string, limit int) (string, bool) {
	// end is the byte offset after the last word that ends inside the limit, 0 while none has;
	// count is the number of runes read.
	segments := wordSegments{s: value}
	end, count := 0, 0
	for {
		next, runes, word := segments.segment()
		if runes == 0 {
			break
		}
		count += runes
		switch {
		case !word:
		case count <= limit:
			end = next
		case end == 0:
			// No word ended inside the limit, so this is the first word, kept whole.
			return value[:next], wordFollows(value[next:])
		default:
			return value[:end], true
		}
	}
	if count <= limit || end == 0 {
		// The value is at or under the limit, or it holds no word.
		return value, false
	}
	// White space alone follows the last word kept.
	return value[:end], false
}

// wordFollows reports whether rest holds a word: a rune that is no white space and that WB4 does
// not join to the rune before it, as wordSegments.segment reads one.
func wordFollows(rest string) bool {
	return strings.IndexFunc(rest, func(r rune) bool {
		value, _ := wordBreakOf(r)
		return !joinsBefore(value) && !unicode.IsSpace(r)
	}) >= 0
}
