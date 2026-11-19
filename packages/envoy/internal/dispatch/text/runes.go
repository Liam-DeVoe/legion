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

// HeadWords returns value cut at a word boundary to its first limit runes, and whether it cut:
// the cut falls at the last space inside the limit, so the answer ends at a whole word. It never
// cuts a word in two, so where no word ends inside the limit, because the first word alone is
// longer, the answer is that whole first word, past the limit, and it is a cut only where more
// text follows the word. A value at or under the limit is returned unchanged and uncut. A
// caller that marks a cut (an ellipsis) reads the second answer rather than comparing lengths,
// which a trailing space the cut removed would make wrong.
func HeadWords(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	head := string(runes[:limit])
	if space := strings.LastIndexAny(head, wordSpace); space >= 0 {
		if words := strings.TrimRight(head[:space], wordSpace); words != "" {
			return words, true
		}
	}
	// No word ends inside the limit, so the first word runs past it: keep that word whole, and
	// call it a cut only where more text follows it.
	end := limit
	for end < len(runes) && !strings.ContainsRune(wordSpace, runes[end]) {
		end++
	}
	if end == len(runes) {
		return value, false
	}
	word := strings.TrimRight(string(runes[:end]), wordSpace)
	for _, r := range runes[end:] {
		if !strings.ContainsRune(wordSpace, r) {
			return word, true
		}
	}
	return word, false
}
