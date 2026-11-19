package text

import "strings"

// HeadRunes returns value cut to its first limit runes; a value at or under the
// limit is returned unchanged.
func HeadRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// wordSpace is the white space HeadWords ends a word at.
const wordSpace = " \t\n"

// HeadWords returns value cut at a word boundary to at most its first limit runes, and whether
// it left a word out. A word ends at white space, and also wherever Unicode's default word rules
// (UAX #29) put a boundary beside a character of a script written without spaces between its
// words, such as Chinese, Japanese or Thai (unspacedBoundary). The cut falls at the last word end
// at or before the limit, so a word that ends at the limit itself is kept. A word is never cut in
// two: where no word ends inside the limit, because the first word alone is longer, the answer is
// that whole first word, past the limit, with no bound but the value's own length. The answer is
// a prefix of value, and a cut only where a word follows it, not where white space alone does; a
// value at or under the limit is returned unchanged and uncut. A caller that marks a cut (an
// ellipsis) reads the second answer rather than comparing lengths, which white space the cut
// removed would make wrong. The walk stops at the first word end past the limit, so a long value
// is read only that far.
func HeadWords(value string, limit int) (string, bool) {
	// end is the byte offset after the last word that ends inside the limit, 0 while none has.
	// base is the last rune read that UAX #29 does not join to the rune before it (WB4).
	end, count := 0, 0
	var before, base rune
	for offset, r := range value {
		if count > 0 && wordEnds(before, base, r) {
			if count > limit {
				// No word ended inside the limit, so this is the end of the first word.
				return value[:offset], wordFollows(value[offset:])
			}
			end = offset
		}
		if count == limit && end > 0 {
			return value[:end], wordFollows(value[end:])
		}
		if count == 0 || !joinsBefore(r) {
			base = r
		}
		before = r
		count++
	}
	// The value is at or under the limit, or it is one word.
	return value, false
}

// wordEnds reports whether a word ends between before and r, the rune after it, where base is
// the last rune at or before before that UAX #29 does not join to the rune before it: before is
// no white space, and r is white space or begins a word of its own.
func wordEnds(before, base, r rune) bool {
	if strings.ContainsRune(wordSpace, before) {
		return false
	}
	return strings.ContainsRune(wordSpace, r) || unspacedBoundary(before, base, r)
}

// wordFollows reports whether rest holds a word: a rune that is no white space.
func wordFollows(rest string) bool {
	return strings.TrimLeft(rest, wordSpace) != ""
}
