package pmdoc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Go links exactly what the browser editor's engine (remark-gfm) links, and leaves text where the
// engine does. Each row's expectation is the engine's reading (gen/differential.ts), an email's
// href its mailto: URL. The last rows are R4's (INFRA-4224), which both readers read by Dispatch's
// own rule after remark-gfm's (wholeEmailLinks): an address holding a character RFC 5322 allows
// in a local part and GFM does not link, as a resource path's `/`, a `%` or a decoded `&` or `$`,
// holds no link, a character reference inside an address of GFM's characters leaves it linked
// whole, an address alone is linked whole, a later address a reader links from inside it after
// an email link that would not read on into it holds no link, and an address that inline markup
// or a link a reader made that would read on into it splits, or that a web address reaches into
// once its link went, keeps remark-gfm's reading.
func TestLinkifyReadsWhatTheEngineReads(t *testing.T) {
	for _, test := range []struct{ markdown, want string }{
		{"mail alice@example.com now", `text "mail " | text "alice@example.com" link mailto:alice@example.com | text " now"`},
		{"(bob@example.org)", `text "(" | text "bob@example.org" link mailto:bob@example.org | text ")"`},
		{"a_a_a@b.c", `text "a_a_a@b.c" link mailto:a_a_a@b.c`},
		{"a_a_a@b.c_", `text "a_a_a@b.c_"`},
		{"x@y and a@@b.c", `text "x@y and a@@b.c"`},
		{"see https://example.com/a_b and www.example.org/x", `text "see " | text "https://example.com/a_b" link https://example.com/a_b | text " and " | text "www.example.org/x" link http://www.example.org/x`},
		{"@start of a line", `text "@start of a line"`},
		{"end.@dot", `text "end.@dot"`},
		{"x;https://x.test/a_ b", `text "x;" | text "https://x.test/a" link https://x.test/a | text "_ b"`},
		{"see http\\://x.test/a_ b", `text "see " | text "http://x.test/a_" link http://x.test/a_ | text " b"`},
		{"x;https://a.test/*b* c", `text "x;" | text "https://a.test/*b" link https://a.test/*b | text "* c"`},
		{"\"www.a.test/?u=https://b.test\"", `text "\"" | text "www.a.test/?u=" link http://www.a.test/?u= | text "https://b.test" link https://b.test | text "\""`},
		{"Grant act-as on agent-hosts@example-project.iam.gserviceaccount.com.", `text "Grant act-as on " | text "agent-hosts@example-project.iam.gserviceaccount.com" link mailto:agent-hosts@example-project.iam.gserviceaccount.com | text "."`},
		{"projects/example-project/serviceAccounts/agent-hosts@example-project.iam.gserviceaccount.com", `text "projects/example-project/serviceAccounts/agent-hosts@example-project.iam.gserviceaccount.com"`},
		{"serviceAccounts/000000000000-compute@developer.gserviceaccount.com", `text "serviceAccounts/000000000000-compute@developer.gserviceaccount.com"`},
		{"a/b.c@example.com", `text "a/b.c@example.com"`},
		{"a/b+c@example.com", `text "a/b+c@example.com"`},
		{"a/b_c@example.com", `text "a/b_c@example.com"`},
		{"serviceAccounts/agenthosts@example.com", `text "serviceAccounts/agenthosts@example.com"`},
		{"x agent&#45;hosts@example.com", `text "x " | text "agent-hosts@example.com" link mailto:agent-hosts@example.com`},
		{"agent&#45;hosts@example.com", `text "agent-hosts@example.com" link mailto:agent-hosts@example.com`},
		{"projects/p/serviceAccounts/agent-hosts\\@example.com", `text "projects/p/serviceAccounts/agent-hosts@example.com"`},
		{"a.b+c_d%e-f@example.com", `text "a.b+c_d%e-f@example.com"`},
		{"agent&amp;hosts@example.com", `text "agent&hosts@example.com"`},
		{"xé-agent@example.com", `text "xé" | text "-agent@example.com" link mailto:-agent@example.com`},
		{"xé&#45;hosts@example.com", `text "xé" | text "-hosts@example.com" link mailto:-hosts@example.com`},
		{"x $agent&#45;hosts@example.com", `text "x $agent-hosts@example.com"`},
		{"x 'agent&#45;hosts@example.com'", `text "x 'agent-hosts@example.com'"`},
		{"*agent*-hosts@example.com", `text "agent" | text "-hosts@example.com" link mailto:-hosts@example.com`},
		{"*agent*&#45;hosts@example.com", `text "agent" | text "-" | text "hosts@example.com" link mailto:hosts@example.com`},
		{"x<br>projects/p/serviceAccounts/agent-hosts@example.com", `text "x" | html "" | text "projects/p/serviceAccounts/agent-hosts@example.com"`},
		{`<span data-proof="comment" data-id="c1" data-by="user:x">a</span>gent&#45;hosts@example.com`, `text "a" | text "gent-hosts@example.com" link mailto:gent-hosts@example.com`},
		{"`SA:`agent&#45;hosts@example.com", `text "SA:" | text "agent-hosts@example.com" link mailto:agent-hosts@example.com`},
		{"www\\.a'b@c.test", `text "www.a" link http://www.a | text "'" | text "b@c.test" link mailto:b@c.test`},
		{"a@b.co+c@d.co", `text "a@b.co" link mailto:a@b.co | text "+c@d.co" link mailto:+c@d.co`},
		{"q/www.a.b~hosts@example.com", `text "q/" | text "www.a.b~" link http://www.a.b~ | text "hosts@example.com" link mailto:hosts@example.com`},
		{"x@y.co&#45;%hosts@example.com", `text "x@y.co" link mailto:x@y.co | text "-%" | text "hosts@example.com" link mailto:hosts@example.com`},
		{"x@y.co&#45;*hosts@example.com", `text "x@y.co" link mailto:x@y.co | text "-*" | text "hosts@example.com" link mailto:hosts@example.com`},
		{"%www.?hosts@example.com", `text "%" | text "www" link http://www | text ".?" | text "hosts@example.com" link mailto:hosts@example.com`},
		{"'_www.a.bhosts@example.com", `text "'" | text "_www.a.bhosts@example.com" link mailto:_www.a.bhosts@example.com`},
		{"q/&#45;www.a.bhosts@example.com", `text "q/-" | text "www.a.bhosts@example.com" link mailto:www.a.bhosts@example.com`},
		{"http://&#45;a.b@c.co", `text "http://-" | text "a.b@c.co" link mailto:a.b@c.co`},
		{"alice@example.com/bob-smith@example.com", `text "alice@example.com" link mailto:alice@example.com | text "/bob-smith@example.com"`},
		{"alice@example.com'bob-smith@example.com", `text "alice@example.com" link mailto:alice@example.com | text "'bob-smith@example.com"`},
		{"alice@example.com%bob-smith@example.com", `text "alice@example.com" link mailto:alice@example.com | text "%bob-smith@example.com"`},
		{"mail a@b.co&c-d@e.co now", `text "mail " | text "a@b.co" link mailto:a@b.co | text "&c-d@e.co now"`},
	} {
		doc, err := Parse(test.markdown)
		if err != nil {
			t.Fatalf("%q: %v", test.markdown, err)
		}
		if got := inlineSummary(doc.Children[0]); got != test.want {
			t.Errorf("%q:\n got %s\nwant %s", test.markdown, got, test.want)
		}
		if err := RefuseMisreadDocument(doc); err != nil {
			t.Errorf("a write of %q is refused: %v", test.markdown, err)
		}
	}
}

