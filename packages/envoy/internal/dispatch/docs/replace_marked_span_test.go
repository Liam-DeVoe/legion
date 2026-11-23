package docs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A replace whose find lies inside a marked span writes its with inside that span, so the span
// stays whole and the text around the find is written as it was. The report is
// dispatch://INFRA-1387/comment/dc3980fd-1ce6-4196-8b5b-ab6cc569a612, with the repro in
// dispatch://OSSV-690/artifact/ossv690-doc-edit-test-md: replaced inside a bold span, `said`
// split the span and wrote its edge characters as entities (`**Sami&#32;**&#97;nswered…`), and
// replaced over a bold span's whole text, `Sami bold` lost the bold. The rows after the repro's
// are the same fault at a span's start and end, in a link, italic, strikethrough and code, and
// the bounds of the rule: a with that writes the span's mark itself, and a find that spells the
// span's markup, over its whole text or across its edge, which leaves the mark to the with. A
// link's markup is spelled with its own URL and title: a titled link's find spelled with its
// title, and one whose title holds a quote and a backslash, which the with writes back so the
// stored markdown reads it as the renderer writes it and the parser reads it; and a find that is
// one link alone, spelled with its own URL, and a titled one with its URL and title, each of
// whose with writes a link to another URL, which is the one the document then holds. Since a with
// that writes a link keeps its own, those two would read the same had the document's link been
// kept, so the next row is the first find under a with of plain text, which then holds no link,
// and the row after it a find inside a link spelled under the link's own URL, whose with is then
// written outside the link. A bare URL and a bare www link are their own markup, so a find that
// covers one whole across its edge spells it; and a find that is a bare link's own text alone,
// matched as written, spells no markup, so its with is written inside the link. A find that
// spells the span's markup over part of the text it covers (`a **b** c` over `**a b c**`) spells
// it over that part alone, so the span keeps its mark. A find that runs past a span's edge
// without spelling its markup, or spells with another URL or title a link its replace would
// remove whole, is refused (TestReplaceAcrossAMarkedSpansEdgeIsRefused). Then a find that matched
// the text as written, whose `*`, `_` or backtick is text the span holds and no markup: `*`, `_`
// and a backtick inside a bold, an italic and a code span, the review's two finds (`2*3*4` in
// italic, `2**3**4` in bold), and `snake_case_name`, whose underscores no reading takes for markup.
// Each row writes one replace and reads it back three ways: the markdown the version stored, the
// document a caller reads, and the text of the paragraph the replace wrote in.
func TestReplaceInsideAMarkedSpanKeepsTheSpanWhole(t *testing.T) {
	for _, test := range []struct{ name, markdown, find, with, want, text string }{
		{
			name:     "the repro's find over a bold span's whole text",
			markdown: "Sami wrote [this](https://example.com/Sami) and **Sami bold**.\n",
			find:     "Sami bold",
			with:     "Sami answered bold",
			want:     "Sami wrote [this](https://example.com/Sami) and **Sami answered bold**.\n",
			text:     "Sami wrote this and Sami answered bold.",
		},
		{
			name:     "the repro's find inside a bold span",
			markdown: "Sami wrote [this](https://example.com/Sami) and **Sami said bold** now.\n",
			find:     "said",
			with:     "answered (https://example.com/x)",
			want:     "Sami wrote [this](https://example.com/Sami) and **Sami answered (https://example.com/x) bold** now.\n",
			text:     "Sami wrote this and Sami answered (https://example.com/x) bold now.",
		},
		{
			name:     "a find at a bold span's start",
			markdown: "**Sami said bold** now.\n",
			find:     "Sami",
			with:     "Ann",
			want:     "**Ann said bold** now.\n",
			text:     "Ann said bold now.",
		},
		{
			name:     "a find at a bold span's end",
			markdown: "**Sami said bold** now.\n",
			find:     "bold",
			with:     "loud",
			want:     "**Sami said loud** now.\n",
			text:     "Sami said loud now.",
		},
		{
			name:     "a find inside a link's text",
			markdown: "Sami wrote [this page](https://example.com/Sami) today.\n",
			find:     "page",
			with:     "note",
			want:     "Sami wrote [this note](https://example.com/Sami) today.\n",
			text:     "Sami wrote this note today.",
		},
		{
			name:     "a find over a link's whole text",
			markdown: "Sami wrote [this page](https://example.com/Sami) today.\n",
			find:     "this page",
			with:     "that note",
			want:     "Sami wrote [that note](https://example.com/Sami) today.\n",
			text:     "Sami wrote that note today.",
		},
		{
			name:     "a find inside an italic span",
			markdown: "*Sami said it* now.\n",
			find:     "said",
			with:     "answered",
			want:     "*Sami answered it* now.\n",
			text:     "Sami answered it now.",
		},
		{
			name:     "a find inside a struck span",
			markdown: "~~Sami said it~~ now.\n",
			find:     "said",
			with:     "answered",
			want:     "~~Sami answered it~~ now.\n",
			text:     "Sami answered it now.",
		},
		{
			name:     "a find inside a code span",
			markdown: "Run `make said all` now.\n",
			find:     "said",
			with:     "test",
			want:     "Run `make test all` now.\n",
			text:     "Run make test all now.",
		},
		{
			name:     "a find inside a code span that holds quotes, a caret, a dollar and pipes",
			markdown: "Run `go test ... -run '^(A|B|TestStreamsEnsureCreatesOnlyTheMeasureStream|TestStreamsUpdateChangesOnlyReplicas)$' -count=1 -v` now.\n",
			find:     "TestStreamsEnsureCreatesOnlyTheMeasureStream|TestStreamsUpdateChangesOnlyReplicas",
			with:     "TestStreamsUpdateMakesAndRaisesTheMeasureStream|TestStreamsUpdateChangesOnlyReplicas|TestStreamsUpdateMakesNoCopiedBucket",
			want:     "Run `go test ... -run '^(A|B|TestStreamsUpdateMakesAndRaisesTheMeasureStream|TestStreamsUpdateChangesOnlyReplicas|TestStreamsUpdateMakesNoCopiedBucket)$' -count=1 -v` now.\n",
			text:     "Run go test ... -run '^(A|B|TestStreamsUpdateMakesAndRaisesTheMeasureStream|TestStreamsUpdateChangesOnlyReplicas|TestStreamsUpdateMakesNoCopiedBucket)$' -count=1 -v now.",
		},
		{
			name:     "a with that writes the span's own mark",
			markdown: "**Sami said bold** now.\n",
			find:     "said",
			with:     "**answered**",
			want:     "**Sami answered bold** now.\n",
			text:     "Sami answered bold now.",
		},
		{
			name:     "a find that writes the span's markup leaves the mark to the with",
			markdown: "Sami wrote [this](https://example.com/Sami) and **Sami bold**.\n",
			find:     "**Sami bold**",
			with:     "Sami answered bold",
			want:     "Sami wrote [this](https://example.com/Sami) and Sami answered bold.\n",
			text:     "Sami wrote this and Sami answered bold.",
		},
		{
			name:     "a find that spells a link's markup across its edge leaves the link to the with",
			markdown: "Sami wrote [this](https://example.com/Sami) today.\n",
			find:     "wrote [this](https://example.com/Sami)",
			with:     "wrote that",
			want:     "Sami wrote that today.\n",
			text:     "Sami wrote that today.",
		},
		{
			name:     "a find that spells a titled link's markup with its own title leaves the link to the with",
			markdown: "I wrote [this](https://a.example/1 \"Spec\") today.\n",
			find:     "wrote [this](https://a.example/1 \"Spec\") today",
			with:     "wrote that today",
			want:     "I wrote that today.\n",
			text:     "I wrote that today.",
		},
		{
			name:     "a find that spells a link's markup with its own title holding a quote and a backslash",
			markdown: "I wrote [this](https://a.example/1 \"say \\\"hi\\\" \\\\ now\") today.\n",
			find:     "wrote [this](https://a.example/1 \"say \\\"hi\\\" \\\\ now\") today",
			with:     "wrote [that](https://a.example/1 \"say \\\"hi\\\" \\\\ now\") today",
			want:     "I wrote [that](https://a.example/1 \"say \\\"hi\\\" \\\\ now\") today.\n",
			text:     "I wrote that today.",
		},
		{
			name:     "a find that is one link alone, spelled with its own URL, holds the link its with writes",
			markdown: "I wrote [this](https://a.example/1) today.\n",
			find:     "[this](https://a.example/1)",
			with:     "[that](https://c.example/3)",
			want:     "I wrote [that](https://c.example/3) today.\n",
			text:     "I wrote that today.",
		},
		{
			name:     "a find that is one link alone, spelled with its own URL and title, holds the link its with writes",
			markdown: "I wrote [this](https://a.example/1 \"Spec\") today.\n",
			find:     "[this](https://a.example/1 \"Spec\")",
			with:     "[that](https://c.example/3)",
			want:     "I wrote [that](https://c.example/3) today.\n",
			text:     "I wrote that today.",
		},
		{
			name:     "a find that is one link alone, spelled with its own URL, leaves the link to a with of plain text",
			markdown: "I wrote [this](https://a.example/1) today.\n",
			find:     "[this](https://a.example/1)",
			with:     "that",
			want:     "I wrote that today.\n",
			text:     "I wrote that today.",
		},
		{
			name:     "a find inside a link, spelled with the link's own URL, leaves the link of its text to the with",
			markdown: "I wrote [one two](https://a.example/1) today.\n",
			find:     "[one](https://a.example/1)",
			with:     "that",
			want:     "I wrote that[ two](https://a.example/1) today.\n",
			text:     "I wrote that two today.",
		},
		{
			name:     "a find that covers a bare URL whole across its edge leaves the link to the with",
			markdown: "Read the docs at https://example.com/x today.\n",
			find:     "docs at https://example.com/x",
			with:     "docs at https://example.com/y",
			want:     "Read the docs at https://example.com/y today.\n",
			text:     "Read the docs at https://example.com/y today.",
		},
		{
			name:     "a find that covers a bare www link whole across its edge leaves the link to the with",
			markdown: "See www.example.com now.\n",
			find:     "See www.example.com",
			with:     "See www.example.org",
			want:     "See www.example.org now.\n",
			text:     "See www.example.org now.",
		},
		{
			name:     "a find that is a bare link's own text alone writes its with inside the link",
			markdown: "Read https://a.example/1 today.\n",
			find:     "https://a.example/1",
			with:     "that",
			want:     "Read [that](https://a.example/1) today.\n",
			text:     "Read that today.",
		},
		{
			name:     "a find that spells a span's markup over part of the text it covers keeps the span",
			markdown: "**a b c** now.\n",
			find:     "a **b** c",
			with:     "x",
			want:     "**x** now.\n",
			text:     "x now.",
		},
		{
			name:     "a literal * inside a bold span",
			markdown: "**a \\*\\*b\\*\\* c** now.\n",
			find:     "**b**",
			with:     "x",
			want:     "**a x c** now.\n",
			text:     "a x c now.",
		},
		{
			name:     "a literal _ inside a bold span",
			markdown: "**a \\_\\_b\\_\\_ c** now.\n",
			find:     "__b__",
			with:     "x",
			want:     "**a x c** now.\n",
			text:     "a x c now.",
		},
		{
			name:     "a literal backtick inside a bold span",
			markdown: "**a \\`b\\` c** now.\n",
			find:     "`b`",
			with:     "x",
			want:     "**a x c** now.\n",
			text:     "a x c now.",
		},
		{
			name:     "a literal * inside an italic span",
			markdown: "*a \\*b\\* c* now.\n",
			find:     "*b*",
			with:     "x",
			want:     "*a x c* now.\n",
			text:     "a x c now.",
		},
		{
			name:     "a literal _ inside an italic span",
			markdown: "*a \\_b\\_ c* now.\n",
			find:     "_b_",
			with:     "x",
			want:     "*a x c* now.\n",
			text:     "a x c now.",
		},
		{
			name:     "a literal backtick inside an italic span",
			markdown: "*a \\`b\\` c* now.\n",
			find:     "`b`",
			with:     "x",
			want:     "*a x c* now.\n",
			text:     "a x c now.",
		},
		{
			name:     "a literal * inside a code span",
			markdown: "Run `make *b* all` now.\n",
			find:     "*b*",
			with:     "x",
			want:     "Run `make x all` now.\n",
			text:     "Run make x all now.",
		},
		{
			name:     "a literal _ inside a code span",
			markdown: "Run `make _b_ all` now.\n",
			find:     "_b_",
			with:     "x",
			want:     "Run `make x all` now.\n",
			text:     "Run make x all now.",
		},
		{
			name:     "a literal backtick inside a code span",
			markdown: "Run ``make `b` all`` now.\n",
			find:     "`b`",
			with:     "x",
			want:     "Run `make x all` now.\n",
			text:     "Run make x all now.",
		},
		{
			name:     "the review's literal asterisks inside an italic span",
			markdown: "*a 2\\*3\\*4 b* now.\n",
			find:     "2*3*4",
			with:     "x",
			want:     "*a x b* now.\n",
			text:     "a x b now.",
		},
		{
			name:     "the review's literal double asterisks inside a bold span",
			markdown: "**a 2\\*\\*3\\*\\*4 b**\n",
			find:     "2**3**4",
			with:     "x",
			want:     "**a x b**\n",
			text:     "a x b",
		},
		{
			name:     "underscores inside a word inside an italic span",
			markdown: "*a snake_case_name b*\n",
			find:     "snake_case_name",
			with:     "x",
			want:     "*a x b*\n",
			text:     "a x b",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			if _, err := joinedApplyOps(service, artifactID, []model.EditOp{
				{Op: "replace", Find: test.find, With: test.with},
			}, model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil); err != nil {
				t.Fatalf("replace %q with %q: %v", test.find, test.with, err)
			}
			requireDocument(t, service, artifactID, test.want, fmt.Sprintf("replace %q with %q in %q", test.find, test.with, test.markdown))
			if text := pmdoc.TextContent(persistedProofTree(t, service.store, artifactID).Children[0]); text != test.text {
				t.Fatalf("replace %q with %q in %q leaves the paragraph's text %q, want %q", test.find, test.with, test.markdown, text, test.text)
			}
		})
	}
}

