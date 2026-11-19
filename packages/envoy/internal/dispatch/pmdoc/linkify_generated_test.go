package pmdoc

import (
	"fmt"
	classicrand "math/rand"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// emailShapeSets fix the shapes TestLinkifyStoresEveryGeneratedAddress builds, so a failure
// reproduces: each seed of the generator, and how many shapes it draws from it. Each bound is the
// smallest that still draws its seed's shapes whose earlier link the renderer cannot write bare
// (explicitOnlyNote, testdata/refused-email-shapes.jsonl), which the run rule refuses without its
// second exception (writtenExplicitOnly): seed 4224's 11,598th shape, seed 7's 2,921st and
// 12,879th, and seed 99's 4,760th.
var emailShapeSets = []struct {
	seed  uint64
	bound int
}{{4224, 11598}, {7, 12879}, {99, 4760}}

// emailShapeBefore is what the generator sets before an address, one to three of them in a row:
// nothing, whitespace, each ASCII punctuation character, a letter outside ASCII, character
// references, backslash escapes, a www link, an http:// link, another bare address, emphasis,
// code, inline HTML and a link the writer wrote, and the letters a path is made of.
var emailShapeBefore = func() []string {
	before := []string{"", " ", "x ", "q", "é", "&#45;", "&amp;", "&period;", "&lowbar;", "&#x2D;",
		`\-`, `\_`, `\.`, `\*`, "\\`", `\~`, "www.", "www.a.b", "www.q.r", "http://h.i/", "http://a.b",
		"x@y.co", "@e.co", "*em*", "_em_", "**b**", "`c`", "<b>", "</b>", "<br>", "[l](http://a.b)",
		"[x@y.co](mailto:x@y.co)"}
	for c := byte('!'); c <= '~'; c++ {
		if !isASCIIAlphaNumeric(c) {
			before = append(before, string(c))
		}
	}
	return before
}()

// emailShapeLocal is what a local part is drawn from, one to three of them in a row: words, GFM's
// local-part punctuation (`-`, `.`, `+`, `_`), RFC 5322's other atext characters, and runs that
// start with `www.`.
var emailShapeLocal = append([]string{"hosts", "a", "b.c", "agent-hosts", "www.", "www.a.b", "www.a.bhosts", "www.hosts", "_www.a.b"},
	strings.Split("-.+_!#$%&'*/=?^`{|}~", "")...)

// emailShapeAfter is what the generator sets after the address's domain.
var emailShapeAfter = []string{"", "", " y", ".", "-", "%", "`", "*", "</b>", `\-`, "/x", ",c@d.co"}

// emailShapeContainers is each block a shape is read in.
var emailShapeContainers = []struct{ name, format string }{
	{"a paragraph", "%s\n"},
	{"a list item", "- %s\n"},
	{"a quote", "> %s\n"},
	{"a typed block", ":::callout{#c1 kind=\"note\" title=\"T\"}\n%s\n:::\n"},
	{"a table cell", "| a |\n| --- |\n| %s |\n"},
}

// emailShape is one text the generator built, the container it stands in, and the markdown of both.
type emailShape struct{ text, container, markdown string }

// emailShapes is bound shapes drawn from the seed: what stands before an address, its local
// part, its domain and what follows it, in one of the containers. A table cell's `|` is escaped,
// so the shape stays one cell, and a shape with a backslash before a `|` is drawn in no cell.
// Each markdown is drawn once, and none that opens with what Parse refuses whatever address
// follows: an HTML block (`<?`, `<!`) or a text directive (`:name{`).
func emailShapes(seed uint64, bound int) []emailShape {
	draw := rand.New(rand.NewPCG(seed, seed))
	pick := func(from []string, most int) string {
		var b strings.Builder
		for range 1 + draw.IntN(most) {
			b.WriteString(from[draw.IntN(len(from))])
		}
		return b.String()
	}
	domains := []string{"example.com", "e.co", "d.co"}
	seen := map[string]bool{}
	var shapes []emailShape
	for len(shapes) < bound {
		text := pick(emailShapeBefore, 3) + pick(emailShapeLocal, 3) + "@" + domains[draw.IntN(len(domains))] + emailShapeAfter[draw.IntN(len(emailShapeAfter))]
		if strings.HasPrefix(text, "<?") || strings.HasPrefix(text, "<!") || strings.HasPrefix(text, ":") && strings.Contains(text, "{") {
			continue
		}
		container := emailShapeContainers[draw.IntN(len(emailShapeContainers))]
		if container.name == "a table cell" {
			if strings.Contains(text, `\|`) {
				continue
			}
			text = strings.ReplaceAll(text, "|", `\|`)
		}
		markdown := fmt.Sprintf(container.format, text)
		if seen[markdown] {
			continue
		}
		seen[markdown] = true
		shapes = append(shapes, emailShape{text, container.name, markdown})
	}
	return shapes
}

// R4 (INFRA-4224): an email address in a document's text is stored either as written or as one
// link whose text and mailto: target are the whole address, or, where markup, another link or a
// web address splits it, as the reader linked it, so no write of one is refused. The rows of
// TestLinkifyLinksAnEmailWholeOrNotAtAll and TestLinkifyStoresEveryRefusedShape are the cases
// someone thought of; this test builds the shapes the rule reads (wholeEmailLinks) from what can
// stand before an address, what its local part holds and the block it stands in, and holds each
// to it: Parse reads it, a write of it is not refused (ParseForWrite), its rendering reads back
// as its tree, and the browser editor's engine reads the markdown and the rendering as Go reads
// the markdown. The shapes come from fixed seeds, so a failure reproduces; the test stops after a
// few failures. The engine reads each distinct markdown once.
func TestLinkifyStoresEveryGeneratedAddress(t *testing.T) {
	const most = 5
	began := time.Now()
	failures := 0
	fail := func(seed uint64, index int, shape emailShape, format string, args ...any) {
		t.Helper()
		t.Errorf("seed %d, shape %d: %q in %s: %s", seed, index, shape.text, shape.container, fmt.Sprintf(format, args...))
		if failures++; failures == most {
			t.Fatalf("stopped after %d failures", most)
		}
	}
	// A shape drawn, where it was drawn, Go's reading of it, and where its markdown and its
	// rendering stand among the engine's markdowns.
	type drawn struct {
		emailShape
		seed                    uint64
		index                   int
		doc                     *Node
		atMarkdown, atRendering int
	}
	var shapes []drawn
	var markdowns []string
	engineIndex := map[string]int{}
	forEngine := func(markdown string) int {
		index, ok := engineIndex[markdown]
		if !ok {
			index = len(markdowns)
			engineIndex[markdown] = index
			markdowns = append(markdowns, markdown)
		}
		return index
	}
	for _, set := range emailShapeSets {
		for index, shape := range emailShapes(set.seed, set.bound) {
			doc, err := Parse(shape.markdown)
			if err != nil {
				fail(set.seed, index, shape, "Parse(%q): %v", shape.markdown, err)
				continue
			}
			if _, err := ParseForWrite(shape.markdown, nil); err != nil {
				fail(set.seed, index, shape, "a write of %q is refused: %v", shape.markdown, err)
			}
			rendering := mustRender(t, doc)
			if back, err := Parse(rendering); err != nil || !back.Equal(doc) {
				fail(set.seed, index, shape, "%q is written %q, which Go reads back as %s (%v), not %s", shape.markdown, rendering, inlineSummaries(back), err, inlineSummaries(doc))
			}
			shapes = append(shapes, drawn{emailShape: shape, seed: set.seed, index: index, doc: doc, atMarkdown: forEngine(shape.markdown), atRendering: forEngine(rendering)})
		}
	}
	t.Logf("seeds and bounds %v: %d shapes read and written in %v", emailShapeSets, len(shapes), time.Since(began))
	readings := engineReadings(t, markdowns)
	for _, shape := range shapes {
		for _, at := range []int{shape.atMarkdown, shape.atRendering} {
			if engine := readings[at]; engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(shape.doc)) {
				fail(shape.seed, shape.index, shape.emailShape, "the engine reads %q as %s, and Go reads %q as %s", markdowns[at], inlineSummaries(engine), shape.markdown, inlineSummaries(shape.doc))
			}
		}
	}
	t.Logf("seeds and bounds %v: %d shapes, %d distinct markdowns for the engine, in %v", emailShapeSets, len(shapes), len(markdowns), time.Since(began))
}

