package text

import (
	"slices"
	"unicode"
	"unicode/utf8"
)

// The word boundaries of Unicode's default word rules, UAX #29 "Unicode Text Segmentation"
// (https://www.unicode.org/reports/tr29/#Word_Boundaries), for Unicode 17.0.0: the rules WB1 to
// WB999, read over each rune's Word_Break value and its Extended_Pictographic property, which
// words_table.go holds. TestWordSegmentsBreakWhereUnicodesTestDataDoes holds them to every case of
// Unicode's own test data for that version, testdata/WordBreakTest-17.0.0.txt.
//
// So a boundary falls beside each ideograph, each Hiragana character, each letter of Thai and of
// the other scripts the rules leave to a dictionary, each emoji, symbol and punctuation mark, and
// at white space; none falls inside a run of letters, digits and the connecting punctuation the
// rules keep inside a word (a full stop, a colon or an apostrophe between two letters, a full stop
// or a comma between two digits, an underscore), inside a run of Katakana, between a character
// and the marks, joiners and format characters after it, between a zero-width joiner and the
// pictograph after it, or between the two regional indicators of a flag.

// wordBreak is a rune's Word_Break value.
type wordBreak uint8

const (
	wbOther wordBreak = iota
	wbCR
	wbLF
	wbNewline
	wbExtend
	wbZWJ
	wbRegionalIndicator
	wbFormat
	wbKatakana
	wbHebrewLetter
	wbALetter
	wbSingleQuote
	wbDoubleQuote
	wbMidNumLet
	wbMidLetter
	wbMidNum
	wbNumeric
	wbExtendNumLet
	wbWSegSpace
)

// wordBreakRange is a range of code points, first to last, that share a Word_Break value and
// whether they are Extended_Pictographic.
type wordBreakRange struct {
	first, last  rune
	value        wordBreak
	pictographic bool
}

// wordBreakOf is r's Word_Break value, and whether it is Extended_Pictographic. A code point
// wordBreakRanges does not hold is Other and no pictograph.
func wordBreakOf(r rune) (wordBreak, bool) {
	i, found := slices.BinarySearchFunc(wordBreakRanges[:], r, func(entry wordBreakRange, r rune) int {
		switch {
		case entry.last < r:
			return -1
		case entry.first > r:
			return 1
		}
		return 0
	})
	if !found {
		return wbOther, false
	}
	return wordBreakRanges[i].value, wordBreakRanges[i].pictographic
}

// joinsBefore reports whether WB4 joins a rune of this value to the rune before it: Extend,
// Format and ZWJ.
func joinsBefore(value wordBreak) bool {
	return value == wbExtend || value == wbFormat || value == wbZWJ
}

// lineBreak reports whether a rune of this value breaks a line: CR, LF and Newline.
func lineBreak(value wordBreak) bool {
	return value == wbCR || value == wbLF || value == wbNewline
}

// aHLetter is AHLetter: ALetter or Hebrew_Letter.
func aHLetter(value wordBreak) bool {
	return value == wbALetter || value == wbHebrewLetter
}

// midLetter is what WB6 and WB7 keep between two letters: MidLetter, MidNumLet or Single_Quote.
func midLetter(value wordBreak) bool {
	return value == wbMidLetter || value == wbMidNumLet || value == wbSingleQuote
}

// midNum is what WB11 and WB12 keep between two digits: MidNum, MidNumLet or Single_Quote.
func midNum(value wordBreak) bool {
	return value == wbMidNum || value == wbMidNumLet || value == wbSingleQuote
}

// extendsNumLet is what WB13a and WB13b join an ExtendNumLet to: AHLetter, Numeric or Katakana.
func extendsNumLet(value wordBreak) bool {
	return aHLetter(value) || value == wbNumeric || value == wbKatakana
}

// wordSegments reads a string one segment at a time, each segment running from one UAX #29 word
// boundary to the next.
type wordSegments struct {
	s string
	// next is the byte offset of the first rune not yet read.
	next int
	// before is the Word_Break value of the last rune read, as it stands, which WB3 to WB4 read.
	// last and lastButOne are the values of the last two runes read once WB4 has joined each
	// Extend, Format and ZWJ rune to the rune before it, which WB5 to WB16 read; riOdd is whether
	// the regional indicators that end those runes are odd in number (WB15, WB16).
	before, last, lastButOne wordBreak
	riOdd                    bool
}

