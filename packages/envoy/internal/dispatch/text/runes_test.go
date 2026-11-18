package text

import (
	"strings"
	"testing"
)

// HeadRunes counts a rune of any script as one, and the cut is a slice of the value it was
// given, so a cut copies nothing, also after a U+FFFD the value holds: a document edit's
// read-back cuts its last block with it.
func TestHeadRunesCutsAtARuneAndCopiesNothing(t *testing.T) {
	for _, row := range []struct {
		name, value string
		limit       int
		want        string
	}{
		{name: "a value under the limit is unchanged", value: "abc", limit: 4, want: "abc"},
		{name: "a value at the limit is unchanged", value: "界界界", limit: 3, want: "界界界"},
		{name: "Latin letters are cut at the limit", value: strings.Repeat("a", 10), limit: 4, want: "aaaa"},
		{name: "Chinese characters are cut at the limit", value: strings.Repeat("界", 10), limit: 4, want: "界界界界"},
		{name: "characters outside the BMP are cut at the limit", value: strings.Repeat("𠀀", 10), limit: 4, want: "𠀀𠀀𠀀𠀀"},
		{name: "a limit of nothing keeps nothing", value: "界a", limit: 0, want: ""},
		{name: "a U+FFFD the value holds is one rune", value: "a\uFFFDbc", limit: 2, want: "a\uFFFD"},
		// A byte that is not UTF-8 is one rune, and a cut writes it as U+FFFD, as a conversion to
		// []rune does; past the cut it is not read.
		{name: "a byte that is not UTF-8 before the cut is written as U+FFFD", value: "a\xffbc", limit: 2, want: "a\uFFFD"},
		{name: "a byte that is not UTF-8 after the cut is left out", value: "ab\xffc", limit: 2, want: "ab"},
		{name: "a value under the limit keeps a byte that is not UTF-8", value: "a\xff", limit: 2, want: "a\xff"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := HeadRunes(row.value, row.limit); got != row.want {
				t.Fatalf("HeadRunes(%q, %d) = %q, want %q", row.value, row.limit, got, row.want)
			}
		})
	}
	// A U+FFFD the value holds is three bytes of UTF-8, not a byte that is not UTF-8, so a cut
	// after one is a slice of the value too.
	for _, row := range []struct {
		name, value string
		limit, kept int
	}{
		{name: "2,001 Chinese characters", value: strings.Repeat("界", 2001), limit: 1967, kept: len("界") * 1967},
		{name: "100 pairs of a letter and U+FFFD", value: strings.Repeat("a\uFFFD", 100), limit: 50, kept: len("a\uFFFD") * 25},
	} {
		var cut string
		if allocations := testing.AllocsPerRun(100, func() { cut = HeadRunes(row.value, row.limit) }); allocations != 0 {
			t.Fatalf("HeadRunes of %s to %d made %v allocations, want a slice of the value and none", row.name, row.limit, allocations)
		}
		if cut != row.value[:row.kept] {
			t.Fatalf("HeadRunes of %s kept %d bytes, want the first %d characters", row.name, len(cut), row.limit)
		}
	}
}