// testerShapes is what the tester's own generator draws, the generator that found the two shapes
// of TestLinkifyStoresTheShapesTheTesterFoundRefused: for each of its seeds, 3,000 draws of a
// prefix, one to six local-part pieces, an `@`, a domain and a suffix, in one of its contexts,
// each distinct markdown once. Its source is math/rand's classic one, rand.New(rand.NewSource(seed)),
// kept rather than math/rand/v2's because the shapes it found are the stream that source gives.
func testerShapes() []string {
	prefixes := []string{"", "x ", "q/", "a/b/", "key=", "--account=", "(", "\"", "www.", "http://", "https://",
		"&#45;", "&amp;", "*e*", "`c`", "[t](mailto:a@b.co) ", "<a@b.co>", "_www.", "alice@example.com/", "see: ",
		"é", "$HOME/", "~", "a&#64;b.co ", "http://x.co/", "q/&#45;", "x;http://", "ftp://"}
	locals := []string{"a", "b", "Z", "9", "-", ".", "+", "_", "%", "&", "'", "/", "!", "#", "$", "=", "?", "^",
		"~", "|", "{", "}", "*", "`", "&#45;", "&amp;", "&#x2D;", "&period;", ";", ":", " ", "www.", "\\.", "\\_", "é"}
	domains := []string{"example.com", "c.co", "b", "a.b.c.test", "ex-ample.co.uk", "x_y.com", "example.com."}
	suffixes := []string{"", ".", "/x", " and y@z.co", "-", "_", "@d.co", ")", "/bob-smith@example.com", ".www.q.com"}
	contexts := []string{"%s", "- %s", "> %s", "| a |\n| --- |\n| %s |", "**%s**", "# %s"}
	seen := map[string]bool{}
	var shapes []string
	for _, seed := range []int64{11, 23, 4242} {
		draw := classicrand.New(classicrand.NewSource(seed))
		for range 3000 {
			var b strings.Builder
			b.WriteString(prefixes[draw.Intn(len(prefixes))])
			for n := 1 + draw.Intn(6); n > 0; n-- {
				b.WriteString(locals[draw.Intn(len(locals))])
			}
			b.WriteString("@")
			b.WriteString(domains[draw.Intn(len(domains))])
			b.WriteString(suffixes[draw.Intn(len(suffixes))])
			shape := strings.Replace(contexts[draw.Intn(len(contexts))], "%s", b.String(), 1) + "\n"
			if !seen[shape] {
				seen[shape] = true
				shapes = append(shapes, shape)
			}
		}
	}
	return shapes
}