// segment reads the next segment and returns the byte offset of its end, the number of runes it
// holds, and whether it holds a word: a rune that is no white space (unicode.IsSpace, where
// strings.Fields parts a string) and that WB4 does not join to the rune before it. It returns 0
// runes when the string is read to its end.
func (w *wordSegments) segment() (end, runes int, word bool) {
	for w.next < len(w.s) {
		r, size := utf8.DecodeRuneInString(w.s[w.next:])
		value, pictographic := wordBreakOf(r)
		if runes > 0 && w.breaksBefore(value, pictographic, w.next+size) {
			break
		}
		w.read(value)
		w.next += size
		runes++
		if !word && !joinsBefore(value) && !unicode.IsSpace(r) {
			word = true
		}
	}
	return w.next, runes, word
}

// read takes in the next rune, of Word_Break value value.
func (w *wordSegments) read(value wordBreak) {
	w.before = value
	// WB4 joins an Extend, Format or ZWJ rune to the rune before it, so WB5 to WB16 read past it.
	// Its exceptions, such a rune at the start or after a line break, need no branch of their
	// own: there the rune begins a segment (WB1, WB3a), and no rule from WB5 on keeps the rune
	// after it with it, as none keeps one with the start or a line break.
	if joinsBefore(value) {
		return
	}
	w.riOdd = value == wbRegionalIndicator && !(w.last == wbRegionalIndicator && w.riOdd)
	w.lastButOne, w.last = w.last, value
}

// breaksBefore reports whether UAX #29 puts a word boundary between the last rune read and the
// next, of Word_Break value value, Extended_Pictographic where pictographic; after is the byte
// offset just past that rune, from which WB6, WB7b and WB12 read ahead.
func (w *wordSegments) breaksBefore(value wordBreak, pictographic bool, after int) bool {
	switch {
	case w.before == wbCR && value == wbLF: // WB3
		return false
	// WB3b needs no case of its own: no rule below keeps a line break with the rune before it.
	case lineBreak(w.before): // WB3a
		return true
	case w.before == wbZWJ && pictographic: // WB3c
		return false
	case w.before == wbWSegSpace && value == wbWSegSpace: // WB3d
		return false
	case joinsBefore(value): // WB4
		return false
	}
	last, lastButOne := w.last, w.lastButOne
	switch {
	case aHLetter(last) && aHLetter(value): // WB5
	case aHLetter(last) && midLetter(value) && aHLetter(w.ahead(after)): // WB6
	case aHLetter(lastButOne) && midLetter(last) && aHLetter(value): // WB7
	case last == wbHebrewLetter && value == wbSingleQuote: // WB7a
	case last == wbHebrewLetter && value == wbDoubleQuote && w.ahead(after) == wbHebrewLetter: // WB7b
	case lastButOne == wbHebrewLetter && last == wbDoubleQuote && value == wbHebrewLetter: // WB7c
	case last == wbNumeric && value == wbNumeric: // WB8
	case aHLetter(last) && value == wbNumeric: // WB9
	case last == wbNumeric && aHLetter(value): // WB10
	case lastButOne == wbNumeric && midNum(last) && value == wbNumeric: // WB11
	case last == wbNumeric && midNum(value) && w.ahead(after) == wbNumeric: // WB12
	case last == wbKatakana && value == wbKatakana: // WB13
	case (extendsNumLet(last) || last == wbExtendNumLet) && value == wbExtendNumLet: // WB13a
	case last == wbExtendNumLet && extendsNumLet(value): // WB13b
	case last == wbRegionalIndicator && value == wbRegionalIndicator && w.riOdd: // WB15, WB16
	default:
		return true // WB999
	}
	return false
}

// ahead is the Word_Break value of the first rune at or after the byte offset from that WB4 does
// not join to the rune before it, or Other where none is left.
func (w *wordSegments) ahead(from int) wordBreak {
	for _, r := range w.s[from:] {
		if value, _ := wordBreakOf(r); !joinsBefore(value) {
			return value
		}
	}
	return wbOther
}
