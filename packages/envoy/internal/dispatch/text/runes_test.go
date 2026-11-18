package text

import (
	"strings"
	"testing"
)

// HeadRunes counts a rune of any script as one, and a document edit's read-back cuts its last
// block with it. A cut whose answer is the value's own first bytes is a slice of the value, so it
// copies nothing; only a kept byte that is not UTF-8, which the cut writes as U+FFFD as a
// conversion to []rune does, makes a copy. Each row is one shape of input, and each row checks
// the answer and, where the answer is a slice, that the cut made no allocation.
func TestHeadRunesCutsAtARuneAndCopiesNothing(t *testing.T) {
	for _, row := range []struct {
		name, value string
		limit       int
		want        string
	}{
		{name: "an empty value is unchanged", value: "", limit: 2, want: ""},
		{name: "a limit of nothing keeps nothing", value: "界a", limit: 0, want: ""},
		{name: "a limit of one keeps the first character", value: "界a", limit: 1, want: "界"},
		{name: "a value at the limit is unchanged", value: "界界界", limit: 3, want: "界界界"},
		{name: "a value under the limit is unchanged", value: "abc", limit: 4, want: "abc"},
		{name: "Latin letters are cut at the limit", value: strings.Repeat("a", 10), limit: 4, want: "aaaa"},
		{name: "Chinese characters are cut at the limit", value: strings.Repeat("界", 10), limit: 4, want: "界界界界"},
		{name: "characters outside the BMP are cut at the limit", value: strings.Repeat("𠀀", 10), limit: 4, want: "𠀀𠀀𠀀𠀀"},
		{name: "2,001 Chinese characters are cut at 1,967", value: strings.Repeat("界", 2001), limit: 1967, want: strings.Repeat("界", 1967)},
		// A U+FFFD the value holds is three bytes of UTF-8, not a byte that is not UTF-8, so it
		// is kept as it is. "At the cut" is the last character kept.
		{name: "a U+FFFD before the cut is kept", value: "\uFFFDab", limit: 2, want: "\uFFFDa"},
		{name: "a U+FFFD at the cut is kept", value: "a\uFFFDbc", limit: 2, want: "a\uFFFD"},
		{name: "a U+FFFD after the cut is left out", value: "ab\uFFFDc", limit: 2, want: "ab"},
		{name: "100 pairs of a letter and U+FFFD are cut at 50", value: strings.Repeat("a\uFFFD", 100), limit: 50, want: strings.Repeat("a\uFFFD", 25)},
		// A byte that is not UTF-8 is one rune too. Kept, it is written as U+FFFD, also when it
		// is the first of U+FFFD's three bytes alone; past the cut it is not read.
		{name: "a byte that is not UTF-8 before the cut is written as U+FFFD", value: "\xffab", limit: 2, want: "\uFFFDa"},
		{name: "a byte that is not UTF-8 at the cut is written as U+FFFD", value: "a\xffbc", limit: 2, want: "a\uFFFD"},
		{name: "a lone first byte of U+FFFD at the cut is written as U+FFFD", value: "a\xefbc", limit: 2, want: "a\uFFFD"},
		{name: "a byte that is not UTF-8 right after the cut is left out", value: "ab\xffc", limit: 2, want: "ab"},
		{name: "a byte that is not UTF-8 past the cut is left out", value: strings.Repeat("界", 10) + "\xff", limit: 5, want: strings.Repeat("界", 5)},
		{name: "a value at the limit keeps a byte that is not UTF-8", value: "a\xff", limit: 2, want: "a\xff"},
	} {
		t.Run(row.name, func(t *testing.T) {
			var got string
			allocations := testing.AllocsPerRun(100, func() { got = HeadRunes(row.value, row.limit) })
			if got != row.want {
				t.Fatalf("HeadRunes to %d kept %d bytes, %.40q, want %d bytes, %.40q", row.limit, len(got), got, len(row.want), row.want)
			}
			if strings.HasPrefix(row.value, row.want) && allocations != 0 {
				t.Fatalf("HeadRunes to %d made %v allocations, want a slice of the value and none", row.limit, allocations)
			}
		})
	}
}