// baseParseRefusal is the base's refusal of a write of markdown, its parse (parseForWrite) read
// with the base's email links (baseEmailReader), as a write read an address before Dispatch's
// rule, or nil where that parse reads it. The write's check of its rendering is not the base's
// here: this build's renderer spells a run for the rule's reader (runSpelling), so the base's
// read-back of its rendering would refuse text the base stores, as it reads `**see:
// a&~&#x2D;@a.b.c.test_**`, rendered `**see: a&~-@a.b.c.test\_**`, with a link on
// `-@a.b.c.test`.
func baseParseRefusal(markdown string) error {
	_, err := parseMarkdown(baseEmailReader, LineFeeds(markdown), fromStart, true, NewWriteBudget(), nil)
	return err
}

// R4 (INFRA-4224) stores every text the base stores, each address as the rule reads it or, where
// that tree has no spelling that reads back, as the base reads it (FallBackEmailLinks). The
// tester's generator (testerShapes) draws 8,994 distinct shapes, and for every one the base's
// parse reads (baseParseRefusal), a write of it is not refused (ParseWritten), the text it stores
// reads back as the tree it stores, and the browser editor's engine reads that text as that tree.
// The shapes the base refuses too, tables holding `|`, are counted and named in the log; the test
// stops after a few failures.
func TestLinkifyStoresEveryShapeTheTesterDraws(t *testing.T) {
	const most = 5
	began := time.Now()
	shapes := testerShapes()
	if len(shapes) != 8994 {
		t.Fatalf("the tester's generator draws %d distinct shapes, want 8,994", len(shapes))
	}
	failures := 0
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
		if failures++; failures == most {
			t.Fatalf("stopped after %d failures", most)
		}
	}
	var refusedByBase, written []string
	var stored []*Node
	for _, shape := range shapes {
		if err := baseParseRefusal(shape); err != nil {
			refusedByBase = append(refusedByBase, fmt.Sprintf("%q (%v)", shape, err))
			continue
		}
		tree, read, err := ParseWritten(shape, nil)
		if err != nil {
			fail("the base stores %q, and a write of it is refused: %v", shape, err)
			continue
		}
		if back, err := ParseRendering(read.Markdown); err != nil || !back.Equal(tree) {
			fail("a write of %q stores %q, which Go reads back as %s (%v), not as the stored tree %s", shape, read.Markdown, inlineSummaries(back), err, inlineSummaries(tree))
		}
		stored = append(stored, tree)
		written = append(written, read.Markdown)
	}
	t.Logf("%d shapes: the base stores %d and refuses %d, in %v", len(shapes), len(stored), len(refusedByBase), time.Since(began))
	t.Logf("the shapes the base refuses:\n%s", strings.Join(refusedByBase, "\n"))
	readings := engineReadings(t, written)
	for index, tree := range stored {
		if engine := readings[index]; engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(tree)) {
			fail("the engine reads the stored %q as %s, not as the stored tree %s", written[index], inlineSummaries(engine), inlineSummaries(tree))
		}
	}
	t.Logf("%d stored texts read by the engine, in %v", len(written), time.Since(began))
}