// A replace whose find crosses a marked span's edge - it covers text inside the span and text
// outside it - is refused, and the document is left as it was: text written over such a find
// lands outside the span, so a link it covered would go with its URL, and a bold, italic or struck
// span the write cut to end on a space would be written with character references
// (`**Sami said&#32;**loud`). The refusal names the span and its kind (ErrFindCrossesSpan).
// TestARefusedFindNamesTheFindThatKeepsItsRange holds each span's edge with the refusal's whole
// text; the rows here are the shapes it does not. A find that spells a bold span's markup over
// only some of the span's text it covers (`**bo**ld now`; one that spells it over all of that text
// is taken, TestAReplaceFindThatSpellsASpansOwnMarkupOverThePartItCoversIsTaken), and a find past a
// bold span's end at an apostrophe (`'s bold` in `**Sami's** bold`). A find that spells one bold
// span's markup and covers a second bold span whose markup it does not spell, which crosses the
// second span: each span is weighed alone. Then a find that spells a link with another URL or title
// than it has over the link's whole text, which its replace would remove whole: a longer find with
// the link's URL and not its title, as a find copied from an older read after another writer gave
// the link a title; a find over a link's whole text and nothing more that spells only part of it as
// another link; and a longer find spelling part of a link's text as another link. Each names the
// link the document holds, which a find over its whole text spells with the link's own URL and
// title (pmdoc.TextblockAt.SpanOver). A longer find's refusal says it covers text outside the link
// and spells the link with another URL or title; one over the link's whole text and nothing more
// says it spells the link with another URL or title, and nothing of text outside it. A find over
// part of a link that spells it with another URL or title, whose replace removes only that part,
// is taken (TestARefusedFindNamesTheFindThatKeepsItsRange).
func TestReplaceAcrossAMarkedSpansEdgeIsRefused(t *testing.T) {
	// acrossAnotherLink is what the refusal of a longer find spelling a link with another URL or
	// title says: why it crosses the link, not what text written over it would do.
	const acrossAnotherLink = "text outside it, and it spells the link with another URL or title than the one it has"
	for _, test := range []struct {
		name, markdown, find, with, kind, span string
		// markup is the span's markup as the document holds it, where the row reads it.
		markup string
		// says is words the refusal's message holds, and unsaid words it does not hold, where the
		// row reads them.
		says, unsaid string
	}{
		{
			name:     "a find that spells a bold span's markup over only some of the span's text it covers",
			markdown: "**Sami said bold** now.\n",
			find:     "**bo**ld now",
			with:     "loud then",
			kind:     "bold",
			span:     "Sami said bold",
		},
		{
			name:     "a find across a bold span's end at an apostrophe",
			markdown: "**Sami's** bold now.\n",
			find:     "'s bold",
			with:     "'s loud",
			kind:     "bold",
			span:     "Sami's",
		},
		{
			name:     "a find that spells one bold span's markup and covers a second",
			markdown: "**a** and **b** now.\n",
			find:     "**a** and b",
			with:     "x",
			kind:     "bold",
			span:     "b",
		},
		{
			name:     "a find that spells a link's markup without the title it has",
			markdown: "I wrote [this](https://a.example/1 \"Spec\") today.\n",
			find:     "wrote [this](https://a.example/1) today",
			with:     "wrote that today",
			kind:     "link",
			span:     "this",
			markup:   "[this](https://a.example/1 \"Spec\")",
			says:     acrossAnotherLink,
		},
		{
			name:     "a find over a link's whole text alone that spells part of it as another link",
			markdown: "I wrote [one two](https://a.example/1) today.\n",
			find:     "[one](https://b.example/2) two",
			with:     "that",
			kind:     "link",
			span:     "one two",
			markup:   "[one two](https://a.example/1)",
			says:     "another URL or title",
			unsaid:   "text outside it",
		},
		{
			name:     "a longer find that spells part of a link's text as another link",
			markdown: "I wrote [one two](https://a.example/1) today.\n",
			find:     "wrote [one](https://b.example/2) two today",
			with:     "wrote that today",
			kind:     "link",
			span:     "one two",
			markup:   "[one two](https://a.example/1)",
			says:     acrossAnotherLink,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			before, _ := latestVersionOf(t, service.store, artifactID)
			_, err := joinedApplyOps(service, artifactID, []model.EditOp{
				{Op: "replace", Find: test.find, With: test.with},
			}, model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil)
			var crossed *ErrFindCrossesSpan
			if !errors.As(err, &crossed) || crossed.Kind() != test.kind || crossed.Span.Text != test.span || (test.markup != "" && spanMarkup(t, service, artifactID, crossed.Span) != test.markup) {
				t.Fatalf("replace %q with %q in %q = %v, want it refused for crossing the %s %q", test.find, test.with, test.markdown, err, test.kind, test.span)
			}
			if message := err.Error(); (test.says != "" && !strings.Contains(message, test.says)) || (test.unsaid != "" && strings.Contains(message, test.unsaid)) {
				t.Fatalf("refusal = %q, want it to say %q and not %q", message, test.says, test.unsaid)
			}
			requireUnchanged(t, service, artifactID, before, test.markdown, fmt.Sprintf("the refused replace %q", test.find))
		})
	}
}

