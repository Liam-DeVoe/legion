package pmdoc

// The email-address shapes of the round 6 deep review of PR #4369 (INFRA-4224, finding 1): its
// generator, rebuilt with its draws unchanged, at seed 4369 and bound 20,000. Main e4584fae stores
// 19,914 of the shapes; TestWriteStoresEveryReviewShapeMainStores holds the write to every one
// main stores.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// reviewShapeSeed and reviewShapeBound fix the draw: rand.New(rand.NewPCG(4369, 4369^0x4224)),
// the first 20,000 distinct markdowns.
const (
	reviewShapeSeed  = 4369
	reviewShapeBound = 20000
)

// reviewShapeBefore is what stands before a local part, one to three entries in a row.
var reviewShapeBefore = []string{"", "", " ", "x ", "q", "é", "/", "a/", "q/", "-", ".", "+", "_", "%", "'", "&", "&amp;", "&#45;", "&#x2D;", "&period;", "&lowbar;", "&#64;", `\-`, `\_`, `\.`, `\*`, "www.", "_www.", "b-_www.", "www.a.b", "http://", "https://", "http://a.b/", "ftp://", "mailto:", "x@y.co", "x@y.co/", "a@b.co+", "*e*", "**b**", "_e_", "`c`", "~~s~~", "<b>", "</b>", "<br>", "[l](http://a.b)", "[x@y.co](mailto:x@y.co)", "<x@y.co>", "<http://a.b>", "(", ")", "[", "]", "!", "#", "$", "=", "?", "^", "{", "}", "~", ":", ";", ",", "\"", "*", "**", "a.b", "a-b", "www.x.y/"}

// reviewShapeLocal is what a local part is made of, one to three entries in a row.
var reviewShapeLocal = []string{"a", "hosts", "agent-hosts", "b.c", "www.", "www.a.b", "_www.", "-", ".", "+", "_", "%", "&", "'", "/", "!", "#", "$", "=", "?", "^", "`", "{", "}", "~", "*", "&#45;", "&amp;", "&period;", `\.`, `\_`, `\-`, "é", "9", "Z", "http://", "x@y.co"}

// reviewShapeDomain is the domain after the `@`, one entry.
var reviewShapeDomain = []string{"example.com", "e.co", "x_y.com", "a.b", "www.e.co", "example.com.", "ex-ample.co.uk", "a_b.c", "b", "a.b.c.test", "e.co-", "e.c_o", "e.co_"}

// reviewShapeAfter is what follows the domain, one entry.
var reviewShapeAfter = []string{"", "", " y", ".", "..", "/x", "-", "_", "%", "*", "**", "`", "&", ")", "]", "@d.co", "/bob-smith@example.com", "/b-c@d.co", ".www.q.com", "&#64;e.co", " and y@z.co", ",c@d.co", "+c@d.co", "-c@d.co", "_c@d.co", "&#45;c@d.co", "&amp;c@d.co", "'c@d.co", "%c@d.co", "?x", ":x", "~", "<br>", "/www.q.r"}

// reviewShapeWrappers is what each segment is wrapped in, one entry per segment.
var reviewShapeWrappers = []string{"%s", "%s", "%s", "*%s*", "**%s**", "_%s_", "~~%s~~", "[%s](http://l.k)", "`x` %s", "%s `y`"}

// reviewShapeContainers is the block a shape stands in: 14 entries, the paragraph twice.
var reviewShapeContainers = []struct{ name, format string }{
	{"a paragraph", "%s\n"},
	{"a paragraph", "%s\n"},
	{"a heading", "# %s\n"},
	{"a list item", "- %s\n"},
	{"a nested list item", "- a\n  - %s\n"},
	{"a quote", "> %s\n"},
	{"a list item in a quote", "> - %s\n"},
	{"a table cell", "| a |\n| --- |\n| %s |\n"},
	{"a typed block", ":::callout{#c1 kind=\"note\" title=\"T\"}\n%s\n:::\n"},
	{"an ordered list item", "1. %s\n"},
	{"a line after a backslash hard break", "x\\\n%s\n"},
	{"a footnote definition", "ref[^1]\n\n[^1]: %s\n"},
	{"a task item", "- [ ] %s\n"},
	{"a line after a two-space hard break", "x  \n%s\n"},
}

