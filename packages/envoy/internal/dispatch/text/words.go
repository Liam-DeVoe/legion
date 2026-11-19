package text

import "unicode"

// The word boundaries of scripts written without spaces between their words, as Unicode's default
// word rules (UAX #29, https://www.unicode.org/reports/tr29/) put them: around each character
// (rule WB999), apart from the marks joined to a character (WB4). Text in other scripts ends a
// word at white space alone (HeadWords), so a long token such as a path or a URL stays one word.

// unspacedBoundary reports whether UAX #29's default rules put a word boundary between before
// and r because one side is a character of a script written without spaces (unspaced). base is
// the last rune at or before before that the rules do not join to the rune before it: a mark
// belongs to the letter it follows, so the boundary after a letter falls after its marks. No
// boundary falls before a rune joined to the one before it (WB4). None falls right after a
// zero-width joiner either: the rules join one to a pictograph after it (WB3c), and this code
// does not tell pictographs apart, so it never cuts there.
func unspacedBoundary(before, base, r rune) bool {
	if joinsBefore(r) || before == '\u200d' {
		return false
	}
	return unspaced(base) || unspaced(r)
}

// unspaced reports whether r is a character UAX #29's default rules break around because its
// script is written without spaces between words: an ideograph (Ideographic, as in Chinese and
// Japanese), a Hiragana character, or a letter of a script whose words the rules leave to a
// dictionary (Line_Break Complex_Context: Thai, Lao, Myanmar, Khmer, Tai Le, New Tai Lue, Tai
// Tham, Tai Viet and Ahom). Each has the Word_Break value Other, so WB999 breaks on both sides of
// it. A run of Katakana is one word under the same rules (WB13), as a run of Hangul is, and a
// rune joined to the one before it is part of that one.
func unspaced(r rune) bool {
	if joinsBefore(r) {
		return false
	}
	if unicode.In(r, unicode.Ideographic, unicode.Hiragana) {
		return true
	}
	return unicode.IsLetter(r) && unicode.In(r, unicode.Thai, unicode.Lao, unicode.Myanmar, unicode.Khmer,
		unicode.Tai_Le, unicode.New_Tai_Lue, unicode.Tai_Tham, unicode.Tai_Viet, unicode.Ahom)
}

// joinsBefore reports whether UAX #29 joins r to the rune before it (WB4): whether its Word_Break
// value is Extend, Format or ZWJ. Extend is Grapheme_Extend (the marks Mn and Me, and
// Other_Grapheme_Extend), the spacing marks (Mc) and the five emoji skin-tone modifiers; Format
// and ZWJ are the format characters (Cf) but the zero-width space and the prepended
// concatenation marks, such as the Arabic number sign.
func joinsBefore(r rune) bool {
	switch {
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc, unicode.Other_Grapheme_Extend):
		return true
	case r >= 0x1f3fb && r <= 0x1f3ff:
		return true
	case unicode.Is(unicode.Cf, r):
		return r != '\u200b' && !unicode.Is(unicode.Prepended_Concatenation_Mark, r)
	}
	return false
}