// Go links an email address whole, or not at all, as the browser editor's engine does (R4,
// INFRA-4224; wholeEmailLinks, and the engine's packages/proof-editor/tests/whole-email-links.test.ts
// with the same rows). Each row is markdown and what Go writes of its reading: the markdown itself
// where the address takes no link, and the markdown with the whole address written as its link,
// `[addr](mailto:addr)`, where it takes one, or with each link a reader made written so, where a
// link stays as the reader made it; a row written with an escape or a character reference is
// written in Go's own spelling. Each row is read in a paragraph and again at a line's start, in
// a list item, after a word and inside a word of one, in a typed block and in a list item there,
// in a quote and in a table cell. In each, Render of Parse's tree is that writing, a write of the
// tree is not refused (RefuseMisreadDocument), the display read in both modes reads Parse's tree,
// block ids aside, and the engine reads the markdown and its rendering as Go reads the markdown.
//
// The rows whose reading the rule changes come first, each with what Go and the engine read before
// it. The rest read the same before it, and are the controls: an address alone, a path that holds
// no `-`, `.`, `+` or `_`, a link that already starts at its run's start, a run of GFM's
// characters that reaches into an email link a reader made, which keeps both links, an address
// that a link a reader made splits where it would read on into the address were it written bare,
// whatever the run holds, which keeps both links, one that a web address would reach into once
// its link went, which keeps the link, an address inline markup splits, which keeps remark-gfm's
// reading, and the links a writer wrote, which are never changed - an early link stored
// explicitly, as a rendering stored before the rule writes it, is written unchanged.
func TestLinkifyLinksAnEmailWholeOrNotAtAll(t *testing.T) {
	const callout = `:::callout{#c1 kind="note" title="T"}`
	rows := []struct{ markdown, written string }{
		// Before: a link to `hosts@…`, `compute@…` and `c@…` after the path's last `-`, `.`, `+`
		// or `_`, where the transform searched again once the `/` refused the address.
		{"projects/example-project/serviceAccounts/agent-hosts@example-project.iam.gserviceaccount.com", "projects/example-project/serviceAccounts/agent-hosts@example-project.iam.gserviceaccount.com"},
		{"projects/p/serviceAccounts/agent-hosts@example.com", "projects/p/serviceAccounts/agent-hosts@example.com"},
		{"serviceAccounts/000000000000-compute@developer.gserviceaccount.com", "serviceAccounts/000000000000-compute@developer.gserviceaccount.com"},
		{"a/b.c@example.com", "a/b.c@example.com"},
		{"a/b+c@example.com", "a/b+c@example.com"},
		{"a/b_c@example.com", "a/b_c@example.com"},
		// Before: links to `c@…` and `f@…`; each link that goes joins the text around it.
		{"a/b.c@example.com, d/e.f@example.com.", "a/b.c@example.com, d/e.f@example.com."},
		// Before: a link to `hosts@…`, which the transform read in the escape's decoded `@`.
		{"projects/p/serviceAccounts/agent-hosts\\@example.com", "projects/p/serviceAccounts/agent-hosts@example.com"},
		// Before: a link to `e-f@…` and to `hosts@…`, which the tokenizer started right after the
		// `%` and the reference's `;`; the run holds the `%` and the decoded `&`.
		{"a.b+c_d%e-f@example.com", "a.b+c_d%e-f@example.com"},
		{"agent&amp;hosts@example.com", "agent&hosts@example.com"},
		// Before: the text through the reference, and a link to `hosts@…`, which the tokenizer
		// started right after the reference's `;`. Each symbol is a character RFC 5322 allows in
		// a local part and GFM does not link, so the run holds it.
		{"x $agent&#45;hosts@example.com", "x $agent-hosts@example.com"},
		{"x 'agent&#45;hosts@example.com'", "x 'agent-hosts@example.com'"},
		{"x =agent&#45;hosts@example.com", "x =agent-hosts@example.com"},
		{"x #agent&#45;hosts@example.com", "x #agent-hosts@example.com"},
		{"x ?agent&#45;hosts@example.com", "x ?agent-hosts@example.com"},
		{"x ^agent&#45;hosts@example.com", "x ^agent-hosts@example.com"},
		// Before: `agent-` or `x agent-` and a link to `hosts@…`; the run is all GFM's.
		{"agent&#45;hosts@example.com", "[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"x agent&#45;hosts@example.com", "x [agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		// `é` is no local-part character, so the run is the `-`, as `xé-hosts@…` reads.
		{"xé&#45;hosts@example.com", "xé[-hosts@example.com](mailto:-hosts@example.com)"},
		// Before: the tag, the path through `agent-` and a link to `hosts@…`. The tag's text ends
		// in `>`, so the run is the path's text, `/` and all.
		{"x<br>projects/p/serviceAccounts/agent-hosts@example.com", "x<br>projects/p/serviceAccounts/agent-hosts@example.com"},
		// Before: the code, `agent-` and a link to `hosts@…`. The code's text ends in `:`, so the
		// run is `agent-`, and the link takes it.
		{"`SA:`agent&#45;hosts@example.com", "`SA:`[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		// Before: the email link, the text through `bob-` or the `&`, and a link to `smith@…` or
		// `c-d@…`. Neither a `/` nor a `&` continues an email's domain, so the run reaches through
		// the first address's link to its `@` and holds that character: the later link goes.
		{"alice@example.com/bob-smith@example.com", "alice@example.com/bob-smith@example.com"},
		{"mail a@b.co&c-d@e.co now", "mail a@b.co&c-d@e.co now"},
		// The controls.
		{"Grant act-as on agent-hosts@example-project.iam.gserviceaccount.com.", "Grant act-as on [agent-hosts@example-project.iam.gserviceaccount.com](mailto:agent-hosts@example-project.iam.gserviceaccount.com)."},
		{"agent-hosts@example.com", "[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"x agent-hosts@example.com", "x [agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"serviceAccounts/agenthosts@example.com", "serviceAccounts/agenthosts@example.com"},
		{"xé-agent@example.com", "xé[-agent@example.com](mailto:-agent@example.com)"},
		// A run of GFM's characters that reaches into an email link a reader made, which a `+`
		// does not continue: one link never takes another's text, so both stand.
		{"a@b.co+c@d.co", "[a@b.co](mailto:a@b.co)[+c@d.co](mailto:+c@d.co)"},
		// A link a reader made that, written bare, would read on into the text after it ends the
		// run as markup does, whatever the run holds: a web link, which runs on through the `'`,
		// `~` or `?` after it, and an email link before a `-`, which continues its domain.
		{"www\\.a'b@c.test", "[www.a](http://www.a)'[b@c.test](mailto:b@c.test)"},
		{"q/www.a.b~hosts@example.com", "q/[www.a.b~](http://www.a.b~)[hosts@example.com](mailto:hosts@example.com)"},
		{"x@y.co&#45;%hosts@example.com", "[x@y.co](mailto:x@y.co)-%[hosts@example.com](mailto:hosts@example.com)"},
		{"x@y.co&#45;*hosts@example.com", "[x@y.co](mailto:x@y.co)-*[hosts@example.com](mailto:hosts@example.com)"},
		{"%www.?hosts@example.com", "%[www](http://www).?[hosts@example.com](mailto:hosts@example.com)"},
		// The run holds a character GFM does not link, and once the link went, a web address GFM
		// links would reach into the address: the link stands.
		{"'_www.a.bhosts@example.com", "'[\\_www.a.bhosts@example.com](mailto:_www.a.bhosts@example.com)"},
		{"q/&#45;www.a.bhosts@example.com", "q/-[www.a.bhosts@example.com](mailto:www.a.bhosts@example.com)"},
		{"http://&#45;a.b@c.co", "http://-[a.b@c.co](mailto:a.b@c.co)"},
		// Markup splits the address: the run's text follows a node whose text ends in a character
		// of the run.
		{"*agent*-hosts@example.com", "*agent*[-hosts@example.com](mailto:-hosts@example.com)"},
		{"`agent`-hosts@example.com", "`agent`[-hosts@example.com](mailto:-hosts@example.com)"},
		{"*agent*&#45;hosts@example.com", "*agent*-[hosts@example.com](mailto:hosts@example.com)"},
		{"`agent`&#45;hosts@example.com", "`agent`-[hosts@example.com](mailto:hosts@example.com)"},
		{"[agent-hosts@example.com](mailto:agent-hosts@example.com)", "[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"agent-[hosts@example-project.iam.gserviceaccount.com](mailto:hosts@example-project.iam.gserviceaccount.com)", "agent-[hosts@example-project.iam.gserviceaccount.com](mailto:hosts@example-project.iam.gserviceaccount.com)"},
		{"agent-[\\[hosts@example.com](mailto:[hosts@example.com)", "agent-[\\[hosts@example.com](mailto:[hosts@example.com)"},
		{"<agent-hosts@example.com>", "[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"agent-<hosts@example.com>", "agent-[hosts@example.com](mailto:hosts@example.com)"},
		{"a/<hosts@example.com>", "a/[hosts@example.com](mailto:hosts@example.com)"},
	}
	contexts := []struct{ name, format string }{
		{"a paragraph", "%s\n"},
		// After a hard break, which the rendering writes as it is written here, where a soft
		// break is read as a space.
		{"at a line's start", "Lead\\\n%s\n"},
		{"a list item", "- %s\n"},
		{"after a word of a list item", "- x %s\n"},
		// After a letter outside ASCII, which no address takes into its local part.
		{"inside a word of a list item", "- é%s\n"},
		{"a typed block", callout + "\n%s\n:::\n"},
		{"a list item in a typed block", callout + "\n- %s\n:::\n"},
		{"a quote", "> %s\n"},
		{"a table cell", "| a |\n| --- |\n| %s |\n"},
	}
	// No web address starts after a letter, so inside a word the www link and the scheme's address
	// are text, the run holds their `'` and `/`, and the address reads as written.
	insideAWord := map[string]string{"www\\.a'b@c.test": "www.a'b@c.test", "http://&#45;a.b@c.co": "http://-a.b@c.co"}
	// A row in a context, and Go's reading and rendering of it.
	type contextual struct {
		name, markdown, want, rendering string
		doc                             *Node
	}
	var reads []contextual
	for _, context := range contexts {
		for _, row := range rows {
			markdown, written := fmt.Sprintf(context.format, row.markdown), row.written
			if inWord, ok := insideAWord[row.markdown]; ok && context.name == "inside a word of a list item" {
				written = inWord
			}
			reads = append(reads, contextual{name: context.name + "/" + row.markdown, markdown: markdown, want: fmt.Sprintf(context.format, written)})
		}
	}
	for index := range reads {
		read := &reads[index]
		t.Run(read.name, func(t *testing.T) {
			doc, err := Parse(read.markdown)
			if err != nil {
				t.Fatalf("Parse(%q): %v", read.markdown, err)
			}
			read.doc, read.rendering = doc, mustRender(t, doc)
			if read.rendering != read.want {
				t.Errorf("Render(Parse(%q)) = %q, want %q; Go reads %s", read.markdown, read.rendering, read.want, inlineSummaries(doc))
			}
			if err := RefuseMisreadDocument(doc); err != nil {
				t.Errorf("a write of %q is refused: %v", read.markdown, err)
			}
			for _, mode := range []DisplayMode{DisplayLatest, DisplayVersion} {
				if display := ReadForDisplay(read.markdown, mode); !withoutBlockIDs(display).Equal(withoutBlockIDs(doc)) {
					t.Errorf("the display read (%v) of %q is %s, and Parse's %s", mode, read.markdown, inlineSummaries(display), inlineSummaries(doc))
				}
			}
		})
	}
	// Parse refuses a link reference definition, so a reference link reaches Go through the display
	// read alone, which draws the definition as the text it is; its link is the writer's, and stays.
	const reference = "agent-[hosts@example.com][r]\n\n[r]: mailto:hosts@example.com\n"
	const referenceLink = `text "agent-" | text "hosts@example.com" link mailto:hosts@example.com`
	for _, mode := range []DisplayMode{DisplayLatest, DisplayVersion} {
		if got := inlineSummary(ReadForDisplay(reference, mode).Children[0]); got != referenceLink {
			t.Errorf("the display read (%v) of %q opens %s, want %s", mode, reference, got, referenceLink)
		}
	}
	t.Run("the engine", func(t *testing.T) {
		markdowns := []string{reference}
		for _, read := range reads {
			markdowns = append(markdowns, read.markdown, read.rendering)
		}
		readings := engineReadings(t, markdowns)
		if engine := readings[0]; engine == nil || len(engine.Children) != 1 || inlineSummary(engine.Children[0]) != referenceLink {
			t.Errorf("the engine reads %q as %s, want %s", reference, inlineSummaries(engine), referenceLink)
		}
		for index, read := range reads {
			t.Run(read.name, func(t *testing.T) {
				if read.doc == nil {
					t.Skip("Go read no tree")
				}
				for offset, markdown := range []string{read.markdown, read.rendering} {
					if engine := readings[1+2*index+offset]; engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(read.doc)) {
						t.Errorf("the engine reads %q as %s, and Go reads %q as %s", markdown, inlineSummaries(engine), read.markdown, inlineSummaries(read.doc))
					}
				}
			})
		}
	})
}

// refusedEmailShape is a row of testdata/refused-email-shapes.jsonl: a document holding an email
// address, the cause a write of it was once refused for, if any, a note where the row's reading
// was ruled on, and its inline runs as Go and the engine read it, each its text, or a node that is
// no text as its type in brackets, and the href of the link over it, if any. The file also
// records, under set and refused, where each shape was measured; no test reads them.
type refusedEmailShape struct {
	Markdown  string      `json:"markdown"`
	Mechanism string      `json:"mechanism"`
	Note      string      `json:"note"`
	Runs      []inlineRun `json:"runs"`
}

// The notes of two kinds of row of testdata/refused-email-shapes.jsonl start with these
// (TestLinkifyStoresEveryRefusedShape).
const (
	secondAddressNote = "a second address after an email link"
	explicitOnlyNote  = "an earlier link the renderer cannot write bare"
)

// inlineRun is one inline node of a reading: its text, and the href of the link over it, or "".
type inlineRun struct {
	Text string `json:"text"`
	Link string `json:"link"`
}

// inlineRuns is every inline node of doc, in document order, as an inlineRun.
func inlineRuns(doc *Node) []inlineRun {
	var runs []inlineRun
	Walk(doc, func(node *Node) bool {
		if !isInlineNodeType(node.Type) {
			return true
		}
		run := inlineRun{Text: node.Text}
		if node.Type != "text" {
			run.Text = "[" + node.Type + "]"
		}
		for _, mark := range node.Marks {
			if mark.Type == "link" {
				run.Link = mark.Attrs["href"].(string)
			}
		}
		runs = append(runs, run)
		return true
	})
	return runs
}

// R4 (INFRA-4224) stores every email address, so a write the rule once refused is stored now. The
// rows are 66 shapes, 55 of them once refused - the run reaching into a link a reader made (cause
// A), or a dropped link leaving a web address (cause B) - and 11 every build stores; 5 more of
// cause B, where the web address is inside the local part or opens with a scheme; and 4 whose
// earlier link the renderer cannot write bare. The engine's
// packages/proof-editor/tests/whole-email-links.test.ts reads the same rows. Each is read as the
// row says Go and the engine read it, a write of it is not refused (ParseForWrite), its rendering
// reads back as its tree, and the engine reads the markdown and the rendering as Go reads the
// markdown. A row of cause B keeps the email link the reader made and gets no web-address link.
// Four of the 11, noted secondAddressNote, hold a second address that a reader links from inside
// it, after an email link that would not read on into it
// (`alice@example.com/bob-smith@example.com`): they store with no link on the second address. The
// last four, noted explicitOnlyNote, are shapes the run rule refuses without its second exception
// (writtenExplicitOnly), where the earlier link the reader made stands next to an email link the
// writer wrote or starts `_www.`: they store as the reader read them, the later address linked as
// the reader linked it.
func TestLinkifyStoresEveryRefusedShape(t *testing.T) {
	shapes := readRefusedEmailShapes(t)
	docs := make([]*Node, len(shapes))
	noted := map[string]int{}
	var markdowns []string
	for index, shape := range shapes {
		doc, err := Parse(shape.Markdown)
		if err != nil {
			t.Errorf("Parse(%q): %v", shape.Markdown, err)
			markdowns = append(markdowns, shape.Markdown, shape.Markdown)
			continue
		}
		runs := inlineRuns(doc)
		if !slices.Equal(runs, shape.Runs) {
			t.Errorf("Go reads %q as %v, want %v", shape.Markdown, runs, shape.Runs)
		}
		if _, err := ParseForWrite(shape.Markdown, nil); err != nil {
			t.Errorf("a write of %q is refused: %v", shape.Markdown, err)
		}
		rendering := mustRender(t, doc)
		if back, err := Parse(rendering); err != nil || !back.Equal(doc) {
			t.Errorf("%q is written %q, which Go reads back as %s (%v), not %s", shape.Markdown, rendering, inlineSummaries(back), err, inlineSummaries(doc))
		}
		if shape.Mechanism == "B" {
			emails := 0
			for _, run := range runs {
				if strings.HasPrefix(run.Link, "mailto:") {
					emails++
				} else if run.Link != "" {
					t.Errorf("%q holds a web-address link, %v", shape.Markdown, run)
				}
			}
			if emails == 0 {
				t.Errorf("%q holds no email link", shape.Markdown)
			}
		}
		links := 0
		for _, run := range runs {
			if run.Link != "" {
				links++
			}
		}
		switch {
		case strings.HasPrefix(shape.Note, secondAddressNote):
			// The first address alone is linked.
			noted[secondAddressNote]++
			if links != 1 {
				t.Errorf("%q holds %d links, want the first address's alone", shape.Markdown, links)
			}
		case strings.HasPrefix(shape.Note, explicitOnlyNote):
			// As the reader read it: the earlier link and the later address's both.
			noted[explicitOnlyNote]++
			if links < 2 {
				t.Errorf("%q holds %d links, want the earlier link's and the later address's", shape.Markdown, links)
			}
		}
		docs[index] = doc
		markdowns = append(markdowns, shape.Markdown, rendering)
	}
	if noted[secondAddressNote] != 4 || noted[explicitOnlyNote] != 4 {
		t.Errorf("testdata/refused-email-shapes.jsonl notes %v, want four of %q and four of %q", noted, secondAddressNote, explicitOnlyNote)
	}
	readings := engineReadings(t, markdowns)
	for index, shape := range shapes {
		if docs[index] == nil {
			continue
		}
		for offset, markdown := range markdowns[2*index : 2*index+2] {
			if engine := readings[2*index+offset]; engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(docs[index])) {
				t.Errorf("the engine reads %q as %s, and Go reads %q as %s", markdown, inlineSummaries(engine), shape.Markdown, inlineSummaries(docs[index]))
			}
		}
	}
}

// readRefusedEmailShapes is every row of testdata/refused-email-shapes.jsonl, which holds 75.
func readRefusedEmailShapes(t *testing.T) []refusedEmailShape {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "refused-email-shapes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var shapes []refusedEmailShape
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var shape refusedEmailShape
		if err := json.Unmarshal([]byte(line), &shape); err != nil {
			t.Fatalf("testdata/refused-email-shapes.jsonl: %v", err)
		}
		shapes = append(shapes, shape)
	}
	if len(shapes) != 75 {
		t.Fatalf("testdata/refused-email-shapes.jsonl holds %d shapes, want 75", len(shapes))
	}
	return shapes
}

// runHolding is the first inline run of doc whose text holds text (inlineRuns), or the zero run
// where none does.
func runHolding(doc *Node, text string) inlineRun {
	for _, run := range inlineRuns(doc) {
		if strings.Contains(run.Text, text) {
			return run
		}
	}
	return inlineRun{}
}

// testerLaterAddress is the second address of each shape the tester found refused, which the
// rule reads with no link and the base links whole.
const testerLaterAddress = "smith@example.com"

// R4 stores every text the base stores (main 948aeca5 stores each of these), each address as
// written or linked whole. These two hold an earlier email link the reader made and then a second
// address after `/`, as `alice@example.com/bob-smith@example.com` does, where the earlier link
// starts `b-_www.` or after a code span and a character reference: the tester's own generator found
// them (seeds 11, 23 and 4242 over its own pieces). The rule reads `smith@example.com` with no
// link, but the renderer writes the earlier email link explicit, and after an explicit link the
// rule keeps the link on `smith@example.com`, so no spelling of the rule's tree reads back: the
// write takes the base's reading of that address (FallBackEmailLinks). For each, (1) a write is
// not refused; (2) the tree the write stores renders to the text it stores, which reads back as
// that tree; (3) the browser editor's engine reads the stored text as the stored tree; and (4) the
// raw shape keeps the rule's reading, where Parse and the engine link nothing on
// `smith@example.com`, while the stored tree holds the base's link to it.
func TestLinkifyStoresTheShapesTheTesterFoundRefused(t *testing.T) {
	shapes := []string{
		"b-_www.@example.com./bob-smith@example.com\n",
		"`c`&period;.@x_y.com/bob-smith@example.com\n",
	}
	stored := make([]*Node, len(shapes))
	var markdowns []string
	for index, shape := range shapes {
		if _, err := ParseForWrite(shape, nil); err != nil {
			t.Errorf("a write of %q is refused: %v", shape, err)
		}
		tree, written, err := ParseWritten(shape, nil)
		if err != nil {
			t.Errorf("ParseWritten(%q): %v", shape, err)
			markdowns = append(markdowns, shape, shape)
			continue
		}
		if rendering := mustRender(t, tree); rendering != written.Markdown {
			t.Errorf("a write of %q stores %q, and its tree renders to %q", shape, written.Markdown, rendering)
		}
		if back, err := ParseRendering(written.Markdown); err != nil || !back.Equal(tree) {
			t.Errorf("a write of %q stores %q, which Go reads back as %s (%v), not as the stored tree %s", shape, written.Markdown, inlineSummaries(back), err, inlineSummaries(tree))
		}
		if run := runHolding(tree, testerLaterAddress); run != (inlineRun{Text: testerLaterAddress, Link: "mailto:" + testerLaterAddress}) {
			t.Errorf("a write of %q stores %s, holding %v where the base links %s whole", shape, inlineSummaries(tree), run, testerLaterAddress)
		}
		doc, err := Parse(shape)
		if err != nil {
			t.Errorf("Parse(%q): %v", shape, err)
		} else if run := runHolding(doc, testerLaterAddress); run.Link != "" {
			t.Errorf("Go reads %q as %s, linking %v, where the rule links nothing on %s", shape, inlineSummaries(doc), run, testerLaterAddress)
		}
		stored[index] = tree
		markdowns = append(markdowns, shape, written.Markdown)
	}
	readings := engineReadings(t, markdowns)
	for index, shape := range shapes {
		if stored[index] == nil {
			continue
		}
		if engine := readings[2*index+1]; engine == nil || !withoutBlockIDs(engine).Equal(withoutBlockIDs(stored[index])) {
			t.Errorf("a write of %q stores %q, which the engine reads as %s, not as the stored tree %s", shape, markdowns[2*index+1], inlineSummaries(engine), inlineSummaries(stored[index]))
		}
		if engine := readings[2*index]; engine == nil || runHolding(engine, testerLaterAddress).Link != "" {
			t.Errorf("the engine reads %q as %s, where the rule links nothing on %s", shape, inlineSummaries(engine), testerLaterAddress)
		}
	}
}

// FallBackEmailLinks gives an address the base's reading only where the rule's reading of it
// cannot be written so that it reads back. The rule reads the first shape the tester found refused
// with no link on `smith@example.com`, a tree no spelling reads back: the fallback gives that
// address the base's link and answers a tree that reads back, with its read-back, keeping the
// paragraph's block id and its text, a line after a hard break included, and leaving the tree it
// was given as it was. Each shape of testdata/refused-email-shapes.jsonl already reads back, and
// comes back as the tree it was given, uncopied, with changed false and its own read-back.
func TestLinkifyFallsBackToTheBaseWhereTheRuleDoesNotReadBack(t *testing.T) {
	const shape = "b-_www.@example.com./bob-smith@example.com\n"
	// inlineKinds is the kind of each inline node of doc's first block that is no text.
	inlineKinds := func(doc *Node) []string {
		var kinds []string
		for _, node := range doc.Children[0].Children {
			if node.Type != "text" {
				kinds = append(kinds, node.Type)
			}
		}
		return kinds
	}
	for _, markdown := range []string{shape, strings.TrimSuffix(shape, "\n") + "\\\nand on\n"} {
		doc, err := Parse(markdown)
		if err != nil {
			t.Fatalf("Parse(%q): %v", markdown, err)
		}
		given := cloneNode(doc)
		if run := runHolding(doc, testerLaterAddress); run.Link != "" {
			t.Fatalf("Go reads %q as %s, linking %v, where the rule links nothing on %s", markdown, inlineSummaries(doc), run, testerLaterAddress)
		}
		tree, changed, read, err := FallBackEmailLinks(doc)
		if err != nil || !changed {
			t.Fatalf("FallBackEmailLinks(Parse(%q)) = %s, changed %v, %v; want the base's link on %s", markdown, inlineSummaries(tree), changed, err, testerLaterAddress)
		}
		if run := runHolding(tree, testerLaterAddress); run != (inlineRun{Text: testerLaterAddress, Link: "mailto:" + testerLaterAddress}) {
			t.Errorf("the fallback of %q is %s, holding %v where the base links %s whole", markdown, inlineSummaries(tree), run, testerLaterAddress)
		}
		if TextContent(tree) != TextContent(doc) || !slices.Equal(inlineKinds(tree), inlineKinds(doc)) {
			t.Errorf("the fallback of %q is %s, not the text and inline nodes of %s", markdown, inlineSummaries(tree), inlineSummaries(doc))
		}
		if rendering := mustRender(t, tree); rendering != read.Markdown {
			t.Errorf("the fallback of %q renders to %q, and answers the read-back of %q", markdown, rendering, read.Markdown)
		}
		if back, err := ParseRendering(read.Markdown); err != nil || !back.Equal(tree) || !read.Tree.Equal(tree) {
			t.Errorf("the fallback of %q is %s, written %q, which Go reads back as %s (%v), answered as %s", markdown, inlineSummaries(tree), read.Markdown, inlineSummaries(back), err, inlineSummaries(read.Tree))
		}
		if id := blockIDOf(doc.Children[0]); id == "" || blockIDOf(tree.Children[0]) != id {
			t.Errorf("the fallback of %q gives its paragraph the block id %q, not %q", markdown, blockIDOf(tree.Children[0]), id)
		}
		if !doc.EqualWithBlockIDs(given) {
			t.Errorf("the fallback of %q wrote the tree it was given: %s, was %s", markdown, inlineSummaries(doc), inlineSummaries(given))
		}
	}
	for _, shape := range readRefusedEmailShapes(t) {
		doc, err := Parse(shape.Markdown)
		if err != nil {
			t.Errorf("Parse(%q): %v", shape.Markdown, err)
			continue
		}
		tree, changed, read, err := FallBackEmailLinks(doc)
		if err != nil || changed || tree != doc {
			t.Errorf("FallBackEmailLinks(Parse(%q)) = %s, changed %v, %v; want the tree it was given, unchanged", shape.Markdown, inlineSummaries(tree), changed, err)
		}
		if rendering := mustRender(t, doc); rendering != read.Markdown || read.Tree == nil || !read.Tree.Equal(doc) {
			t.Errorf("FallBackEmailLinks(Parse(%q)) answers the read-back %q, %s, not its rendering %q", shape.Markdown, read.Markdown, inlineSummaries(read.Tree), rendering)
		}
	}
}

// literalURL answers a URL at each offset of a line as a fresh read would, through the two records
// that keep a line linear: the domain read from an earlier trigger (literalDomains), which a www
// literal starting inside it reads again with its own underscores and dots, and a trail run
// already found to continue the URL (literalTrail).
func TestLiteralURLReadsEachOffsetAsAFreshRead(t *testing.T) {
	for _, test := range []struct {
		line string
		want map[int]int // offset → the URL's length, 0 for none; offsets not listed are checked against a fresh read only
	}{
		{"_www.a_www.a_www.ab", map[int]int{1: 0, 7: 0, 13: 6}},
		{"www.a_b.c.d www.d", map[int]int{0: 11, 12: 5}},
		{"www.x_y.z.com/q", map[int]int{0: 15}},
		{"www.a.b_c.d", map[int]int{0: 0}},
		{"https://x.test/a)\"; b", map[int]int{0: 16}},
		{"https://x.test/(a)) b", map[int]int{0: 18}},
		{"https://x.test/a&amp;&amp;) b", map[int]int{0: 16}},
		{"https://x.test/a&amp;x b", map[int]int{0: 22}},
		{"https://x.test/a.,;:!?'\"_~*)x b", map[int]int{0: 29}},
		{"https://x.test/a.,;:!?'\"_~*) b", map[int]int{0: 16}},
		{"https://x.test/a](b) https://x.test/c]x", map[int]int{0: 16, 21: 18}},
		{"HTTPS://X.TEST/Y www.", map[int]int{0: 16, 17: 0}},
	} {
		line := []byte(test.line)
		shared := &literalDomains{from: -1, end: -1}
		for offset := range len(line) {
			if !literalURLPrefix(line[offset:]) {
				continue
			}
			shared.base = offset
			got, _ := literalURL(line[offset:], shared)
			fresh, _ := literalURL(line[offset:], &literalDomains{from: -1, end: -1, base: offset})
			if got != fresh {
				t.Errorf("%q at %d: %d with the line's record, %d read fresh", test.line, offset, got, fresh)
			}
			if want, ok := test.want[offset]; ok && got != want {
				t.Errorf("%q at %d: length %d, want %d", test.line, offset, got, want)
			}
		}
	}
}

func inlineSummary(paragraph *Node) string {
	var parts []string
	for _, node := range paragraph.Children {
		part := fmt.Sprintf("%s %q", node.Type, node.Text)
		for _, mark := range node.Marks {
			if mark.Type == "link" {
				part += " link " + mark.Attrs["href"].(string)
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " | ")
}