// reviewShapesSHA256 is the sha256 of the drawn markdowns, each followed by a NUL, so a change to
// the generator that would move main's verdicts below fails the test before it reads them.
const reviewShapesSHA256 = "6ee575d9ed1a807274b49e6563244328eac61da1cca5a2ddb19bf24ab3f0ab17"

// mainRefusedReviewShapes are the indexes of the shapes main e4584fae refuses to store, 86 of the
// 20,000; main stores every other one. They are main's own verdicts, the base's write itself: the
// review's program took them on 2026-11-20, on a `git archive e4584fae` copy of packages/envoy,
// by calling main's ParseForWrite(markdown, nil) on each row of the file
// PMDOC_REVIEW_SHAPES_OUT writes (TestWriteStoresEveryReviewShapeMainStores) and listing each
// index where it answers an error. To take them again, write that file at this build and run the
// same call over its rows at main.
var mainRefusedReviewShapes = []int{
	172, 202, 377, 423, 912, 1283, 1391, 1762, 2001, 2140, 2313, 2732, 2882, 3005, 3194, 3243, 3359,
	3731, 4038, 4062, 4292, 4546, 4726, 4871, 4985, 5092, 5274, 5475, 6153, 6185, 6617, 6914, 7187,
	7526, 7714, 8318, 8827, 9021, 9116, 9309, 9343, 10332, 10668, 10792, 10940, 11431, 11753, 11854,
	12023, 12092, 12221, 12308, 12682, 12699, 12834, 12993, 13053, 13056, 13346, 13852, 13901,
	14115, 14507, 14914, 15033, 15662, 15796, 16216, 16731, 16739, 16953, 17601, 17878, 18118,
	18304, 18346, 18503, 18532, 18556, 18861, 19100, 19303, 19636, 19640, 19851, 19968,
}

// reviewShape is one drawn shape: the container's name and the markdown.
type reviewShape struct{ container, markdown string }

// reviewShapeSegment is one segment: what stands before the local part, the local part, an `@`,
// the domain and what follows it.
func reviewShapeSegment(draw *rand.Rand) string {
	return drawEntries(draw, reviewShapeBefore, 3) + drawEntries(draw, reviewShapeLocal, 3) + "@" + reviewShapeDomain[draw.IntN(len(reviewShapeDomain))] + reviewShapeAfter[draw.IntN(len(reviewShapeAfter))]
}

// reviewShapes draws the shapes: one to three segments, each wrapped; a text opening `<?`, `<!` or
// `:` is dropped; then a container, a table cell's `|` escaped and a text with a backslash before
// a `|` or at its end drawn in no cell; each markdown once.
func reviewShapes() []reviewShape {
	draw := rand.New(rand.NewPCG(reviewShapeSeed, reviewShapeSeed^0x4224))
	seen := map[string]bool{}
	var out []reviewShape
	for len(out) < reviewShapeBound {
		var text string
		for range 1 + draw.IntN(3) {
			seg := reviewShapeSegment(draw)
			text += fmt.Sprintf(reviewShapeWrappers[draw.IntN(len(reviewShapeWrappers))], seg)
		}
		if strings.HasPrefix(text, "<?") || strings.HasPrefix(text, "<!") || strings.HasPrefix(text, ":") {
			continue
		}
		container := reviewShapeContainers[draw.IntN(len(reviewShapeContainers))]
		if strings.HasPrefix(container.format, "|") {
			if strings.Contains(text, `\|`) || strings.HasSuffix(text, "\\") {
				continue
			}
			text = strings.ReplaceAll(text, "|", `\|`)
		}
		md := fmt.Sprintf(container.format, text)
		if seen[md] {
			continue
		}
		seen[md] = true
		out = append(out, reviewShape{container.name, md})
	}
	return out
}