// A replace's find that runs past a span's edge and spells the span's own markup over all of the
// span's text it covers is taken, whatever the span: a link, bold, italic or code each take the
// one rule (pmdoc.TextblockAt.CrossedSpan). The with is written over the text the find covers,
// outside the span, which keeps the rest of its text and its mark, cut inside a word with no
// character reference; where the part a bold or italic span keeps would meet the with at a space,
// which markdown cannot write bold or italic delimiters beside, the find is refused.
// TestARefusedFindNamesTheFindThatKeepsItsRange sends the find its refusal names for each span's
// cut, an untitled link's among them under a with of plain text; the rows here are the links it
// does not. A titled link under a with of plain text leaves the part of the link the find covered
// unlinked, the rest of the link keeping its URL and title. A with that writes the same link, URL
// and title, joins the rest of the link, untitled or titled: the live paragraph holds one text run
// under that link, not two beside each other (requireDocument). And a find that spells a titled
// link with another title over the part it covers, whose replace removes only that part, writes
// the with outside the link, which keeps the document's URL and title.
func TestAReplaceFindThatSpellsASpansOwnMarkupOverThePartItCoversIsTaken(t *testing.T) {
	const (
		plain  = "I wrote [one two](https://a.example/1) today.\n"
		titled = "I wrote [one two](https://a.example/1 \"Spec\") today.\n"
	)
	actor := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, test := range []struct {
		name, markdown, find, with string
		// want is the document the replace leaves.
		want string
	}{
		{
			name: "a titled link, with plain text", markdown: titled, find: `wrote [one](https://a.example/1 "Spec")`, with: "NEW",
			want: "I NEW[ two](https://a.example/1 \"Spec\") today.\n",
		},
		{
			name: "a link, with written as the same link", markdown: plain, find: "wrote [one](https://a.example/1)", with: "[NEW](https://a.example/1)",
			want: "I [NEW two](https://a.example/1) today.\n",
		},
		{
			name: "a titled link, with written as the same link", markdown: titled, find: `wrote [one](https://a.example/1 "Spec")`,
			with: `[NEW](https://a.example/1 "Spec")`, want: "I [NEW two](https://a.example/1 \"Spec\") today.\n",
		},
		{
			name: "a titled link spelled with another title, with plain text", markdown: titled, find: `wrote [one](https://a.example/1 "Draft")`, with: "NEW",
			want: "I NEW[ two](https://a.example/1 \"Spec\") today.\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			if _, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: test.find, With: test.with}}, actor, nil); err != nil {
				t.Fatalf("replace %q with %q in %q: %v, want it taken", test.find, test.with, test.markdown, err)
			}
			requireDocument(t, service, artifactID, test.want, fmt.Sprintf("replace %q with %q in %q", test.find, test.with, test.markdown))
		})
	}
}

// linkDocument is the paragraph whose link each of linkSpellings spells.
const linkDocument = "I wrote [this one](https://a.example/1 \"Spec\") today.\n"

// linkSpellings are the ways a quote spells the link of linkDocument: as the document holds it or
// with another URL or title, alone, inside the link, over part of it and the text before it, or in
// a longer quote, and with no link markup at all. deleted is the document a delete of the quote
// leaves, and "" where the delete is refused: the quote spells the link with another URL or title
// over its whole text, so the delete would remove a link the quote spells otherwise
// (TestARefusedFindNamesTheFindThatKeepsItsRange holds each such shape and its refusal).
var linkSpellings = []struct{ name, quote, deleted string }{
	{name: "the link alone, spelled with another URL", quote: `[this one](https://b.example/2 "Spec")`},
	{name: "the link alone, spelled with another title", quote: `[this one](https://a.example/1 "Draft")`},
	{name: "a longer quote spelling the link with another URL", quote: `wrote [this one](https://b.example/2 "Spec") today`},
	{name: "a longer quote spelling the link with another title", quote: `wrote [this one](https://a.example/1 "Draft") today`},
	{
		name: "a quote inside the link spelling its text as another link", quote: `[this](https://b.example/2 "Spec")`,
		deleted: "I wrote [ one](https://a.example/1 \"Spec\") today.\n",
	},
	{
		name: "a quote over part of the link and the text before it, spelling that part as another link", quote: `wrote [this](https://b.example/2 "Spec")`,
		deleted: "I [ one](https://a.example/1 \"Spec\") today.\n",
	},
	{name: "the link alone, spelled as it is", quote: `[this one](https://a.example/1 "Spec")`, deleted: "I wrote  today.\n"},
	{name: "a longer quote spelling the link as it is", quote: `wrote [this one](https://a.example/1 "Spec") today`, deleted: "I .\n"},
	{
		name: "a quote inside the link under the link's own URL and title", quote: `[this](https://a.example/1 "Spec")`,
		deleted: "I wrote [ one](https://a.example/1 \"Spec\") today.\n",
	},
	{name: "a quote inside the link with no link markup", quote: "this", deleted: "I wrote [ one](https://a.example/1 \"Spec\") today.\n"},
	{
		name: "a quote over part of the link and the text before it, under the link's own URL and title", quote: `wrote [this](https://a.example/1 "Spec")`,
		deleted: "I [ one](https://a.example/1 \"Spec\") today.\n",
	},
	{name: "a quote over part of the link and the text before it, with no link markup", quote: "wrote this", deleted: "I [ one](https://a.example/1 \"Spec\") today.\n"},
}

// A find names text to change, so a delete's spells a link as a replace's does: a link the delete
// removes whole must be spelled as it is wherever the find spells it, and a delete that would
// remove one the find spells otherwise is refused (TestARefusedFindNamesTheFindThatKeepsItsRange).
// Each other spelling of linkSpellings deletes exactly the text it named: the link as the document
// holds it, alone or in a longer find, and a find over part of the link, inside it or with the
// text before it, whatever URL or title it spells the link with, since that delete removes only
// that part, and the rest of the link keeps the document's URL and title.
func TestADeleteOfAFindThatSpellsALinkDeletesWhatItNamed(t *testing.T) {
	for _, spelling := range linkSpellings {
		if spelling.deleted == "" {
			continue
		}
		t.Run(spelling.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, linkDocument)
			if _, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "delete", Find: spelling.quote}},
				model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil); err != nil {
				t.Fatalf("delete %q in %q: %v, want it taken", spelling.quote, linkDocument, err)
			}
			requireDocument(t, service, artifactID, spelling.deleted, fmt.Sprintf("delete %q in %q", spelling.quote, linkDocument))
		})
	}
}

// An insert's or a move's anchor names a place, not text to change, so it finds its block whatever
// it spells a link with: each of linkSpellings, as the document holds the link or with another URL
// or title, alone, inside the link, over part of it and the text before it, or in a longer quote,
// is taken, the insert and the moved block land after the paragraph, and the link stays as the
// document holds it.
func TestAnAnchorFindsItsBlockWhateverItSpellsALinkWith(t *testing.T) {
	const markdown = "Moved.\n\n" + linkDocument
	for _, op := range []struct {
		op string
		// edit is the operation anchored after quote, and moved the id of the block `Moved.`.
		edit func(quote, moved string) model.EditOp
		// landed is the document the operation leaves.
		landed string
	}{
		{
			op: "insert",
			edit: func(quote, _ string) model.EditOp {
				return model.EditOp{Op: "insert", After: quote, Markdown: "Inserted."}
			},
			landed: markdown + "\nInserted.\n",
		},
		{
			op: "move",
			edit: func(quote, moved string) model.EditOp {
				return model.EditOp{Op: "move", Block: moved, After: quote}
			},
			landed: linkDocument + "\nMoved.\n",
		},
	} {
		for _, spelling := range linkSpellings {
			t.Run(op.op+" after "+spelling.name, func(t *testing.T) {
				service, artifactID := newTestService(t)
				seedServiceText(t, service, artifactID, markdown)
				moved := blockIDOfText(t, liveTree(t, service, artifactID), "Moved.")
				if _, err := joinedApplyOps(service, artifactID, []model.EditOp{op.edit(spelling.quote, moved)},
					model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil); err != nil {
					t.Fatalf("%s after %q in %q: %v, want it taken", op.op, spelling.quote, markdown, err)
				}
				requireDocument(t, service, artifactID, op.landed, fmt.Sprintf("%s after %q in %q", op.op, spelling.quote, markdown))
			})
		}
	}
}

