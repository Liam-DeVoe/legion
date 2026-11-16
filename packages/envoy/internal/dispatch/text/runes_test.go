package text

import (
	"strings"
	"testing"
)

// TestHeadRefsNeverSplitsAReference holds HeadRefs to its rule: a reference that crosses the cut
// moves the cut back to its start, and so does one the head would read that the text does not
// write, while a text with no reference at the cut is cut as HeadRunes cuts it.
func TestHeadRefsNeverSplitsAReference(t *testing.T) {
	for _, row := range []struct {
		name     string
		value    string
		limit    int
		ellipsis string
		want     string
	}{
		{"a mention crossing the cut", "Ask [@Planner](dispatch://agent/s1) now", 10, "", "Ask"},
		{"a mention crossing the cut of a line", "Ask [@Planner](dispatch://agent/s1) now", 11, "…", "Ask…"},
		{"a mention crossing the cut at its address", "Ask [@Planner](dispatch://agent/s1) now", 20, "", "Ask"},
		{"a mention whose words escape a bracket", `Ask [@a\[b](dispatch://agent/s1) now`, 14, "", "Ask"},
		{"a cut in the words of a link whose words balance brackets", "Ask [@Alice [x]](dispatch://agent/s1) now", 10, "", "Ask"},
		{"a picture crossing the cut", "See ![chart](dispatch://CORE-1/artifact/chart) now", 8, "", "See"},
		{"a key in a link's words crossing the cut", "See [CORE-1234](dispatch://CORE-1234) now", 9, "", "See"},
		{"a bare address crossing the cut", "Read dispatch://CORE-1/spec first", 12, "", "Read"},
		{"a bare key crossing the cut", "Ship CORE-1234 today", 12, "", "Ship"},
		{"a bare key crossing the cut of a line", "Ship CORE-1234 today", 13, "…", "Ship…"},
		{"a dashboard URL crossing the cut", "Open https://dispatch.example/issues/CORE-1 now", 20, "", "Open"},
		{"a question id crossing the cut", "See #q12 now", 7, "", "See"},
		{"a key ending at the bound", "Ship CORE-1234 today", 14, "", "Ship CORE-1234"},
		{"a key ending at the bound of a line", "Ship CORE-1234 today", 15, "…", "Ship CORE-1234…"},
		{"a mention ending at the bound", "Ask [@Planner](dispatch://agent/s1) now", 35, "", "Ask [@Planner](dispatch://agent/s1)"},
		{"a mention ending at the bound of a line", "Ask [@Planner](dispatch://agent/s1) now", 36, "…", "Ask [@Planner](dispatch://agent/s1)…"},
		{"an address ending at the bound", "Read dispatch://CORE-1 first", 22, "", "Read dispatch://CORE-1"},
		{"an address the ellipsis would join", "Read dispatch://CORE-1 first", 23, "…", "Read…"},
		{"a key the cut would make of no key", "Ship CORE-12x4 today", 12, "", "Ship"},
		{"a key the cut would make of a decimal", "Ship CORE-1.5 today", 11, "", "Ship"},
		{"a line with no reference", "The quick brown fox", 9, "", "The quick"},
		{"a line with no reference cut after a space", "The quick brown fox", 4, "", "The "},
		{"a line with no reference and an ellipsis", "The quick brown fox", 10, "…", "The quick…"},
		{"a line at the bound", "Ship CORE-1234", 14, "…", "Ship CORE-1234"},
		{"a multibyte line", "界界界 CORE-1234 界", 8, "", "界界界"},
		{"a multibyte mention", "界 [@计划者](dispatch://agent/s1) 界", 5, "", "界"},
		{"a reference on the line after another", "First line.\nShip CORE-1234 today", 19, "", "First line.\nShip"},
		{"a text opening with a reference past the bound", "dispatch://CORE-1/artifact/notes is long", 10, "…", "…"},
		{"an excerpt opening with a reference past the bound", "https://example.com/" + strings.Repeat("x", 300), 240, "", ""},
		{"a cut that moves to the start of its line", "First line.\nCORE-1234 today", 15, "", "First line."},
		{"an address the ellipsis would join across a line break", "Read dispatch://CORE-1\nCORE-1234 today", 27, "…", "Read…"},
		{"an address the ellipsis would join across a blank line", "Read dispatch://CORE-1\n\nCORE-1234 today", 28, "…", "Read…"},
		{"a cut that moves to the start of a line after a blank one", "First line.\n\nCORE-1234 today", 16, "…", "First line.…"},
		{"a cut in the words of a link that wrap", "See [the spec for\nthis part](dispatch://CORE-1/spec) now", 20, "", "See"},
		{"a cut in the address of a link whose words wrap", "See [the spec for\nthis part](dispatch://CORE-1/spec) now", 35, "", "See"},
		{"a blank line ends a link's words", "See [the spec\n\nfor](dispatch://CORE-1/spec) now", 17, "", "See [the spec\n\nfo"},
		{"a line of spaces ends a link's words", "See [the spec\n  \nfor](dispatch://CORE-1/spec) now", 19, "", "See [the spec\n  \nfo"},
	} {
		if got := HeadRefs(row.value, row.limit, row.ellipsis); got != row.want {
			t.Errorf("%s: HeadRefs(%q, %d, %q) = %q, want %q", row.name, row.value, row.limit, row.ellipsis, got, row.want)
		}
	}
}