// writeReviewShapes writes each shape to path as one JSON object a line, the rows main's verdicts
// were taken on (mainRefusedReviewShapes): i, container, markdown and main_stored.
func writeReviewShapes(t *testing.T, path string, shapes []reviewShape, mainRefuses map[int]bool) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	out := json.NewEncoder(file)
	out.SetEscapeHTML(false)
	for index, shape := range shapes {
		row := struct {
			I          int    `json:"i"`
			Container  string `json:"container"`
			Markdown   string `json:"markdown"`
			MainStored bool   `json:"main_stored"`
		}{index, shape.container, shape.markdown, !mainRefuses[index]}
		if err := out.Encode(row); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// R4 (INFRA-4224) stores every text the base stores. The deep review's generator (reviewShapes)
// draws 20,000 shapes, and for each of the 19,914 main e4584fae stores (all but
// mainRefusedReviewShapes), a write of it (ParseWritten, through parseWritten, which also answers
// the tier of the fallback that gave the tree) is not refused, and the text it stores reads back
// as the tree it stores: under the rule, or, for a tree of the fallback's last tier
// (baseStoredTier), whose text the rule reads as another tree, under the base reader, as main
// reads it. The log counts the shapes each tier stored.
//
// With PMDOC_REVIEW_SHAPES_FULL=1 it also checks what the review checked of each stored shape: a
// second write of the stored text, over the stored tree, stores the same tree and text, and the
// browser editor's engine (engineReadings, gen/differential.ts; bun on PATH, or CI=1) reads the
// stored text as Go's rule reads it, which is the stored tree but in the last tier. A text of the
// last tier the rule reads as another tree may be stored as that tree by a second write, as
// TestTheFallbacksLastTierIsAFixedPoint holds: where it is, that tree reads back as itself without
// the fallback, a third write keeps its text, and the log names the shape. With
// PMDOC_REVIEW_SHAPES_OUT=<path> it writes every shape to path (writeReviewShapes), the file main's
// verdicts are taken on. Every failure is counted; the first few are shown.
func TestWriteStoresEveryReviewShapeMainStores(t *testing.T) {
	const shown = 40
	began := time.Now()
	shapes := reviewShapes()
	sum := sha256.New()
	for _, shape := range shapes {
		sum.Write([]byte(shape.markdown))
		sum.Write([]byte{0})
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != reviewShapesSHA256 {
		t.Fatalf("the generator draws shapes with sha256 %s, not %s: main's verdicts no longer name these shapes", got, reviewShapesSHA256)
	}
	mainRefuses := make(map[int]bool, len(mainRefusedReviewShapes))
	for _, index := range mainRefusedReviewShapes {
		mainRefuses[index] = true
	}
	if path := os.Getenv("PMDOC_REVIEW_SHAPES_OUT"); path != "" {
		writeReviewShapes(t, path, shapes, mainRefuses)
	}
	full := os.Getenv("PMDOC_REVIEW_SHAPES_FULL") != ""
	failures := 0
	fail := func(format string, args ...any) {
		t.Helper()
		if failures++; failures <= shown {
			t.Errorf(format, args...)
		}
	}
	// A shape the write stored, in the full mode: where it was drawn, the tree stored, the text
	// stored and its read-back, and the tier that gave the tree.
	type stored struct {
		index int
		tree  *Node
		read  WrittenReadBack
		tier  fallbackTier
	}
	var kept []stored
	var mainStores, refused, misread int
	var tiers [baseStoredTier + 1]int
	for index, shape := range shapes {
		if mainRefuses[index] {
			continue
		}
		mainStores++
		tree, read, tier, err := parseWritten(shape.markdown, nil)
		if err != nil {
			refused++
			fail("shape %d in %s: main stores %q, and a write of it is refused: %v", index, shape.container, shape.markdown, err)
			continue
		}
		tiers[tier]++
		if tier == baseStoredTier {
			if base, err := parseRenderingAsBase(read.Markdown); err != nil || !readsBackAsWritten(tree, base) {
				misread++
				fail("shape %d: a write of %q stores %q through the last tier, which the base reads back as %s (%v), not as the stored tree %s", index, shape.markdown, read.Markdown, inlineSummaries(base), err, inlineSummaries(tree))
			}
		} else if !read.Tree.Equal(tree) {
			misread++
			fail("shape %d: a write of %q stores %q through tier %d, which Go reads back as %s, not as the stored tree %s", index, shape.markdown, read.Markdown, tier, inlineSummaries(read.Tree), inlineSummaries(tree))
		}
		if full {
			kept = append(kept, stored{index, tree, read, tier})
		}
	}
	headStores := mainStores - refused
	t.Logf("main stores %d of %d shapes; the write stores %d and refuses %d, %d stored as parsed, %d through the base's links, %d through the base's reading and %d through the base's stored text; %d read back otherwise; in %v",
		mainStores, len(shapes), headStores, refused, tiers[noFallback], tiers[baseLinksTier], tiers[baseReadingTier], tiers[baseStoredTier], misread, time.Since(began))
	if full {
		rewritten, settled, engineDiffers := 0, 0, 0
		texts := make([]string, len(kept))
		for at, shape := range kept {
			texts[at] = shape.read.Markdown
			again, readAgain, againTier, err := parseWritten(shape.read.Markdown, shape.tree)
			if err == nil && again.Equal(shape.tree) && readAgain.Markdown == shape.read.Markdown {
				continue
			}
			if err == nil && shape.tier == baseStoredTier && againTier == noFallback && readAgain.Tree.Equal(again) {
				if _, third, _, err := parseWritten(readAgain.Markdown, again); err == nil && third.Markdown == readAgain.Markdown {
					settled++
					t.Logf("shape %d: %q is stored as %q through the last tier, which a second write stores as %q, the rule's reading %s, and a third write keeps", shape.index, shapes[shape.index].markdown, shape.read.Markdown, readAgain.Markdown, inlineSummaries(again))
					continue
				}
			}
			rewritten++
			fail("shape %d: %q is stored as %q through tier %d, which a second write stores as %q through tier %d (%v)", shape.index, shapes[shape.index].markdown, shape.read.Markdown, shape.tier, readAgain.Markdown, againTier, err)
		}
		for at, engine := range engineReadings(t, texts) {
			shape := kept[at]
			want := shape.tree
			if shape.tier == baseStoredTier {
				want = shape.read.Tree
			}
			if engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(want)) {
				engineDiffers++
				fail("shape %d: the engine reads the stored %q as %s, and Go as %s", shape.index, shape.read.Markdown, inlineSummaries(engine), inlineSummaries(want))
			}
		}
		t.Logf("of the %d stored, a second write stores %d otherwise, %d through the last tier settle on a second write, and the engine reads %d otherwise than Go; in %v", len(kept), rewritten, settled, engineDiffers, time.Since(began))
	}
	if failures > shown {
		t.Errorf("%d failures, the first %d shown", failures, shown)
	}
}

// linksOff is doc as written (asWritten) with every link mark taken off: two trees that differ in
// their links alone are one tree here.
func linksOff(doc *Node) *Node {
	out := cloneNode(doc)
	Walk(out, func(node *Node) bool {
		node.Marks = withoutMark(node.Marks, "link")
		return true
	})
	return asWritten(out)
}

// The fallback changes nothing but links in a text the base refuses. Of the shapes main e4584fae
// refuses (mainRefusedReviewShapes), most read back otherwise in a way the base reads the same:
// emphasis beside an address read back with its delimiters as text, or without its emphasis. The
// live path's second tier took the base's reading of the rendering for the tree, so the fallback
// of the rule's tree (FallBackEmailLinks of Parse) and an edit inserting the shape after a
// paragraph (FallBackMisreadWrite) stored 46 of the 62 Parse reads with text no one wrote,
// `_$}@e.co_` as `*$}@e.co*` among them, and one more, shape 2140, without the emphasis on its
// `*#*`, where main refuses the edit. For each shape Parse reads, the tree the live path answers,
// and the tree an edit stores where it is not refused, is the rule's tree but for its links: the
// same blocks, text, marks and attributes (linksOff).
func TestTheFallbackChangesOnlyLinksOfAReviewShapeMainRefuses(t *testing.T) {
	shapes := reviewShapes()
	before := parseFixture(t, "First.\n")
	read, refused := 0, 0
	for _, index := range mainRefusedReviewShapes {
		markdown := shapes[index].markdown
		doc, err := Parse(markdown)
		if err != nil {
			continue
		}
		read++
		live, _, err := FallBackEmailLinks(doc)
		if err != nil {
			t.Errorf("shape %d: FallBackEmailLinks(Parse(%q)): %v", index, markdown, err)
		} else if !linksOff(live).Equal(linksOff(doc)) {
			t.Errorf("shape %d: the fallback of Parse(%q) = %s, which differs from %s in more than its links", index, markdown, inlineSummaries(live), inlineSummaries(doc))
		}
		after := &Node{Type: "doc", Children: append(slices.Clone(before.Children), doc.Children...)}
		stored, err := FallBackMisreadWrite(before, after)
		if err != nil {
			refused++
			continue
		}
		if !linksOff(stored).Equal(linksOff(after)) {
			t.Errorf("shape %d: an edit inserting %q stores %s, which differs from %s in more than its links", index, markdown, inlineSummaries(stored), inlineSummaries(after))
		}
	}
	t.Logf("Parse reads %d of the %d shapes main refuses; an edit inserting one is refused for %d", read, len(mainRefusedReviewShapes), refused)
}
