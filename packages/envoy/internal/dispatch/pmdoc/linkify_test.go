package pmdoc

import (
	"fmt"
	"strings"
	"testing"
)

// Go links exactly what the browser editor's engine (remark-gfm) links, and leaves text where the
// engine does. Each row's expectation is the engine's reading (gen/differential.ts), an email's
// href its mailto: URL. The last rows are R4's (INFRA-4224), which both readers read by Dispatch's
// own rule after remark-gfm's (wholeEmailLinks): a resource path holds no link, a character
// reference inside an address leaves it linked whole, and an address alone is linked whole.
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
		{"Grant act-as on agent-hosts@theorem-research.iam.gserviceaccount.com.", `text "Grant act-as on " | text "agent-hosts@theorem-research.iam.gserviceaccount.com" link mailto:agent-hosts@theorem-research.iam.gserviceaccount.com | text "."`},
		{"projects/theorem-research/serviceAccounts/agent-hosts@theorem-research.iam.gserviceaccount.com", `text "projects/theorem-research/serviceAccounts/agent-hosts@theorem-research.iam.gserviceaccount.com"`},
		{"serviceAccounts/482450377695-compute@developer.gserviceaccount.com", `text "serviceAccounts/482450377695-compute@developer.gserviceaccount.com"`},
		{"a/b.c@example.com", `text "a/b.c@example.com"`},
		{"a/b+c@example.com", `text "a/b+c@example.com"`},
		{"a/b_c@example.com", `text "a/b_c@example.com"`},
		{"serviceAccounts/agenthosts@example.com", `text "serviceAccounts/agenthosts@example.com"`},
		{"x agent&#45;hosts@example.com", `text "x " | text "agent-hosts@example.com" link mailto:agent-hosts@example.com`},
		{"projects/p/serviceAccounts/agent-hosts\\@example.com", `text "projects/p/serviceAccounts/agent-hosts@example.com"`},
		{"xé-agent@example.com", `text "xé" | text "-agent@example.com" link mailto:-agent@example.com`},
		{"xé&#45;hosts@example.com", `text "xé" | text "-hosts@example.com" link mailto:-hosts@example.com`},
		{"x $agent&#45;hosts@example.com", `text "x $" | text "agent-hosts@example.com" link mailto:agent-hosts@example.com`},
	} {
		doc, err := Parse(test.markdown)
		if err != nil {
			t.Fatalf("%q: %v", test.markdown, err)
		}
		if got := inlineSummary(doc.Children[0]); got != test.want {
			t.Errorf("%q:\n got %s\nwant %s", test.markdown, got, test.want)
		}
	}
}

// Go links an email address whole, or not at all, as the browser editor's engine does (R4,
// INFRA-4224; wholeEmailLinks, and the engine's packages/proof-editor/tests/whole-email-links.test.ts
// with the same rows). Each row is markdown and what Go writes of its reading: the markdown itself
// where the address takes no link, and the markdown with the whole address written as its link,
// `[addr](mailto:addr)`, where it takes one; a row written with an escape or a character reference
// is written in Go's own spelling. Each row is read in a paragraph and again at a line's start, in
// a list item, after a word and inside a word of one, in a typed block and in a list item there,
// in a quote and in a table cell. In each, Render of Parse's tree is that writing, a write of the
// tree is not refused (RefuseMisreadDocument), the display read in both modes reads Parse's tree,
// block ids aside, and the engine reads the markdown and its rendering as Go reads the markdown.
//
// The rows whose reading the rule changes come first, each with what Go and the engine read before
// it. The rest read the same before it, and are the controls: an address alone, a path that holds
// no `-`, `.`, `+` or `_`, a link that already starts at its run's start, and the links a writer
// wrote, which are never changed - an early link stored explicitly, as a rendering stored before
// the rule writes it, is written unchanged.
func TestLinkifyLinksAnEmailWholeOrNotAtAll(t *testing.T) {
	const callout = `:::callout{#c1 kind="note" title="T"}`
	rows := []struct{ markdown, written string }{
		// Before: a link to `hosts@…`, `compute@…` and `c@…` after the path's last `-`, `.`, `+`
		// or `_`, where the transform searched again once the `/` refused the address.
		{"projects/theorem-research/serviceAccounts/agent-hosts@theorem-research.iam.gserviceaccount.com", "projects/theorem-research/serviceAccounts/agent-hosts@theorem-research.iam.gserviceaccount.com"},
		{"projects/p/serviceAccounts/agent-hosts@example.com", "projects/p/serviceAccounts/agent-hosts@example.com"},
		{"serviceAccounts/482450377695-compute@developer.gserviceaccount.com", "serviceAccounts/482450377695-compute@developer.gserviceaccount.com"},
		{"a/b.c@example.com", "a/b.c@example.com"},
		{"a/b+c@example.com", "a/b+c@example.com"},
		{"a/b_c@example.com", "a/b_c@example.com"},
		// Before: links to `c@…` and `f@…`; each link that goes joins the text around it.
		{"a/b.c@example.com, d/e.f@example.com.", "a/b.c@example.com, d/e.f@example.com."},
		// Before: a link to `hosts@…`, which the transform read in the escape's decoded `@`.
		{"projects/p/serviceAccounts/agent-hosts\\@example.com", "projects/p/serviceAccounts/agent-hosts@example.com"},
		// Before: a link to `hosts@…`, which the tokenizer started right after the reference's `;`.
		{"x agent&#45;hosts@example.com", "x [agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"x $agent&#45;hosts@example.com", "x $[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		// `é` is no local-part character, so the run is the `-`, as `xé-hosts@…` reads.
		{"xé&#45;hosts@example.com", "xé[-hosts@example.com](mailto:-hosts@example.com)"},
		// The controls.
		{"Grant act-as on agent-hosts@theorem-research.iam.gserviceaccount.com.", "Grant act-as on [agent-hosts@theorem-research.iam.gserviceaccount.com](mailto:agent-hosts@theorem-research.iam.gserviceaccount.com)."},
		{"agent-hosts@example.com", "[agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"x agent-hosts@example.com", "x [agent-hosts@example.com](mailto:agent-hosts@example.com)"},
		{"serviceAccounts/agenthosts@example.com", "serviceAccounts/agenthosts@example.com"},
		{"xé-agent@example.com", "xé[-agent@example.com](mailto:-agent@example.com)"},
		{"agent-[hosts@theorem-research.iam.gserviceaccount.com](mailto:hosts@theorem-research.iam.gserviceaccount.com)", "agent-[hosts@theorem-research.iam.gserviceaccount.com](mailto:hosts@theorem-research.iam.gserviceaccount.com)"},
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
	// A row in a context, and Go's reading and rendering of it.
	type contextual struct {
		name, markdown, want, rendering string
		doc                             *Node
	}
	var reads []contextual
	for _, context := range contexts {
		for _, row := range rows {
			markdown := fmt.Sprintf(context.format, row.markdown)
			reads = append(reads, contextual{name: context.name + "/" + row.markdown, markdown: markdown, want: fmt.Sprintf(context.format, row.written)})
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
