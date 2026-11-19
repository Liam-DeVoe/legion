package pmdoc

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// emailShapeSeed and emailShapeBound fix the shapes TestLinkifyStoresEveryGeneratedAddress builds,
// so a failure reproduces: the generator's seed, and how many shapes it draws.
const (
	emailShapeSeed  = 4224
	emailShapeBound = 4000
)

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
// the markdown. The shapes come from a fixed seed, so a failure reproduces; the test stops after
// a few failures.
func TestLinkifyStoresEveryGeneratedAddress(t *testing.T) {
	const most = 5
	began := time.Now()
	shapes := emailShapes(emailShapeSeed, emailShapeBound)
	failures := 0
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
		if failures++; failures == most {
			t.Fatalf("stopped after %d failures", most)
		}
	}
	docs := make([]*Node, len(shapes))
	markdowns := make([]string, 0, 2*len(shapes))
	for index, shape := range shapes {
		doc, err := Parse(shape.markdown)
		if err != nil {
			fail("%q in %s: Parse(%q): %v", shape.text, shape.container, shape.markdown, err)
			markdowns = append(markdowns, shape.markdown, shape.markdown)
			continue
		}
		if _, err := ParseForWrite(shape.markdown, nil); err != nil {
			fail("%q in %s: a write of %q is refused: %v", shape.text, shape.container, shape.markdown, err)
		}
		rendering := mustRender(t, doc)
		if back, err := Parse(rendering); err != nil || !back.Equal(doc) {
			fail("%q in %s: %q is written %q, which Go reads back as %s (%v), not %s", shape.text, shape.container, shape.markdown, rendering, inlineSummaries(back), err, inlineSummaries(doc))
		}
		docs[index] = doc
		markdowns = append(markdowns, shape.markdown, rendering)
	}
	t.Logf("seed %d, bound %d: %d shapes read and written in %v", emailShapeSeed, emailShapeBound, len(shapes), time.Since(began))
	readings := engineReadings(t, markdowns)
	for index, shape := range shapes {
		if docs[index] == nil {
			continue
		}
		for offset, markdown := range markdowns[2*index : 2*index+2] {
			if engine := readings[2*index+offset]; engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(docs[index])) {
				fail("%q in %s: the engine reads %q as %s, and Go reads %q as %s", shape.text, shape.container, markdown, inlineSummaries(engine), shape.markdown, inlineSummaries(docs[index]))
			}
		}
	}
	t.Logf("seed %d, bound %d: %d shapes, the engine's reading included, in %v", emailShapeSeed, emailShapeBound, len(shapes), time.Since(began))
}