// A refused find's refusal names the find that covers the very text the find named, selects as it
// does and is taken there, which the caller sends in its place, with the occurrence at which it
// matches that text, so the edit removes what the refused one would have. The table is each shape
// of a find that spells a link with another URL or title - the whole link, untitled or titled;
// part of it; the whole link with text before or after it; part of it with text before or after
// it; a bare link, alone and in a longer find - by a delete and a replace (with `NEW`); then each
// shape of an edit that removes more than its find matched, or could: a delete whose find's list
// marker selects an item, which removes the whole item; a replace whose find's marker selects an
// item, which writes over what the find matched alone; a delete of a paragraph's whole text, which
// removes the paragraph and no text besides; and a replace whose with is blocks, which drops the
// whitespace beside its find; then each shape of a replace's find across a span's edge: a link's,
// a code span's, a struck span's, and a bold or an italic span's end and start, cut inside a word
// or at a space, the find spelling the span's markup over the part it covers or none. A link the
// edit removes whole must be spelled as it is wherever the find spells it, since the edit would
// remove a link another writer may have changed, so a find that spells such a link otherwise is
// refused, even one over part of the link whose edit removes the rest of it. An edit that removes
// only part of a link changes only the text its find named: its row reads the document it leaves,
// where exactly that text is deleted or replaced and the rest of the link keeps the document's URL
// and title. Each refused row reads the refusal's whole text, then sends the operation again with
// the find the refusal names in the find's place (ErrFindCrossesSpan.Own), with the occurrence it
// names, and reads the document that leaves: exactly what the refused edit would have removed is
// deleted or replaced - for a delete whose marker selected an item, that whole item - and the rest
// is as it was. A find is named only where one of its matches is that text, selected as the
// refused find selected it, and it is taken there. Where none is, the refusal names none: to move
// the space out of a bold or italic span where the part the find leaves would meet the written
// text at a space, which markdown cannot write the span's delimiters beside; for a struck span, to
// replace inside it and outside it in two operations
// (TestARefusalNamingNoFindGivesTheAdviceOfTheSpanThatStopsIt holds the spans that stop the find
// written from the document's text). The rows with a document of their own show where the find
// named matches at another occurrence than the refused find did: the text stands inside another
// word before the link, or bare before it, or in a second link, or in a link before it with no bold
// run; the link holds a bold run; or the selected item's link stands in a paragraph before the list
// too, or in the item before it, where the find named counts only the items it selects. One more
// is a link whose text starts on a space, which a with of blocks drops before the find.
func TestARefusedFindNamesTheFindThatKeepsItsRange(t *testing.T) {
	const (
		plain   = "I wrote [one two](https://a.example/1) today.\n"
		titled  = "I wrote [one two](https://a.example/1 \"Spec\") today.\n"
		bare    = "Read https://a.example/1 today.\n"
		list    = "- [one two](https://a.example/1) and more\n- second\n"
		whole   = "[one two](https://a.example/1) and more\n\nAfter.\n"
		spaced  = "I wrote [one ](https://a.example/1) today.\n"
		another = "spells the link \"one two\" with another URL or title than the one it has; "
		crosses = "crosses the edge of the link \"one two\": it covers text inside the link and text outside it, and " +
			"it spells the link with another URL or title than the one it has; "
		replaces = ", which then replaces the markup too, so with decides that formatting"
		inside   = ", which writes with inside the link"
	)
	occurrence := func(index int) *int { return &index }
	for _, test := range []struct {
		shape, name, markdown, op, find string
		occurrence                      *int
		// with is what a replace writes, NEW where the row names none.
		with string
		// takes is the document a find that is taken leaves; refusal is the refusal's whole text
		// after the operation's index, and leaves the document the find it names leaves, "" where
		// it names none.
		takes, refusal, leaves string
	}{
		{
			shape: "the whole link", name: "spelled with another URL", markdown: plain, op: "delete", find: "[one two](https://b.example/2)",
			refusal: `find "[one two](https://b.example/2)" ` + another + `write the link as it is in find ("[one two](https://a.example/1)")`,
			leaves:  "I wrote  today.\n",
		},
		{
			shape: "the whole link", name: "spelled with another URL", markdown: plain, op: "replace", find: "[one two](https://b.example/2)",
			refusal: `find "[one two](https://b.example/2)" ` + another + `write the link as it is in find ("[one two](https://a.example/1)")` + replaces,
			leaves:  "I wrote NEW today.\n",
		},
		{
			shape: "the whole link", name: "spelled with another URL under its own title", markdown: titled, op: "delete", find: `[one two](https://b.example/2 "Spec")`,
			refusal: `find "[one two](https://b.example/2 \"Spec\")" ` + another + `write the link as it is in find ("[one two](https://a.example/1 \"Spec\")")`,
			leaves:  "I wrote  today.\n",
		},
		{
			shape: "the whole link", name: "spelled with another URL under its own title", markdown: titled, op: "replace", find: `[one two](https://b.example/2 "Spec")`,
			refusal: `find "[one two](https://b.example/2 \"Spec\")" ` + another + `write the link as it is in find ("[one two](https://a.example/1 \"Spec\")")` + replaces,
			leaves:  "I wrote NEW today.\n",
		},
		{
			shape: "the whole link", name: "spelled with another title", markdown: titled, op: "delete", find: `[one two](https://a.example/1 "Draft")`,
			refusal: `find "[one two](https://a.example/1 \"Draft\")" ` + another + `write the link as it is in find ("[one two](https://a.example/1 \"Spec\")")`,
			leaves:  "I wrote  today.\n",
		},
		{
			shape: "the whole link", name: "spelled with another title", markdown: titled, op: "replace", find: `[one two](https://a.example/1 "Draft")`,
			refusal: `find "[one two](https://a.example/1 \"Draft\")" ` + another + `write the link as it is in find ("[one two](https://a.example/1 \"Spec\")")` + replaces,
			leaves:  "I wrote NEW today.\n",
		},
		{
			// The find's occurrence counts each place `one two` stands under a link, the first
			// link and then this one; the link as it is, spelling its bold run, matches only this
			// one, so it is named with occurrence 0.
			shape: "the whole link", name: "holding a bold run, after a link with none", markdown: "I wrote [one two](https://a.example/1) and [one **two**](https://a.example/1) today.\n",
			op: "delete", find: "[one two](https://b.example/2)", occurrence: occurrence(1),
			refusal: `find "[one two](https://b.example/2)" ` + another + `write the link as it is in find ("[one **two**](https://a.example/1)") with occurrence 0`,
			leaves:  "I wrote [one two](https://a.example/1) and  today.\n",
		},
		{
			shape: "the whole link", name: "holding a bold run, after a link with none", markdown: "I wrote [one two](https://a.example/1) and [one **two**](https://a.example/1) today.\n",
			op: "replace", find: "[one two](https://b.example/2)", occurrence: occurrence(1),
			refusal: `find "[one two](https://b.example/2)" ` + another +
				`write the link as it is in find ("[one **two**](https://a.example/1)") with occurrence 0` + replaces,
			leaves: "I wrote [one two](https://a.example/1) and NEW today.\n",
		},
		{shape: "part of the link", name: "spelled with another URL", markdown: plain, op: "delete", find: "[one](https://b.example/2)", takes: "I wrote [ two](https://a.example/1) today.\n"},
		{shape: "part of the link", name: "spelled with another URL", markdown: plain, op: "replace", find: "[one](https://b.example/2)", takes: "I wrote NEW[ two](https://a.example/1) today.\n"},
		{
			shape: "part of the link", name: "spelled with another title", markdown: titled, op: "delete", find: `[two](https://a.example/1 "Draft")`,
			takes: "I wrote [one ](https://a.example/1 \"Spec\") today.\n",
		},
		{
			shape: "part of the link", name: "spelled with another title", markdown: titled, op: "replace", find: `[two](https://a.example/1 "Draft")`,
			takes: "I wrote [one ](https://a.example/1 \"Spec\")NEW today.\n",
		},
		{
			shape: "part of the link", name: "whose text stands inside a word before the link", markdown: "Someone wrote [one two](https://a.example/1) today.\n",
			op: "delete", find: "[one](https://b.example/2)", occurrence: occurrence(0), takes: "Someone wrote [ two](https://a.example/1) today.\n",
		},
		{
			shape: "part of the link", name: "whose text stands inside a word before the link", markdown: "Someone wrote [one two](https://a.example/1) today.\n",
			op: "replace", find: "[one](https://b.example/2)", occurrence: occurrence(0), takes: "Someone wrote NEW[ two](https://a.example/1) today.\n",
		},
		{
			shape: "part of the link", name: "in the second link holding its text", markdown: "I wrote [one](https://a.example/1) and [one two](https://a.example/1) today.\n",
			op: "delete", find: "[one](https://b.example/2)", occurrence: occurrence(1),
			takes: "I wrote [one](https://a.example/1) and [ two](https://a.example/1) today.\n",
		},
		{
			shape: "part of the link", name: "in the second link holding its text", markdown: "I wrote [one](https://a.example/1) and [one two](https://a.example/1) today.\n",
			op: "replace", find: "[one](https://b.example/2)", occurrence: occurrence(1),
			takes: "I wrote [one](https://a.example/1) and NEW[ two](https://a.example/1) today.\n",
		},
		{
			shape: "part of the link", name: "holding a bold run", markdown: "I wrote [one **two** three](https://a.example/1) today.\n",
			op: "delete", find: "[one **two**](https://b.example/2)", takes: "I wrote [ three](https://a.example/1) today.\n",
		},
		{
			shape: "part of the link", name: "holding a bold run", markdown: "I wrote [one **two** three](https://a.example/1) today.\n",
			op: "replace", find: "[one **two**](https://b.example/2)", takes: "I wrote NEW[ three](https://a.example/1) today.\n",
		},
		{
			shape: "part of the link", name: "whose text stands before it under a link with no bold run",
			markdown: "I wrote [one two](https://a.example/1) and [one **two** three](https://a.example/1) today.\n",
			op:       "delete", find: "[one two](https://b.example/2)", occurrence: occurrence(1),
			takes: "I wrote [one two](https://a.example/1) and [ three](https://a.example/1) today.\n",
		},
		{
			shape: "the whole link and text", name: "before and after it, spelled with another URL", markdown: titled, op: "delete", find: `wrote [one two](https://b.example/2 "Spec") today`,
			refusal: `find "wrote [one two](https://b.example/2 \"Spec\") today" ` + crosses +
				`write the whole link as it is in find ("wrote [one two](https://a.example/1 \"Spec\") today")`,
			leaves: "I .\n",
		},
		{
			shape: "the whole link and text", name: "before and after it, spelled with another URL", markdown: titled, op: "replace", find: `wrote [one two](https://b.example/2 "Spec") today`,
			refusal: `find "wrote [one two](https://b.example/2 \"Spec\") today" ` + crosses +
				`write the whole link as it is in find ("wrote [one two](https://a.example/1 \"Spec\") today")` + replaces,
			leaves: "I NEW.\n",
		},
		{
			shape: "the whole link and text", name: "before and after it, spelled with another title", markdown: titled, op: "delete", find: `wrote [one two](https://a.example/1 "Draft") today`,
			refusal: `find "wrote [one two](https://a.example/1 \"Draft\") today" ` + crosses +
				`write the whole link as it is in find ("wrote [one two](https://a.example/1 \"Spec\") today")`,
			leaves: "I .\n",
		},
		{
			shape: "the whole link and text", name: "before and after it, spelled with another title", markdown: titled, op: "replace", find: `wrote [one two](https://a.example/1 "Draft") today`,
			refusal: `find "wrote [one two](https://a.example/1 \"Draft\") today" ` + crosses +
				`write the whole link as it is in find ("wrote [one two](https://a.example/1 \"Spec\") today")` + replaces,
			leaves: "I NEW.\n",
		},
		{
			shape: "the whole link and text", name: "after it", markdown: plain, op: "delete", find: "[one two](https://b.example/2) today",
			refusal: `find "[one two](https://b.example/2) today" ` + crosses + `write the whole link as it is in find ("[one two](https://a.example/1) today")`,
			leaves:  "I wrote .\n",
		},
		{
			shape: "the whole link and text", name: "after it", markdown: plain, op: "replace", find: "[one two](https://b.example/2) today",
			refusal: `find "[one two](https://b.example/2) today" ` + crosses + `write the whole link as it is in find ("[one two](https://a.example/1) today")` + replaces,
			leaves:  "I wrote NEW.\n",
		},
		{shape: "part of the link and text", name: "before it", markdown: plain, op: "delete", find: "wrote [one](https://b.example/2)", takes: "I [ two](https://a.example/1) today.\n"},
		{shape: "part of the link and text", name: "before it", markdown: plain, op: "replace", find: "wrote [one](https://b.example/2)", takes: "I NEW[ two](https://a.example/1) today.\n"},
		{
			shape: "part of the link and text", name: "after it", markdown: titled, op: "delete", find: `[two](https://b.example/2 "Spec") today`,
			takes: "I wrote [one ](https://a.example/1 \"Spec\").\n",
		},
		{
			shape: "part of the link and text", name: "after it", markdown: titled, op: "replace", find: `[two](https://b.example/2 "Spec") today`,
			takes: "I wrote [one ](https://a.example/1 \"Spec\")NEW.\n",
		},
		{
			shape: "part of the link and text", name: "before it, which stands bare before the link too", markdown: "I wrote one, then I wrote [one two](https://a.example/1) today.\n",
			op: "delete", find: "wrote [one](https://b.example/2)", takes: "I wrote one, then I [ two](https://a.example/1) today.\n",
		},
		{
			shape: "a bare link", name: "alone", markdown: bare, op: "delete", find: "[https://a.example/1](https://b.example/2)",
			refusal: `find "[https://a.example/1](https://b.example/2)" spells the link "https://a.example/1" with another URL or title than the one it has; ` +
				`write the link as it is in find ("https://a.example/1")`,
			leaves: "Read  today.\n",
		},
		{
			shape: "a bare link", name: "alone", markdown: bare, op: "replace", find: "[https://a.example/1](https://b.example/2)",
			refusal: `find "[https://a.example/1](https://b.example/2)" spells the link "https://a.example/1" with another URL or title than the one it has; ` +
				`write the link as it is in find ("https://a.example/1")` + inside,
			leaves: "Read [NEW](https://a.example/1) today.\n",
		},
		{
			shape: "a bare link", name: "in a longer find", markdown: bare, op: "delete", find: "Read [https://a.example/1](https://b.example/2) today",
			refusal: `find "Read [https://a.example/1](https://b.example/2) today" crosses the edge of the link "https://a.example/1": ` +
				`it covers text inside the link and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`write the whole link as it is in find ("Read https://a.example/1 today")`,
			leaves: ".\n",
		},
		{
			shape: "a bare link", name: "in a longer find", markdown: bare, op: "replace", find: "Read [https://a.example/1](https://b.example/2) today",
			refusal: `find "Read [https://a.example/1](https://b.example/2) today" crosses the edge of the link "https://a.example/1": ` +
				`it covers text inside the link and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`write the whole link as it is in find ("Read https://a.example/1 today")` + replaces,
			leaves: "NEW.\n",
		},
		{
			// The delete removes the whole item its marker selects, so the link the find spells
			// with another URL over part of its text goes whole: the find named keeps the
			// marker, selects that item and removes it.
			shape: "a list item its marker selects", name: "part of its link spelled with another URL", markdown: list, op: "delete", find: "- [one](https://b.example/2)",
			refusal: `find "- [one](https://b.example/2)" selects a list item, which the delete removes whole with the link "one two", ` +
				`and find spells that link with another URL or title than the one it has; write the link as it is in find ("- [one](https://a.example/1)")`,
			leaves: "- second\n",
		},
		{
			shape: "a list item its marker selects", name: "its whole link spelled with another URL", markdown: list, op: "delete", find: "- [one two](https://b.example/2)",
			refusal: `find "- [one two](https://b.example/2)" ` + another + `write the link as it is in find ("- [one two](https://a.example/1)")`,
			leaves:  "- second\n",
		},
		{
			// A paragraph before the list holds the link too, which a find with no marker
			// matches; the find named, with its marker, counts only the items it selects.
			shape: "a list item its marker selects", name: "its whole link spelled with another URL, after a paragraph holding that link",
			markdown: "I wrote [one two](https://a.example/1).\n\n" + list, op: "delete", find: "- [one two](https://b.example/2)",
			refusal: `find "- [one two](https://b.example/2)" ` + another + `write the link as it is in find ("- [one two](https://a.example/1)")`,
			leaves:  "I wrote [one two](https://a.example/1).\n\n- second\n",
		},
		{
			// Both items hold the link, and the find's occurrence selects the second; the find
			// named, with its marker, counts the items it selects, so it selects that item too.
			shape: "a list item its marker selects", name: "its whole link spelled with another URL, in the second of two items holding that link",
			markdown: "- [one two](https://a.example/1) and more\n- [one two](https://a.example/1) and less\n",
			op:       "delete", find: "- [one two](https://b.example/2)", occurrence: occurrence(1),
			refusal: `find "- [one two](https://b.example/2)" ` + another + `write the link as it is in find ("- [one two](https://a.example/1)") with occurrence 1`,
			leaves:  "- [one two](https://a.example/1) and more\n",
		},
		{
			// A replace writes over what its find matched alone, whatever selected it, so part of
			// the link spelled otherwise is taken as it is with no marker.
			shape: "a list item its marker selects", name: "part of its link spelled with another URL", markdown: list, op: "replace", find: "- [one](https://b.example/2)",
			takes: "- NEW[ two](https://a.example/1) and more\n- second\n",
		},
		{
			shape: "a list item its marker selects", name: "its whole link spelled with another URL", markdown: list, op: "replace", find: "- [one two](https://b.example/2)",
			refusal: `find "- [one two](https://b.example/2)" ` + another + `write the link as it is in find ("- [one two](https://a.example/1)")` + replaces,
			leaves:  "- NEW and more\n- second\n",
		},
		{
			// The delete of a paragraph's whole text removes the paragraph, and no text but what
			// the find matched, so every link of it is one the find covers whole.
			shape: "a paragraph's whole text", name: "part of its link spelled with another URL", markdown: whole, op: "delete", find: "[one](https://b.example/2) two and more",
			refusal: `find "[one](https://b.example/2) two and more" ` + crosses + `write the whole link as it is in find ("[one two](https://a.example/1) and more")`,
			leaves:  "After.\n",
		},
		{
			shape: "a paragraph's whole text", name: "its whole link spelled with another URL", markdown: whole, op: "delete", find: "[one two](https://b.example/2) and more",
			refusal: `find "[one two](https://b.example/2) and more" ` + crosses + `write the whole link as it is in find ("[one two](https://a.example/1) and more")`,
			leaves:  "After.\n",
		},
		{
			// Blocks written over the find split the paragraph and drop the space after it, all
			// the link holds beyond the find, so the replace removes the link whole.
			shape: "a link whose text ends on a space", name: "part of it spelled with another URL, with blocks", markdown: spaced,
			op: "replace", find: "[one](https://b.example/2)", with: "- x\n- y",
			refusal: `find "[one](https://b.example/2)" covers part of the link "one ", whose rest is whitespace beside find that a with of blocks drops, ` +
				`so the replace removes the link whole, and find spells that link with another URL or title than the one it has; ` +
				`write the link as it is in find ("[one](https://a.example/1)")` + replaces,
			leaves: "I wrote\n\n- x\n- y\n\n&#32;today.\n",
		},
		{
			// The finder matches the find's text without the space it ends on.
			shape: "a link whose text ends on a space", name: "the whole of it spelled with another URL, with blocks", markdown: spaced,
			op: "replace", find: "[one ](https://b.example/2)", with: "- x\n- y",
			refusal: `find "[one ](https://b.example/2)" covers part of the link "one ", whose rest is whitespace beside find that a with of blocks drops, ` +
				`so the replace removes the link whole, and find spells that link with another URL or title than the one it has; ` +
				`write the link as it is in find ("[one](https://a.example/1)")` + replaces,
			leaves: "I wrote\n\n- x\n- y\n\n&#32;today.\n",
		},
		{
			// A with of one paragraph is written over the find alone, which leaves the link its
			// space.
			shape: "a link whose text ends on a space", name: "part of it spelled with another URL", markdown: spaced, op: "replace", find: "[one](https://b.example/2)",
			takes: "I wrote NEW[ ](https://a.example/1) today.\n",
		},
		{
			// The link's text opens on a space, the end of the run before the find, which blocks
			// written over the find drop too, so the replace removes the link whole.
			shape: "a link whose text starts on a space", name: "part of it spelled with another URL, with blocks", markdown: "I wrote[ one](https://a.example/1) today.\n",
			op: "replace", find: "[one](https://b.example/2)", with: "- x\n- y",
			refusal: `find "[one](https://b.example/2)" covers part of the link " one", whose rest is whitespace beside find that a with of blocks drops, ` +
				`so the replace removes the link whole, and find spells that link with another URL or title than the one it has; ` +
				`write the link as it is in find ("[one](https://a.example/1)")` + replaces,
			leaves: "I wrote\n\n- x\n- y\n\ntoday.\n",
		},
		{
			shape: "a link's edge", name: "the whole link and text before it", markdown: "Sami wrote [this](https://example.com/Sami) today.\n",
			op: "replace", find: "wrote this",
			refusal: `find "wrote this" crosses the edge of the link "this": it covers text inside the link and text outside it, and ` +
				`text written over both would not keep the link whole; to keep the text find names, ` +
				`spell the link's markup over the part of it find covers ("wrote [this](https://example.com/Sami)")` + replaces,
			leaves: "Sami NEW today.\n",
		},
		{
			shape: "a link's edge", name: "part of the link and text before it", markdown: plain, op: "replace", find: "wrote one",
			refusal: `find "wrote one" crosses the edge of the link "one two": it covers text inside the link and text outside it, and ` +
				`text written over both would not keep the link whole; to keep the text find names, ` +
				`spell the link's markup over the part of it find covers ("wrote [one](https://a.example/1)")` + replaces,
			leaves: "I NEW[ two](https://a.example/1) today.\n",
		},
		{
			shape: "a link's edge", name: "a second link, past one spelled", markdown: "I wrote [this](https://a.example/1) and [that](https://b.example/2) today.\n",
			op: "replace", find: "wrote [this](https://a.example/1) and that",
			refusal: `find "wrote [this](https://a.example/1) and that" crosses the edge of the link "that": it covers text inside the link and text outside it, and ` +
				`text written over both would not keep the link whole; to keep the text find names, ` +
				`spell the link's markup over the part of it find covers ("wrote [this](https://a.example/1) and [that](https://b.example/2)")` + replaces,
			leaves: "I NEW today.\n",
		},
		{
			shape: "a code span's end", name: "with text after it", markdown: "Run `make said all` now.\n", op: "replace", find: "all now",
			refusal: `find "all now" crosses the edge of the code span "make said all": it covers text inside the code span and text outside it, and ` +
				"text written over both would not keep the code span whole; to keep the text find names, " +
				"spell the code span's markup over the part of it find covers (\"`all` now\")" + replaces,
			leaves: "Run `make said `NEW.\n",
		},
		{
			// No find spells strikethrough's markup, so two operations keep the text: one inside
			// the struck span, one outside it.
			shape: "a struck span's end", name: "with text after it", markdown: "~~Sami said it~~ now.\n", op: "replace", find: "it now",
			refusal: `find "it now" crosses the edge of the strikethrough span "Sami said it": it covers text inside the strikethrough span and text outside it, and ` +
				`text written over both would not keep the strikethrough span whole; narrow find to the text inside the strikethrough span, ` +
				`and replace the text outside it in an operation of its own, since a find cannot spell strikethrough's markup`,
		},
		{
			shape: "a bold span's end", name: "cut after a space", markdown: "**Sami said bold** now.\n", op: "replace", find: "bold now",
			refusal: `find "bold now" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; no find keeps the text find names, since the part of the bold span it leaves, ` +
				`"Sami said ", would meet the text written over it at a space, where markdown cannot write the bold span's delimiters; move that space out of the bold span`,
		},
		{
			shape: "a bold span's end", name: "cut inside a word", markdown: "**Sami said bold** now.\n", op: "replace", find: "old now",
			refusal: `find "old now" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; to keep the text find names, ` +
				`spell the bold span's markup over the part of it find covers ("**old** now")` + replaces,
			leaves: "**Sami said b**NEW.\n",
		},
		{
			shape: "a bold span's start", name: "cut before a space", markdown: "Then **Sami said bold**.\n", op: "replace", find: "Then Sami",
			refusal: `find "Then Sami" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; no find keeps the text find names, since the part of the bold span it leaves, ` +
				`" said bold", would meet the text written over it at a space, where markdown cannot write the bold span's delimiters; move that space out of the bold span`,
		},
		{
			shape: "a bold span's start", name: "cut inside a word", markdown: "Then **Sami said bold**.\n", op: "replace", find: "Then Sa",
			refusal: `find "Then Sa" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; to keep the text find names, ` +
				`spell the bold span's markup over the part of it find covers ("Then **Sa**")` + replaces,
			leaves: "NEW**mi said bold**.\n",
		},
		{
			shape: "an italic span's end", name: "cut after a space", markdown: "*Sami said it* now.\n", op: "replace", find: "it now",
			refusal: `find "it now" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; no find keeps the text find names, since the part of the italic span it leaves, ` +
				`"Sami said ", would meet the text written over it at a space, where markdown cannot write the italic span's delimiters; move that space out of the italic span`,
		},
		{
			shape: "an italic span's end", name: "cut inside a word", markdown: "*Sami said it* now.\n", op: "replace", find: "t now",
			refusal: `find "t now" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; to keep the text find names, ` +
				`spell the italic span's markup over the part of it find covers ("*t* now")` + replaces,
			leaves: "*Sami said i*NEW.\n",
		},
		{
			shape: "an italic span's start", name: "cut before a space", markdown: "Then *Sami said it*.\n", op: "replace", find: "Then Sami",
			refusal: `find "Then Sami" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; no find keeps the text find names, since the part of the italic span it leaves, ` +
				`" said it", would meet the text written over it at a space, where markdown cannot write the italic span's delimiters; move that space out of the italic span`,
		},
		{
			shape: "an italic span's start", name: "cut inside a word", markdown: "Then *Sami said it*.\n", op: "replace", find: "Then Sa",
			refusal: `find "Then Sa" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; to keep the text find names, ` +
				`spell the italic span's markup over the part of it find covers ("Then *Sa*")` + replaces,
			leaves: "NEW*mi said it*.\n",
		},
		{
			// The find spells the bold span's markup over the part of it it covers, which the one
			// rule takes, but the part it leaves would end on a space, which markdown cannot close
			// bold after, and that same spelling is the only find over this text.
			shape: "a bold span's end, spelled", name: "cut after a space", markdown: "**Sami said bold** now.\n", op: "replace", find: "**bold** now",
			refusal: `find "**bold** now" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`the part of the bold span it leaves, "Sami said ", would meet the text written over it at a space, ` +
				`where markdown cannot write the bold span's delimiters; move that space out of the bold span`,
		},
		{
			shape: "a bold span's start, spelled", name: "cut before a space", markdown: "Then **Sami said bold**.\n", op: "replace", find: "Then **Sami**",
			refusal: `find "Then **Sami**" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`the part of the bold span it leaves, " said bold", would meet the text written over it at a space, ` +
				`where markdown cannot write the bold span's delimiters; move that space out of the bold span`,
		},
		{
			shape: "an italic span's end, spelled", name: "cut after a space", markdown: "*Sami said it* now.\n", op: "replace", find: "*it* now",
			refusal: `find "*it* now" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`the part of the italic span it leaves, "Sami said ", would meet the text written over it at a space, ` +
				`where markdown cannot write the italic span's delimiters; move that space out of the italic span`,
		},
		{
			shape: "an italic span's start, spelled", name: "cut before a space", markdown: "Then *Sami said it*.\n", op: "replace", find: "Then *Sami*",
			refusal: `find "Then *Sami*" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`the part of the italic span it leaves, " said it", would meet the text written over it at a space, ` +
				`where markdown cannot write the italic span's delimiters; move that space out of the italic span`,
		},
	} {
		t.Run(test.op+" of "+test.shape+", "+test.name, func(t *testing.T) {
			with := test.with
			if with == "" {
				with = "NEW"
			}
			edit := func(find string, occurrence *int) []model.EditOp {
				if test.op == "delete" {
					return []model.EditOp{{Op: "delete", Find: find, Occurrence: occurrence}}
				}
				return []model.EditOp{{Op: "replace", Find: find, With: with, Occurrence: occurrence}}
			}
			actor := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			before, _ := latestVersionOf(t, service.store, artifactID)
			_, err := joinedApplyOps(service, artifactID, edit(test.find, test.occurrence), actor, nil)
			if test.takes != "" {
				if err != nil {
					t.Fatalf("%s %q in %q: %v, want it taken", test.op, test.find, test.markdown, err)
				}
				requireDocument(t, service, artifactID, test.takes, fmt.Sprintf("%s %q in %q", test.op, test.find, test.markdown))
				return
			}
			var crossed *ErrFindCrossesSpan
			if !errors.As(err, &crossed) {
				t.Fatalf("%s %q in %q = %v, want it refused with the refusal\n%s", test.op, test.find, test.markdown, err, test.refusal)
			}
			requireUnchanged(t, service, artifactID, before, test.markdown, fmt.Sprintf("the refused %s %q", test.op, test.find))
			if refusal := strings.TrimPrefix(err.Error(), "operation 0: "); refusal != test.refusal {
				t.Errorf("%s %q in %q: refusal\n%s\nwant\n%s", test.op, test.find, test.markdown, refusal, test.refusal)
			}
			// The find the refusal names (Own) is sent in the find's place, with the occurrence it
			// names, so a find named that does not do what the refused edit would have fails here
			// whatever text the row wants.
			if (crossed.Own.Quote == "") != (test.leaves == "") {
				t.Fatalf("%s %q in %q: the refusal names the find %q, want one exactly where the row says what it leaves (%q)",
					test.op, test.find, test.markdown, crossed.Own.Quote, test.leaves)
			}
			if test.leaves == "" {
				return
			}
			t.Run(crossed.Own.Quote, func(t *testing.T) {
				service, artifactID := newTestService(t)
				seedServiceText(t, service, artifactID, test.markdown)
				if _, err := joinedApplyOps(service, artifactID, edit(crossed.Own.Quote, crossed.Own.Occurrence), actor, nil); err != nil {
					t.Fatalf("%s %q, which the refusal of %q names, in %q: %v, want it taken", test.op, crossed.Own.Quote, test.find, test.markdown, err)
				}
				requireDocument(t, service, artifactID, test.leaves, fmt.Sprintf("%s %q, which the refusal of %q names, in %q", test.op, crossed.Own.Quote, test.find, test.markdown))
			})
		})
	}
}

