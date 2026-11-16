package text

import (
	"strings"
	"testing"
	"time"
)

// headRefsRow is one cut HeadRefs is held to: value cut at limit runes with ellipsis, read against
// the deployment at server ("" where the caller knows no origin).
type headRefsRow struct {
	name     string
	value    string
	limit    int
	ellipsis string
	server   string
	want     string
}

// linkLead is the 140 runes and the space a link of the link rows stands after, so a cut at 160
// runes falls inside the link.
var linkLead = strings.Repeat("x", 140)

// linkTails is a single line of 1 MiB that is nothing but link tails, `](dispatch://CORE-1)`: a
// body every one of whose addresses stands where a link's would, and none of which a `[` opens.
var linkTails = strings.Repeat("](dispatch://CORE-1)", (1<<20)/len("](dispatch://CORE-1)"))

// TestHeadRefsNeverSplitsAReference holds HeadRefs to its rule: a reference that crosses the cut
// moves the cut back to its start, and so does one the head would read that the text does not
// write, while a text with no reference at the cut, a URL of anywhere else, and a reference no cut
// can keep whole are cut as HeadRunes cuts them.
func TestHeadRefsNeverSplitsAReference(t *testing.T) {
	githubURL := "https://github.com/cybermonklabs/legion/actions/runs/" + strings.Repeat("1", 130)
	longAddress := "dispatch://CORE-1/artifact/" + strings.Repeat("x", 200)
	externalHead := "https://example.com/" + strings.Repeat("x", 300)
	unit := "](dispatch://CORE-1)"
	for _, row := range []headRefsRow{
		{"a mention crossing the cut", "Ask [@Planner](dispatch://agent/s1) now", 10, "", "", "Ask"},
		{"a mention crossing the cut of a line", "Ask [@Planner](dispatch://agent/s1) now", 11, "…", "", "Ask…"},
		{"a mention crossing the cut at its address", "Ask [@Planner](dispatch://agent/s1) now", 20, "", "", "Ask"},
		{"a mention whose words escape a bracket", `Ask [@a\[b](dispatch://agent/s1) now`, 14, "", "", "Ask"},
		{"a cut in the words of a link whose words balance brackets", "Ask [@Alice [x]](dispatch://agent/s1) now", 10, "", "", "Ask"},
		{"a picture crossing the cut", "See ![chart](dispatch://CORE-1/artifact/chart) now", 8, "", "", "See"},
		{"a key in a link's words crossing the cut", "See [CORE-1234](dispatch://CORE-1234) now", 9, "", "", "See"},
		{"a bare address crossing the cut", "Read dispatch://CORE-1/spec first", 12, "", "", "Read"},
		{"a bare key crossing the cut", "Ship CORE-1234 today", 12, "", "", "Ship"},
		{"a bare key crossing the cut of a line", "Ship CORE-1234 today", 13, "…", "", "Ship…"},
		{"a dashboard URL crossing the cut", "Open https://dispatch.example/issues/CORE-1 now", 20, "", "https://dispatch.example", "Open"},
		{"a dashboard URL the ellipsis would join", "Open https://dispatch.example/issues/CORE-1 now", 44, "…", "https://dispatch.example", "Open…"},
		{"a dashboard URL of no known origin is cut as runes", "Open https://dispatch.example/issues/CORE-1 now", 20, "", "", "Open https://dispatc"},
		{"an external URL crossing the cut is cut as runes", "Open https://example.com/issues/CORE-1 now", 20, "", "https://dispatch.example", "Open https://example"},
		{"a question id crossing the cut", "See #q12 now", 7, "", "", "See"},
		{"a key ending at the bound", "Ship CORE-1234 today", 14, "", "", "Ship CORE-1234"},
		{"a key ending at the bound of a line", "Ship CORE-1234 today", 15, "…", "", "Ship CORE-1234…"},
		{"a mention ending at the bound", "Ask [@Planner](dispatch://agent/s1) now", 35, "", "", "Ask [@Planner](dispatch://agent/s1)"},
		{"a mention ending at the bound of a line", "Ask [@Planner](dispatch://agent/s1) now", 36, "…", "", "Ask [@Planner](dispatch://agent/s1)…"},
		{"an address ending at the bound", "Read dispatch://CORE-1 first", 22, "", "", "Read dispatch://CORE-1"},
		{"an address the ellipsis would join", "Read dispatch://CORE-1 first", 23, "…", "", "Read…"},
		{"a key the cut would make of no key", "Ship CORE-12x4 today", 12, "", "", "Ship"},
		{"a key the cut would make of a decimal", "Ship CORE-1.5 today", 11, "", "", "Ship"},
		{"a line with no reference", "The quick brown fox", 9, "", "", "The quick"},
		{"a line with no reference cut after a space", "The quick brown fox", 4, "", "", "The "},
		{"a line with no reference and an ellipsis", "The quick brown fox", 10, "…", "", "The quick…"},
		{"a line at the bound", "Ship CORE-1234", 14, "…", "", "Ship CORE-1234"},
		{"a multibyte line", "界界界 CORE-1234 界", 8, "", "", "界界界"},
		{"a multibyte mention", "界 [@计划者](dispatch://agent/s1) 界", 5, "", "", "界"},
		{"a reference on the line after another", "First line.\nShip CORE-1234 today", 19, "", "", "First line.\nShip"},
		{"a text opening with an address past the bound keeps its rune cut", "dispatch://CORE-1/artifact/notes is long", 10, "…", "", "dispatch:…"},
		{"an excerpt opening with an external URL past the bound is cut as runes", externalHead, 240, "", "", externalHead[:240]},
		{"a first line opening with a long GitHub URL is cut as runes", githubURL + " failed again", 160, "…", "", githubURL[:159] + "…"},
		{"a first line opening with a long address keeps its rune cut", longAddress + " is long", 160, "…", "", longAddress[:159] + "…"},
		{"a cut that moves to the start of its line", "First line.\nCORE-1234 today", 15, "", "", "First line."},
		{"an address the ellipsis would join across a line break", "Read dispatch://CORE-1\nCORE-1234 today", 27, "…", "", "Read…"},
		{"an address the ellipsis would join across a blank line", "Read dispatch://CORE-1\n\nCORE-1234 today", 28, "…", "", "Read…"},
		{"a cut that moves to the start of a line after a blank one", "First line.\n\nCORE-1234 today", 16, "…", "", "First line.…"},
		{"a cut in the words of a link that wrap", "See [the spec for\nthis part](dispatch://CORE-1/spec) now", 20, "", "", "See"},
		{"a cut in the address of a link whose words wrap", "See [the spec for\nthis part](dispatch://CORE-1/spec) now", 35, "", "", "See"},
		{"a blank line ends a link's words", "See [the spec\n\nfor](dispatch://CORE-1/spec) now", 17, "", "", "See [the spec\n\nfo"},
		{"a line of spaces ends a link's words", "See [the spec\n  \nfor](dispatch://CORE-1/spec) now", 19, "", "", "See [the spec\n  \nfo"},
		{"a mention with a title", linkLead + ` [@Alice](dispatch://agent/s1 "t") now`, 160, "…", "", linkLead + "…"},
		{"a mention whose address is in angle brackets", linkLead + " [@Alice](<dispatch://agent/s1>) now", 160, "…", "", linkLead + "…"},
		{"a key's link with a single-quoted title", linkLead + " [see CORE-1](dispatch://CORE-1 'x') now", 160, "…", "", linkLead + "…"},
		{"an address longer than the window is cut by runes", "Read " + longAddress + strings.Repeat("x", 1000) + " now", 160, "…", "",
			("Read " + longAddress + strings.Repeat("x", 1000))[:159] + "…"},
		{"a line of 1 MiB cut as a first line", linkTails, 160, "…", "", strings.Repeat(unit, 7) + "](…"},
		{"a line of 1 MiB cut as an excerpt", linkTails, 240, "", "", strings.Repeat(unit, 12)},
	} {
		if got := HeadRefs(row.value, row.limit, row.ellipsis, row.server); got != row.want {
			t.Errorf("%s: HeadRefs(%.80q, %d, %q, %q) = %.200q, want %.200q",
				row.name, row.value, row.limit, row.ellipsis, row.server, got, row.want)
		}
	}
}

// TestHeadRefsCostsTheSameOnABodyOfAnyLength holds a cut to the window it reads: 50 cuts of a
// single line of 1 MiB - one of link tails, one of keys - take well under a second, where reading
// the whole line took seconds for each cut. It stops at the first cut past the bound, so a cut
// that reads the whole line fails it in that cut's time rather than in all fifty's.
func TestHeadRefsCostsTheSameOnABodyOfAnyLength(t *testing.T) {
	keys := strings.Repeat("CORE-1 ", (1<<20)/len("CORE-1 "))
	for _, body := range []struct{ name, text string }{{"link tails", linkTails}, {"keys", keys}} {
		began := time.Now()
		for cut := range 50 {
			HeadRefs(body.text, 160+cut*7, []string{"…", ""}[cut%2], "https://dispatch.example")
			if took := time.Since(began); took > time.Second {
				t.Fatalf("a line of %d bytes of %s: %d cuts took %s, want 50 of them well under a second",
					len(body.text), body.name, cut+1, took)
			}
		}
	}
}
