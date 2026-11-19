package text

import (
	"fmt"
	"strings"
	"testing"
)

// headWordsRow is one value HeadWords cuts, and the answer it must give.
type headWordsRow struct {
	name, value string
	limit       int
	want        string
	cut         bool
}

func checkHeadWords(t *testing.T, rows []headWordsRow) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, cut := HeadWords(row.value, row.limit)
			if got != row.want || cut != row.cut {
				t.Fatalf("HeadWords(%q, %d) = %q, %v; want %q, %v", row.value, row.limit, got, cut, row.want, row.cut)
			}
		})
	}
}

// A word ends at white space: the cut falls at the last word end at or before the limit, a word
// is never cut in two, and the answer is a cut only where a word follows it. Both callers in the
// api package pass words joined by single spaces, so the rows with other white space are the only
// ones that hold what HeadWords does with it.
func TestHeadWordsEndsAWordAtWhiteSpace(t *testing.T) {
	rows := []headWordsRow{
		{name: "the cut falls at the last space inside the limit", value: "one two three four", limit: 10, want: "one two", cut: true},
		{name: "a word that ends at the limit is kept", value: "one two three four", limit: 13, want: "one two three", cut: true},
		{name: "a word that ends one past the limit is left out", value: "one two three four", limit: 12, want: "one two", cut: true},
		{name: "a tab and a newline end a word as a space does", value: "one\ttwo\nthree", limit: 9, want: "one\ttwo", cut: true},
		{name: "white space at the start is kept and ends no word", value: "  one two", limit: 6, want: "  one", cut: true},
		{name: "white space alone after the last word kept is no cut", value: "one two     ", limit: 9, want: "one two", cut: false},
		{name: "a first word past the limit is kept whole and is a cut where a word follows it", value: "abcdefghij klm", limit: 4, want: "abcdefghij", cut: true},
		{name: "a first word past the limit with white space alone after it is no cut", value: "abcdefghij   ", limit: 4, want: "abcdefghij", cut: false},
		{name: "a first word past the limit that is the whole value is returned whole", value: "abcdefghij", limit: 4, want: "abcdefghij", cut: false},
		{name: "a value at or under the limit is returned unchanged", value: " one  two ", limit: 10, want: " one  two ", cut: false},
	}
	for _, length := range []int{81, 110, 161, 1000} {
		word := strings.Repeat("w", length)
		rows = append(rows,
			headWordsRow{name: fmt.Sprintf("a first word of %d characters is kept whole before more words", length), value: word + " and more", limit: 80, want: word, cut: true},
			headWordsRow{name: fmt.Sprintf("a first word of %d characters that is the whole value is returned whole", length), value: word, limit: 80, want: word, cut: false})
	}
	checkHeadWords(t, rows)
}

// In a script written without spaces between its words, Unicode's default word rules (UAX #29)
// put a word boundary beside each ideograph, each Hiragana character and each letter of Thai and
// the other Complex_Context scripts (WB999), and keep a mark, a joiner or a format character with
// the character before it (WB4). Katakana is one word for as long as it runs (WB13).
func TestHeadWordsCutsAScriptWithoutSpacesAtItsWordBoundaries(t *testing.T) {
	checkHeadWords(t, []headWordsRow{
		{name: "Chinese: a boundary falls beside each ideograph", value: "每个测试都通过了", limit: 3, want: "每个测", cut: true},
		{name: "Japanese: a boundary falls beside each Hiragana character", value: "すべてのしけん", limit: 3, want: "すべて", cut: true},
		{name: "Japanese: a run of Katakana is one word, kept whole past the limit", value: "テストテストは通った", limit: 4, want: "テストテスト", cut: true},
		{name: "Thai: the cut inside the limit falls before a letter, never before its marks", value: "ที่นี่", limit: 4, want: "ที่", cut: true},
		{name: "Thai: with no boundary inside the limit the first letter keeps its marks", value: "ที่นี่", limit: 2, want: "ที่", cut: true},
		{name: "a variation selector stays with its ideograph", value: "漢\ufe00字", limit: 1, want: "漢\ufe00", cut: true},
		{name: "a combining sound mark stays with its Hiragana character", value: "か\u3099き", limit: 1, want: "か\u3099", cut: true},
		{name: "a boundary after an ideograph falls after the marks that follow it", value: "中\ufe00defgh", limit: 4, want: "中\ufe00", cut: true},
		{name: "a first word in Latin letters ends at the ideograph after it", value: "abcdefgh中文", limit: 3, want: "abcdefgh", cut: true},
		{name: "words and ideographs mixed: the cut falls at the last boundary of either kind", value: "ab 中文字", limit: 4, want: "ab 中", cut: true},
		{name: "the white space before an ideograph is not kept at the end of the cut", value: "ab 中文字", limit: 3, want: "ab", cut: true},
		{name: "no cut falls right after a zero-width joiner", value: "中\u200d文字", limit: 2, want: "中\u200d文", cut: true},
		{name: "a value at or under the limit is returned unchanged", value: "每个测试", limit: 4, want: "每个测试", cut: false},
	})
}