// Where the find written from the document's text over the text a refused find named would be
// refused itself, for a span that find crosses too, the refusal names no find and gives that
// span's advice, naming it, and following the advice leaves what the refused find meant. A
// replace's find from a link it spells with another URL into a struck span: the link as it is
// crosses the struck span too, whose markup no find spells, so the refusal says to replace the
// text inside the struck span and the text outside it in two operations, which are taken. A
// replace's find from a bold span into an italic span (`b c d` over `**ab** c *d e*`): the find
// spelling both spans leaves the italic span's ` e`, which would meet the written text at a space,
// so the refusal says to move that space out of the italic span; in the document with that space
// moved out (`**ab** c *d* *e*`), the same find is refused naming the find that spells both spans,
// which is taken there.
func TestARefusalNamingNoFindGivesTheAdviceOfTheSpanThatStopsIt(t *testing.T) {
	actor := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	// refused is the refusal of a replace of find with NEW in a document seeded with markdown,
	// which it leaves as it was.
	refused := func(t *testing.T, markdown, find string) *ErrFindCrossesSpan {
		t.Helper()
		service, artifactID := newTestService(t)
		seedServiceText(t, service, artifactID, markdown)
		before, _ := latestVersionOf(t, service.store, artifactID)
		_, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: find, With: "NEW"}}, actor, nil)
		var crossed *ErrFindCrossesSpan
		if !errors.As(err, &crossed) {
			t.Fatalf("replace %q in %q = %v, want it refused for crossing a span", find, markdown, err)
		}
		requireUnchanged(t, service, artifactID, before, markdown, fmt.Sprintf("the refused replace %q", find))
		return crossed
	}
	// taken requires ops, sent in one batch on a document seeded with markdown, to leave want.
	taken := func(t *testing.T, markdown string, ops []model.EditOp, want string) {
		t.Helper()
		service, artifactID := newTestService(t)
		seedServiceText(t, service, artifactID, markdown)
		if _, err := joinedApplyOps(service, artifactID, ops, actor, nil); err != nil {
			t.Fatalf("%+v in %q: %v, want it taken", ops, markdown, err)
		}
		requireDocument(t, service, artifactID, want, fmt.Sprintf("%+v in %q", ops, markdown))
	}
	t.Run("a find from a link it spells with another URL into a struck span", func(t *testing.T) {
		const markdown = "I wrote [one two](https://a.example/1) ~~three four~~ today.\n"
		crossed := refused(t, markdown, "[one two](https://b.example/2) three")
		want := `operation 0: find "[one two](https://b.example/2) three" crosses the edge of the link "one two": ` +
			`it covers text inside the link and text outside it, and it spells the link with another URL or title than the one it has; ` +
			`no find keeps the text find names, since it crosses the edge of the strikethrough span "three four" too: ` +
			`narrow find to the text inside the strikethrough span, and replace the text outside it in an operation of its own, ` +
			`since a find cannot spell strikethrough's markup`
		if crossed.Error() != want || crossed.Own.Quote != "" {
			t.Fatalf("refusal =\n%s\nnaming %q, want\n%s\nnaming none", crossed, crossed.Own.Quote, want)
		}
		taken(t, markdown, []model.EditOp{
			{Op: "replace", Find: "three", With: "3"},
			{Op: "replace", Find: "[one two](https://a.example/1)", With: "NEW"},
		}, "I wrote NEW ~~3 four~~ today.\n")
	})
	t.Run("a find from a bold span into an italic span it leaves on a space", func(t *testing.T) {
		crossed := refused(t, "**ab** c *d e*\n", "b c d")
		want := `operation 0: find "b c d" crosses the edge of the bold span "ab": it covers text inside the bold span and text outside it, and ` +
			`text written over both would not keep the bold span whole; no find keeps the text find names, ` +
			`since it crosses the edge of the italic span "d e" too, and the part of the italic span it leaves, " e", ` +
			`would meet the text written over it at a space, where markdown cannot write the italic span's delimiters; move that space out of the italic span`
		if crossed.Error() != want || crossed.Own.Quote != "" {
			t.Fatalf("refusal =\n%s\nnaming %q, want\n%s\nnaming none", crossed, crossed.Own.Quote, want)
		}
		const moved = "**ab** c *d* *e*\n"
		if named := refused(t, moved, "b c d").Own; named.Quote != "**b** c *d*" || named.Occurrence != nil {
			t.Fatalf("with the space moved out of the italic span, the refusal of %q names %+v, want %q", "b c d", named, "**b** c *d*")
		}
		taken(t, moved, []model.EditOp{{Op: "replace", Find: "**b** c *d*", With: "NEW"}}, "**a**NEW *e*\n")
	})
}

// requireDocument fails unless the write before it left artifactID's document reading want, as a
// caller reads it and as the latest version stored holds it, and its live tree holds no two text
// runs beside each other under the same marks, which the markdown writes as one. what names the
// write. A write that left the document reading anything else fails on the read at once. Then it
// waits for the latest version to read want, bounded by the test binary's deadline alone, as
// waitForDocumentVersion waits: the seed stores no version of its own, and the settlement after
// the write stores what both wrote, unless a read of the room between the seed and the write armed
// the seed's settlement, which then stores the seed's text first and the write's after it.
func requireDocument(t *testing.T, service *Service, artifactID, want, what string) {
	t.Helper()
	read, err := service.Text(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}
	if read != want {
		t.Fatalf("%s:\n read markdown %q\nwant %q", what, read, want)
	}
	testwait.Until(t, fmt.Sprintf("%s: the latest stored version to read %q", what, want), func() (string, bool) {
		stored, _ := latestVersionOf(t, service.store, artifactID)
		return fmt.Sprintf("stored markdown %q", stored), stored == want
	})
	pmdoc.Walk(liveTree(t, service, artifactID), func(node *pmdoc.Node) bool {
		for index := 1; index < len(node.Children); index++ {
			if left, right := node.Children[index-1], node.Children[index]; left.Type == "text" && right.Type == "text" && reflect.DeepEqual(left.Marks, right.Marks) {
				t.Fatalf("%s leaves the runs %q and %q beside each other under the marks %v", what, left.Text, right.Text, left.Marks)
			}
		}
		return true
	})
}

// requireUnchanged fails unless a refused write, what, left artifactID's document as markdown, the
// text it was seeded with over the version before, before. A refused write stores no version: the
// latest is the one before the seed, or the seed's own if a settlement has stored it since.
func requireUnchanged(t *testing.T, service *Service, artifactID, before, markdown, what string) {
	t.Helper()
	read, err := service.Text(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}
	if stored, _ := latestVersionOf(t, service.store, artifactID); (stored != before && stored != markdown) || read != markdown {
		t.Fatalf("after %s in %q:\n stored markdown %q, was %q\n read markdown   %q\nwant both unchanged", what, markdown, stored, before, read)
	}
}

// spanMarkup is the markup a find spells span with over its whole text, the document's own
// (pmdoc.TextblockAt.SpanOver), as the stored document holds it.
func spanMarkup(t *testing.T, service *Service, artifactID string, span pmdoc.Span) string {
	t.Helper()
	at, ok := pmdoc.ContainingTextblock(persistedProofTree(t, service.store, artifactID), span.Range.From)
	if !ok {
		t.Fatalf("no textblock of the stored document holds the span %q at %d", span.Text, span.Range.From)
	}
	markup, err := at.SpanOver(span.Mark, span.Range)
	if err != nil {
		t.Fatalf("spell the span %q: %v", span.Text, err)
	}
	return markup
}

// A delete's find and an insert's or a move's anchor write no text over their match, so the
// shapes a replace's find is refused for crossing a span's edge
// (TestReplaceAcrossAMarkedSpansEdgeIsRefused) are taken from them: a quote across a bold, an
// italic or a code span's end, and plain text over a link (`wrote this` over `wrote [this](…)`),
// which spells no link. The delete removes the text it matched, a bold or an italic span it cut
// to end on a space keeping its delimiter with a character reference; the insert and the moved
// block land after the paragraph, which keeps its spans.
func TestADeleteOrAnAnchorAcrossASpansEdgeIsTaken(t *testing.T) {
	shapes := []struct {
		name, paragraph, quote string
		// deleted is the paragraph the delete of quote leaves.
		deleted string
	}{
		{name: "a quote across a bold span's end", paragraph: "**Sami said bold** now.", quote: "bold now", deleted: "**Sami said&#32;**."},
		{name: "a quote across an italic span's end", paragraph: "*Sami said it* now.", quote: "it now", deleted: "*Sami said&#32;*."},
		{name: "a quote across a code span's end", paragraph: "Run `make said all` now.", quote: "all now", deleted: "Run `make said `."},
		{name: "plain text over a link", paragraph: "I wrote [this](https://a.example/1) today.", quote: "wrote this", deleted: "I  today."},
	}
	ops := []struct {
		op string
		// edit is the operation quoting quote, and moved the id of the block `Moved.`.
		edit func(quote, moved string) model.EditOp
		// want is the document the operation leaves of the paragraph, or of what its delete left.
		want func(paragraph, deleted string) string
	}{
		{
			op:   "delete",
			edit: func(quote, _ string) model.EditOp { return model.EditOp{Op: "delete", Find: quote} },
			want: func(_, deleted string) string { return "Moved.\n\n" + deleted + "\n" },
		},
		{
			op: "insert",
			edit: func(quote, _ string) model.EditOp {
				return model.EditOp{Op: "insert", After: quote, Markdown: "Inserted."}
			},
			want: func(paragraph, _ string) string { return "Moved.\n\n" + paragraph + "\n\nInserted.\n" },
		},
		{
			op: "move",
			edit: func(quote, moved string) model.EditOp {
				return model.EditOp{Op: "move", Block: moved, After: quote}
			},
			want: func(paragraph, _ string) string { return paragraph + "\n\nMoved.\n" },
		},
	}
	for _, op := range ops {
		for _, shape := range shapes {
			t.Run(op.op+" of "+shape.name, func(t *testing.T) {
				markdown := "Moved.\n\n" + shape.paragraph + "\n"
				service, artifactID := newTestService(t)
				seedServiceText(t, service, artifactID, markdown)
				moved := blockIDOfText(t, liveTree(t, service, artifactID), "Moved.")
				if _, err := joinedApplyOps(service, artifactID, []model.EditOp{op.edit(shape.quote, moved)},
					model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil); err != nil {
					t.Fatalf("%s %q in %q: %v, want it taken", op.op, shape.quote, markdown, err)
				}
				requireDocument(t, service, artifactID, op.want(shape.paragraph, shape.deleted), fmt.Sprintf("%s %q in %q", op.op, shape.quote, markdown))
			})
		}
	}
}

// An accepted suggestion's text lands inside the marked span the suggestion lay inside, as a
// replace's does (TestReplaceInsideAMarkedSpanKeepsTheSpanWhole): accepting a suggestion over
// `said` in `**Sami said bold**` split the span and wrote its edges as entities, and one over a
// bold span's whole text lost the bold. Each row marks a suggestion on its quote as the margin
// does, accepts it, and reads the document back three ways: the markdown the store holds, the
// document a caller reads, and the text of the paragraph the accept wrote in.
func TestAcceptedSuggestionInsideAMarkedSpanKeepsTheSpanWhole(t *testing.T) {
	for _, test := range []struct{ name, markdown, quote, accepted, want, text string }{
		{
			name:     "a suggestion inside a bold span",
			markdown: "**Sami said bold** now.\n",
			quote:    "said",
			accepted: "answered",
			want:     "**Sami answered bold** now.\n",
			text:     "Sami answered bold now.",
		},
		{
			name:     "a suggestion over a bold span's whole text",
			markdown: "Sami wrote [this](https://example.com/Sami) and **Sami bold**.\n",
			quote:    "Sami bold",
			accepted: "Sami answered bold",
			want:     "Sami wrote [this](https://example.com/Sami) and **Sami answered bold**.\n",
			text:     "Sami wrote this and Sami answered bold.",
		},
		{
			name:     "a suggestion inside a link's text",
			markdown: "Sami wrote [this page](https://example.com/Sami) today.\n",
			quote:    "page",
			accepted: "note",
			want:     "Sami wrote [this note](https://example.com/Sami) today.\n",
			text:     "Sami wrote this note today.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			spec := MarkSpec{Kind: MarkSuggestion, ID: "s1", By: model.Actor{Kind: "session", ID: "s1"}}
			if _, err := joinedMarkQuote(service, artifactID, spec, test.quote, nil); err != nil {
				t.Fatalf("suggest over %q: %v", test.quote, err)
			}
			if err := joinedAcceptSuggestion(service, artifactID, "s1", test.accepted, model.Actor{Kind: "user", ID: "alice"}); err != nil {
				t.Fatalf("accept %q over %q: %v", test.accepted, test.quote, err)
			}
			stored := storedMarkdown(t, service.store, artifactID)
			read, err := service.Text(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the document: %v", err)
			}
			text := pmdoc.TextContent(persistedProofTree(t, service.store, artifactID).Children[0])
			if stored != test.want || read != test.want || text != test.text {
				t.Fatalf("accept %q over %q in %q:\n stored markdown %q\n read markdown   %q\n text            %q\nwant markdown %q and text %q",
					test.accepted, test.quote, test.markdown, stored, read, text, test.want, test.text)
			}
		})
	}
}
