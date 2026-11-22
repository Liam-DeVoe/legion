package docs

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
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
// covers one whole across its edge spells it, where it was refused with that same find as the
// markup to spell; and a find that is a bare link's own text alone, matched as written, spells no
// markup, so its with is written inside the link. A find that spells the span's markup over part
// of the text it covers (`a **b** c` over `**a b c**`) spells it over that part alone, so the span
// keeps its mark, where the with was written without the bold. A find that runs past a span's
// edge without spelling its markup, or spells a link with another URL or title, is refused
// (TestReplaceAcrossAMarkedSpansEdgeIsRefused). Then a find that matched the text as written,
// whose `*`, `_` or backtick is text the span holds and no markup: `*`, `_` and a backtick inside
// a bold, an italic and a code span, the review's two finds (`2*3*4` in italic, `2**3**4` in
// bold), and `snake_case_name`, whose underscores no reading takes for markup.
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
			waitForDocumentVersion(t, service.store, artifactID, 2)
			stored, _ := latestVersionOf(t, service.store, artifactID)
			read, err := service.Text(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the document: %v", err)
			}
			text := pmdoc.TextContent(persistedProofTree(t, service.store, artifactID).Children[0])
			if stored != test.want || read != test.want || text != test.text {
				t.Fatalf("replace %q with %q in %q:\n stored markdown %q\n read markdown   %q\n text            %q\nwant markdown %q and text %q",
					test.find, test.with, test.markdown, stored, read, text, test.want, test.text)
			}
		})
	}
}

// A replace whose find crosses a marked span's edge - it covers text inside the span and text
// outside it - is refused, and the document is left as it was. Text written over such a find
// lands outside the span: over the tester's `wrote this`, where only `this` is the link, the link
// and its URL were gone with no warning, and a bold, italic or struck span the write cut to end
// on a space was written with character references (`**Sami said&#32;**loud`). The refusal names
// the span and its kind (ErrFindCrossesSpan). The rows are the link, bold and italic edges, a
// strikethrough's and a code span's, a find that spells a bold span's markup over only some of the
// span's text it covers (`**bo**ld now`; one that spells it over all of that text is taken,
// TestAReplaceFindThatSpellsASpansOwnMarkupOverThePartItCoversIsTaken), and the find past a bold
// span's end (`'s bold` in `**Sami's** bold`) that TestReplaceInsideAMarkedSpanKeepsTheSpanWhole
// once wrote outside the span, as `**Sami**'s loud`.
// The next two are a find that spells one span's markup and covers a second span of that kind
// whose markup it does not spell: the second link lost its URL, and the second bold its bold,
// since the find was read as spelling every span of the kind once it spelled one. The next two are
// a find that spells a link's markup with another URL, and one that spells it with the link's URL
// and not its title, as finds copied from an older read after another writer changed the URL or
// gave the link a title: each was written, and the URL or the title the document held went with
// no word. The next two are a find that is that one link alone, spelled with another URL and with
// another title: it lay inside the link, so it was written there under the URL and title the
// document held, where the same spelling in a longer find was refused. The last five are that
// spelling in other shapes: four that were taken - a bare link spelled as a link to another URL,
// alone and in a longer find, which the bare link's own exemption let in; a find over a link's
// whole text that spells only part of it as another link, taken alone; and a find inside a link
// that spells its text as another link, taken there - and the control, the row after the third,
// a longer find spelling part of a link's text as another link, which was refused already. Each
// refusal gives the link's markup as the document holds it. A longer find's refusal says it
// covers text outside the link and that it spells the link with another URL or title, where it
// said text written over both would not keep the link whole, though the find of a delete
// spelling the link the same way was refused for spelling it. A find over the link's whole text
// and nothing more says it spells the link with another URL or title, to write the link as it
// is, and nothing of text outside it; for a bare link it says with is then written inside the
// link, where it said with decides that formatting, which a find that is the link's own text
// spells none of. A find inside the link says to quote the text it names under the link's own URL
// and title, or with no link markup, where it said to write the whole link as it is, which would
// replace text the find did not name; TestARefusedFindNamesTheFindsThatKeepItsRange reads each
// refusal of such a find whole and sends each find it names.
func TestReplaceAcrossAMarkedSpansEdgeIsRefused(t *testing.T) {
	// acrossAnotherLink is what the refusal of a longer find spelling a link with another URL or
	// title says: why it crosses the link, not what text written over it would do.
	const acrossAnotherLink = "text outside it, and it spells the link with another URL or title than the one it has"
	for _, test := range []struct {
		name, markdown, find, with, kind, span string
		// markup is the span's markup the refusal says to spell, where the row reads it.
		markup string
		// says is words the refusal's message holds, and unsaid words it does not hold, where the
		// row reads them.
		says, unsaid string
	}{
		{
			name:     "a find across a link's start",
			markdown: "Sami wrote [this](https://example.com/Sami) today.\n",
			find:     "wrote this",
			with:     "wrote that",
			kind:     "link",
			span:     "this",
		},
		{
			name:     "a find across a bold span's end",
			markdown: "**Sami said bold** now.\n",
			find:     "bold now",
			with:     "loud then",
			kind:     "bold",
			span:     "Sami said bold",
		},
		{
			name:     "a find across an italic span's end",
			markdown: "*Sami said it* now.\n",
			find:     "it now",
			with:     "that then",
			kind:     "italic",
			span:     "Sami said it",
		},
		{
			name:     "a find across a struck span's end",
			markdown: "~~Sami said it~~ now.\n",
			find:     "it now",
			with:     "that then",
			kind:     "strikethrough",
			span:     "Sami said it",
		},
		{
			name:     "a find across a code span's end",
			markdown: "Run `make said all` now.\n",
			find:     "all now",
			with:     "test then",
			kind:     "code",
			span:     "make said all",
		},
		{
			name:     "a find that spells a bold span's markup over only some of the span's text it covers",
			markdown: "**Sami said bold** now.\n",
			find:     "**bo**ld now",
			with:     "loud then",
			kind:     "bold",
			span:     "Sami said bold",
		},
		{
			name:     "a find across a bold span's end that once wrote outside the span",
			markdown: "**Sami's** bold now.\n",
			find:     "'s bold",
			with:     "'s loud",
			kind:     "bold",
			span:     "Sami's",
		},
		{
			name:     "a find that spells one link's markup and covers a second link",
			markdown: "I wrote [this](https://a.example/1) and [that](https://b.example/2) today.\n",
			find:     "wrote [this](https://a.example/1) and that",
			with:     "wrote it",
			kind:     "link",
			span:     "that",
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
			name:     "a find that spells a link's markup with another URL",
			markdown: "I wrote [this](https://a.example/1) today.\n",
			find:     "wrote [this](https://wrong.example/9) today",
			with:     "wrote that today",
			kind:     "link",
			span:     "this",
			markup:   "[this](https://a.example/1)",
			says:     acrossAnotherLink,
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
			name:     "a find that is one link alone, spelled with another URL",
			markdown: "I wrote [this](https://a.example/1) today.\n",
			find:     "[this](https://wrong.example/9)",
			with:     "that",
			kind:     "link",
			span:     "this",
			markup:   "[this](https://a.example/1)",
			says:     "another URL or title",
			unsaid:   "text outside it",
		},
		{
			name:     "a find that is one link alone, spelled with another title",
			markdown: "I wrote [this](https://a.example/1 \"Spec\") today.\n",
			find:     "[this](https://a.example/1 \"Draft\")",
			with:     "that",
			kind:     "link",
			span:     "this",
			markup:   "[this](https://a.example/1 \"Spec\")",
			says:     "another URL or title",
			unsaid:   "text outside it",
		},
		{
			name:     "a find that is a bare link alone, spelled as a link to another URL",
			markdown: "Read https://a.example/1 today.\n",
			find:     "[https://a.example/1](https://b.example/2)",
			with:     "that",
			kind:     "link",
			span:     "https://a.example/1",
			markup:   "https://a.example/1",
			says:     `another URL or title than the one it has; write the link as it is in find ("https://a.example/1"), which writes with inside the link`,
			unsaid:   "text outside it",
		},
		{
			name:     "a find that spells a bare link as a link to another URL in a longer find",
			markdown: "Read https://a.example/1 today.\n",
			find:     "Read [https://a.example/1](https://b.example/2) today",
			with:     "Read that today",
			kind:     "link",
			span:     "https://a.example/1",
			markup:   "https://a.example/1",
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
		{
			name:     "a find inside a link that spells its text as another link",
			markdown: "I wrote [one two](https://a.example/1) today.\n",
			find:     "[one](https://b.example/2)",
			with:     "that",
			kind:     "link",
			span:     "one two",
			markup:   "[one two](https://a.example/1)",
			says: `spells the link "one two" with another URL or title than the one it has; to keep the text find names, ` +
				`quote it under the link's own URL and title ("[one](https://a.example/1)"), which then replaces the markup too, ` +
				`so with decides that formatting, or with no link markup ("one"), which writes with inside the link`,
			unsaid: "text outside it",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			before, _ := latestVersionOf(t, service.store, artifactID)
			_, err := joinedApplyOps(service, artifactID, []model.EditOp{
				{Op: "replace", Find: test.find, With: test.with},
			}, model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil)
			read, readErr := service.Text(context.Background(), artifactID)
			if readErr != nil {
				t.Fatalf("read the document: %v", readErr)
			}
			var crossed *ErrFindCrossesSpan
			if !errors.As(err, &crossed) || crossed.Kind() != test.kind || crossed.Span.Text != test.span || (test.markup != "" && crossed.Span.Markdown != test.markup) {
				t.Fatalf("replace %q with %q in %q = %v, want it refused for crossing the %s %q; the document reads %q",
					test.find, test.with, test.markdown, err, test.kind, test.span, read)
			}
			if message := err.Error(); (test.says != "" && !strings.Contains(message, test.says)) || (test.unsaid != "" && strings.Contains(message, test.unsaid)) {
				t.Fatalf("refusal = %q, want it to say %q and not %q", message, test.says, test.unsaid)
			}
			// A refused replace stores no version: the latest is the one before the seed, or the
			// seed's own if a settlement has stored it since.
			stored, _ := latestVersionOf(t, service.store, artifactID)
			if (stored != before && stored != test.markdown) || read != test.markdown {
				t.Fatalf("after the refused replace %q in %q:\n stored markdown %q, was %q\n read markdown   %q\nwant both unchanged",
					test.find, test.markdown, stored, before, read)
			}
		})
	}
}

// A replace's find that runs past a span's edge and spells the span's own markup over all of the
// span's text it covers is taken, whatever the span: a link, bold, italic or code each take the
// one rule (pmdoc.TextblockAt.CrossedSpan). The with is written over the text the find covers,
// outside the span, which keeps the rest of its text and its mark. A bold or italic span keeps it
// where the cut falls inside a word, at the span's end or its start, with no character reference;
// where the part it keeps would meet the with at a space, which markdown cannot write bold or
// italic delimiters beside, the find is refused (TestARefusedFindNamesTheFindsThatKeepItsRange),
// where it was written with character references (`**Sami said&#32;**&#78;EW`). A plain with
// leaves the part of the link the find covered unlinked, the rest of the link keeping its URL and
// title; a with that writes the same link, URL and title, joins the rest of the link: the live
// paragraph holds one text run under that link, not two beside each other. Each of those was
// refused, though the find spelled the span as the document holds it. The same find with another
// URL or title is still refused, and its refusal names the find with the link's own URL and title.
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
		{name: "a link, with plain text", markdown: plain, find: "wrote [one](https://a.example/1)", with: "NEW", want: "I NEW[ two](https://a.example/1) today.\n"},
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
		{name: "a bold span's end, cut inside a word", markdown: "**Sami said bold** now.\n", find: "**old** now", with: "NEW", want: "**Sami said b**NEW.\n"},
		{name: "a bold span's start, cut inside a word", markdown: "Then **Sami said bold**.\n", find: "Then **Sa**", with: "NEW", want: "NEW**mi said bold**.\n"},
		{name: "an italic span's end, cut inside a word", markdown: "*Sami said it* now.\n", find: "*t* now", with: "NEW", want: "*Sami said i*NEW.\n"},
		{name: "an italic span's start, cut inside a word", markdown: "Then *Sami said it*.\n", find: "Then *Sa*", with: "NEW", want: "NEW*mi said it*.\n"},
		{name: "a code span", markdown: "Run `make said all` now.\n", find: "`all` now", with: "NEW", want: "Run `make said `NEW.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			if _, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: test.find, With: test.with}}, actor, nil); err != nil {
				t.Fatalf("replace %q with %q in %q: %v, want it taken", test.find, test.with, test.markdown, err)
			}
			stored := settledMarkdown(t, service.store, artifactID, test.want)
			read, err := service.Text(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the document: %v", err)
			}
			if stored != test.want || read != test.want {
				t.Fatalf("replace %q with %q in %q:\n stored markdown %q\n read markdown   %q\nwant %q", test.find, test.with, test.markdown, stored, read, test.want)
			}
			// Two text runs beside each other under the same marks would be two spans that the
			// markdown writes as one; the live paragraph holds one run where the marks join.
			paragraph := liveTree(t, service, artifactID).Children[0]
			for index := 1; index < len(paragraph.Children); index++ {
				if left, right := paragraph.Children[index-1], paragraph.Children[index]; left.Type == "text" && right.Type == "text" && reflect.DeepEqual(left.Marks, right.Marks) {
					t.Fatalf("replace %q with %q in %q leaves the runs %q and %q beside each other under the marks %v", test.find, test.with, test.markdown, left.Text, right.Text, left.Marks)
				}
			}
		})
	}
	for _, test := range []struct {
		name, markdown, find string
		// refusal is the refusal after the operation's index.
		refusal string
	}{
		{
			name: "a link spelled with another URL", markdown: plain, find: "wrote [one](https://b.example/2)",
			refusal: `find "wrote [one](https://b.example/2)" crosses the edge of the link "one two": it covers text inside the link and text outside it, ` +
				`and it spells the link with another URL or title than the one it has; to keep the text find names, ` +
				`write the part of the link it covers under the link's own URL and title ("wrote [one](https://a.example/1)"), ` +
				`which then replaces the markup too, so with decides that formatting`,
		},
		{
			name: "a titled link spelled with another title", markdown: titled, find: `wrote [one](https://a.example/1 "Draft")`,
			refusal: `find "wrote [one](https://a.example/1 \"Draft\")" crosses the edge of the link "one two": it covers text inside the link and text outside it, ` +
				`and it spells the link with another URL or title than the one it has; to keep the text find names, ` +
				`write the part of the link it covers under the link's own URL and title ("wrote [one](https://a.example/1 \"Spec\")"), ` +
				`which then replaces the markup too, so with decides that formatting`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			before, _ := latestVersionOf(t, service.store, artifactID)
			_, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: test.find, With: "NEW"}}, actor, nil)
			read, readErr := service.Text(context.Background(), artifactID)
			if readErr != nil {
				t.Fatalf("read the document: %v", readErr)
			}
			var crossed *ErrFindCrossesSpan
			if want := "operation 0: " + test.refusal; !errors.As(err, &crossed) || err.Error() != want {
				t.Fatalf("replace %q in %q =\n%v\nwant the refusal\n%s", test.find, test.markdown, err, want)
			}
			// A refused edit stores no version: the latest is the one before the seed, or the
			// seed's own if a settlement has stored it since.
			if stored, _ := latestVersionOf(t, service.store, artifactID); (stored != before && stored != test.markdown) || read != test.markdown {
				t.Fatalf("after the refused replace %q:\n stored markdown %q, was %q\n read markdown   %q\nwant both unchanged", test.find, stored, before, read)
			}
		})
	}
}

// A find names text to change, so a delete's spells a link as a replace's does
// (TestReplaceAcrossAMarkedSpansEdgeIsRefused): one that spells a link with another URL or
// another title, as a find copied from an older read after another writer changed the link may,
// is refused with ErrFindCrossesSpan, and the document is left as it was, where the delete
// removed that text with no word of the change. The refusal names finds that delete the very text
// the find named: for the link alone, the link as it is; for a longer find, that find with the
// link as it is, where it gave the whole link and then the find narrowed to the link's inside,
// which deletes less than it named; for a find inside the link, that text under the link's own
// URL and title, or with no link markup; and for a find over part of the link and the text before
// it, that find with the part under the link's own URL and title, or with no link markup, where it
// gave the whole link, which deletes text the find did not name. Each spelling of the link as the
// document holds it is taken, and deletes the text it named; so is each find a refusal names
// (TestARefusedFindNamesTheFindsThatKeepItsRange).
func TestADeleteOfAFindThatSpellsALinkIsRefusedOrDeletesWhatItNamed(t *testing.T) {
	const markdown = "I wrote [this one](https://a.example/1 \"Spec\") today.\n"
	for _, spelling := range []struct {
		name, quote string
		// crossing is why the delete's find of quote crosses the link, and want is the refusal
		// after the operation's index, or, for a quote that crosses no span, the document the
		// delete leaves.
		crossing pmdoc.Crossing
		want     string
	}{
		{
			name: "the link alone, spelled with another URL", quote: `[this one](https://b.example/2 "Spec")`, crossing: pmdoc.SpellsAnotherLinkAlone,
			want: `find "[this one](https://b.example/2 \"Spec\")" spells the link "this one" with another URL or title than the one it has; ` +
				`write the link as it is in find ("[this one](https://a.example/1 \"Spec\")")`,
		},
		{
			name: "the link alone, spelled with another title", quote: `[this one](https://a.example/1 "Draft")`, crossing: pmdoc.SpellsAnotherLinkAlone,
			want: `find "[this one](https://a.example/1 \"Draft\")" spells the link "this one" with another URL or title than the one it has; ` +
				`write the link as it is in find ("[this one](https://a.example/1 \"Spec\")")`,
		},
		{
			name: "a longer quote spelling the link with another URL", quote: `wrote [this one](https://b.example/2 "Spec") today`, crossing: pmdoc.SpellsAnotherLink,
			want: `find "wrote [this one](https://b.example/2 \"Spec\") today" crosses the edge of the link "this one": it covers text inside the link ` +
				`and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`write the whole link as it is in find ("wrote [this one](https://a.example/1 \"Spec\") today")`,
		},
		{
			name: "a longer quote spelling the link with another title", quote: `wrote [this one](https://a.example/1 "Draft") today`, crossing: pmdoc.SpellsAnotherLink,
			want: `find "wrote [this one](https://a.example/1 \"Draft\") today" crosses the edge of the link "this one": it covers text inside the link ` +
				`and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`write the whole link as it is in find ("wrote [this one](https://a.example/1 \"Spec\") today")`,
		},
		{
			name: "a quote inside the link spelling its text as another link", quote: `[this](https://b.example/2 "Spec")`, crossing: pmdoc.SpellsAnotherLinkInside,
			want: `find "[this](https://b.example/2 \"Spec\")" spells the link "this one" with another URL or title than the one it has; ` +
				`to keep the text find names, quote it under the link's own URL and title ("[this](https://a.example/1 \"Spec\")"), ` +
				`or with no link markup ("this")`,
		},
		{
			name: "a quote over part of the link and the text before it, spelling that part as another link", quote: `wrote [this](https://b.example/2 "Spec")`,
			crossing: pmdoc.SpellsAnotherLinkAcrossEdge,
			want: `find "wrote [this](https://b.example/2 \"Spec\")" crosses the edge of the link "this one": it covers text inside the link ` +
				`and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`to keep the text find names, write the part of the link it covers under the link's own URL and title ` +
				`("wrote [this](https://a.example/1 \"Spec\")"), or with no link markup ("wrote this")`,
		},
		{name: "the link alone, spelled as it is", quote: `[this one](https://a.example/1 "Spec")`, want: "I wrote  today.\n"},
		{name: "a longer quote spelling the link as it is", quote: `wrote [this one](https://a.example/1 "Spec") today`, want: "I .\n"},
		{name: "a quote inside the link under the link's own URL and title", quote: `[this](https://a.example/1 "Spec")`, want: "I wrote [ one](https://a.example/1 \"Spec\") today.\n"},
		{name: "a quote inside the link with no link markup", quote: "this", want: "I wrote [ one](https://a.example/1 \"Spec\") today.\n"},
		{
			name: "a quote over part of the link and the text before it, under the link's own URL and title", quote: `wrote [this](https://a.example/1 "Spec")`,
			want: "I [ one](https://a.example/1 \"Spec\") today.\n",
		},
		{name: "a quote over part of the link and the text before it, with no link markup", quote: "wrote this", want: "I [ one](https://a.example/1 \"Spec\") today.\n"},
	} {
		t.Run(spelling.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, markdown)
			before, _ := latestVersionOf(t, service.store, artifactID)
			_, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "delete", Find: spelling.quote}},
				model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil)
			if spelling.crossing == pmdoc.CrossesNoSpan {
				if err != nil {
					t.Fatalf("delete %q in %q: %v, want it taken", spelling.quote, markdown, err)
				}
				stored := settledMarkdown(t, service.store, artifactID, spelling.want)
				read, err := service.Text(context.Background(), artifactID)
				if err != nil {
					t.Fatalf("read the document: %v", err)
				}
				if stored != spelling.want || read != spelling.want {
					t.Fatalf("delete %q in %q:\n stored markdown %q\n read markdown   %q\nwant %q", spelling.quote, markdown, stored, read, spelling.want)
				}
				return
			}
			read, readErr := service.Text(context.Background(), artifactID)
			if readErr != nil {
				t.Fatalf("read the document: %v", readErr)
			}
			var crossed *ErrFindCrossesSpan
			if !errors.As(err, &crossed) || crossed.Kind() != "link" || crossed.Span.Text != "this one" ||
				crossed.Span.Markdown != `[this one](https://a.example/1 "Spec")` || crossed.Crossing != spelling.crossing {
				t.Fatalf("delete %q in %q = %v, want it refused naming the link; the document reads %q", spelling.quote, markdown, err, read)
			}
			if want := "operation 0: " + spelling.want; err.Error() != want {
				t.Fatalf("refusal =\n%s\nwant\n%s", err, want)
			}
			// A refused edit stores no version: the latest is the one before the seed, or the
			// seed's own if a settlement has stored it since.
			stored, _ := latestVersionOf(t, service.store, artifactID)
			if (stored != before && stored != markdown) || read != markdown {
				t.Fatalf("after the refused delete %q:\n stored markdown %q, was %q\n read markdown   %q\nwant both unchanged",
					spelling.quote, stored, before, read)
			}
		})
	}
}

// An insert's or a move's anchor names a place, not text to change, so it finds its block whatever
// it spells a link with: each spelling of the link, as the document holds it or with another URL
// or title, alone, inside the link, over part of it and the text before it, or in a longer quote,
// is taken, the insert and the moved block land after the paragraph, and the link stays as the
// document holds it, where each anchor spelling it otherwise was refused.
func TestAnAnchorFindsItsBlockWhateverItSpellsALinkWith(t *testing.T) {
	const markdown = "Moved.\n\nI wrote [this one](https://a.example/1 \"Spec\") today.\n"
	spellings := []struct{ name, quote string }{
		{name: "the link alone, spelled with another URL", quote: `[this one](https://b.example/2 "Spec")`},
		{name: "the link alone, spelled with another title", quote: `[this one](https://a.example/1 "Draft")`},
		{name: "a longer quote spelling the link with another URL", quote: `wrote [this one](https://b.example/2 "Spec") today`},
		{name: "a longer quote spelling the link with another title", quote: `wrote [this one](https://a.example/1 "Draft") today`},
		{name: "a quote inside the link spelling its text as another link", quote: `[this](https://b.example/2 "Spec")`},
		{name: "a quote over part of the link and the text before it, spelling that part as another link", quote: `wrote [this](https://b.example/2 "Spec")`},
		{name: "the link alone, spelled as it is", quote: `[this one](https://a.example/1 "Spec")`},
		{name: "a longer quote spelling the link as it is", quote: `wrote [this one](https://a.example/1 "Spec") today`},
		{name: "a quote inside the link under the link's own URL and title", quote: `[this](https://a.example/1 "Spec")`},
		{name: "a quote inside the link with no link markup", quote: "this"},
	}
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
			landed: "I wrote [this one](https://a.example/1 \"Spec\") today.\n\nMoved.\n",
		},
	} {
		for _, spelling := range spellings {
			t.Run(op.op+" after "+spelling.name, func(t *testing.T) {
				service, artifactID := newTestService(t)
				seedServiceText(t, service, artifactID, markdown)
				moved := blockIDOfText(t, liveTree(t, service, artifactID), "Moved.")
				if _, err := joinedApplyOps(service, artifactID, []model.EditOp{op.edit(spelling.quote, moved)},
					model.Actor{Kind: "session", ID: "session-0123456789abcdef"}, nil); err != nil {
					t.Fatalf("%s after %q in %q: %v, want it taken", op.op, spelling.quote, markdown, err)
				}
				stored := settledMarkdown(t, service.store, artifactID, op.landed)
				read, err := service.Text(context.Background(), artifactID)
				if err != nil {
					t.Fatalf("read the document: %v", err)
				}
				if stored != op.landed || read != op.landed {
					t.Fatalf("%s after %q in %q:\n stored markdown %q\n read markdown   %q\nwant %q",
						op.op, spelling.quote, markdown, stored, read, op.landed)
				}
			})
		}
	}
}

// A refused find's refusal names the finds that cover the very text the find named and are taken
// there, which the caller sends in its place, each with the occurrence at which it matches that
// text. The table is each shape of a find that spells a link with another URL or title - the whole
// link; part of it; the whole link with text before or after it; part of it with text before or
// after it; a bare link, alone and in a longer find - by a delete and a replace (with `NEW`), over
// a document holding the link, with a title where a spelling names one; then each shape of a
// replace's find across a span's edge: a link's, a code span's, a struck span's, and a bold or an
// italic span's end and start, cut inside a word or at a space, the find spelling the span's
// markup over the part it covers or none. Each row reads the refusal's whole text, then sends the
// operation again with each find the refusal gives names (each `("…")` in it), with the occurrence
// it names after that find, and reads the document that leaves: exactly the text the refused find
// named is deleted or replaced, and the rest is as it was, the link keeping its URL and title
// where that text does not cover all of it. A find is named only where one of its matches is that
// text and it is taken there. Where none is, the refusal says so and names none: to move the space
// out of a bold or italic span where the part the find leaves would meet the written text at a
// space, which markdown cannot write the span's delimiters beside; for a struck span, to replace
// inside it and outside it in two operations; otherwise to read the document again. Before, a find
// over part of a link and text outside it got the whole link's fix, which deleted `wrote one two`
// where it named `wrote one`, and the find narrowed to the link's inside, which deleted less; a
// find over the whole link and text outside it got that narrowed find too; a find inside a link
// got its text with no link markup first, which the matcher of text as written found inside
// `Someone`, and, with the occurrence the find had, in another link; one over part of a link
// holding a bold run got that part without the bold, which a replace refused for crossing the bold
// span's edge; a replace's find over part of a link and text outside it was refused however it
// spelled the link; a find matching the text at another occurrence than the refused find did was
// not named; a replace's find across another span's edge got the find narrowed to the span's
// inside or widened to the whole span, neither of which names the same text; and one that spelled
// a bold span's markup over the part it covers, cut at a space, was taken and written with
// character references (`**Sami said&#32;**&#78;EW`). The rows with a document of their own show
// those cases: the text stands inside another word before the link, or bare before it, or in a
// second link, or in a link before it with no bold run; the link holds a bold run; or the find runs
// into a struck span, which no find written from the document can replace across.
func TestARefusedFindNamesTheFindsThatKeepItsRange(t *testing.T) {
	const (
		plain   = "I wrote [one two](https://a.example/1) today.\n"
		titled  = "I wrote [one two](https://a.example/1 \"Spec\") today.\n"
		bare    = "Read https://a.example/1 today.\n"
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
		// refusal is the refusal's whole text after the operation's index, and leaves the document
		// each find it names leaves, in the order it names them.
		refusal string
		leaves  []string
	}{
		{
			shape: "the whole link", name: "spelled with another URL", markdown: plain, op: "delete", find: "[one two](https://b.example/2)",
			refusal: `find "[one two](https://b.example/2)" ` + another + `write the link as it is in find ("[one two](https://a.example/1)")`,
			leaves:  []string{"I wrote  today.\n"},
		},
		{
			shape: "the whole link", name: "spelled with another URL", markdown: plain, op: "replace", find: "[one two](https://b.example/2)",
			refusal: `find "[one two](https://b.example/2)" ` + another + `write the link as it is in find ("[one two](https://a.example/1)")` + replaces,
			leaves:  []string{"I wrote NEW today.\n"},
		},
		{
			shape: "the whole link", name: "spelled with another title", markdown: titled, op: "delete", find: `[one two](https://a.example/1 "Draft")`,
			refusal: `find "[one two](https://a.example/1 \"Draft\")" ` + another + `write the link as it is in find ("[one two](https://a.example/1 \"Spec\")")`,
			leaves:  []string{"I wrote  today.\n"},
		},
		{
			shape: "the whole link", name: "spelled with another title", markdown: titled, op: "replace", find: `[one two](https://a.example/1 "Draft")`,
			refusal: `find "[one two](https://a.example/1 \"Draft\")" ` + another + `write the link as it is in find ("[one two](https://a.example/1 \"Spec\")")` + replaces,
			leaves:  []string{"I wrote NEW today.\n"},
		},
		{
			shape: "part of the link", name: "spelled with another URL", markdown: plain, op: "delete", find: "[one](https://b.example/2)",
			refusal: `find "[one](https://b.example/2)" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[one](https://a.example/1)"), or with no link markup ("one")`,
			leaves: []string{"I wrote [ two](https://a.example/1) today.\n", "I wrote [ two](https://a.example/1) today.\n"},
		},
		{
			shape: "part of the link", name: "spelled with another URL", markdown: plain, op: "replace", find: "[one](https://b.example/2)",
			refusal: `find "[one](https://b.example/2)" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[one](https://a.example/1)")` + replaces +
				`, or with no link markup ("one")` + inside,
			leaves: []string{"I wrote NEW[ two](https://a.example/1) today.\n", "I wrote [NEW two](https://a.example/1) today.\n"},
		},
		{
			shape: "part of the link", name: "spelled with another title", markdown: titled, op: "delete", find: `[two](https://a.example/1 "Draft")`,
			refusal: `find "[two](https://a.example/1 \"Draft\")" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[two](https://a.example/1 \"Spec\")"), or with no link markup ("two")`,
			leaves: []string{"I wrote [one ](https://a.example/1 \"Spec\") today.\n", "I wrote [one ](https://a.example/1 \"Spec\") today.\n"},
		},
		{
			shape: "part of the link", name: "spelled with another title", markdown: titled, op: "replace", find: `[two](https://a.example/1 "Draft")`,
			refusal: `find "[two](https://a.example/1 \"Draft\")" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[two](https://a.example/1 \"Spec\")")` + replaces +
				`, or with no link markup ("two")` + inside,
			leaves: []string{"I wrote [one ](https://a.example/1 \"Spec\")NEW today.\n", "I wrote [one NEW](https://a.example/1 \"Spec\") today.\n"},
		},
		{
			// The text with no link markup matches `one` inside `Someone` first, so it is named
			// with occurrence 1, where the link's is its first.
			shape: "part of the link", name: "whose text stands inside a word before the link", markdown: "Someone wrote [one two](https://a.example/1) today.\n",
			op: "delete", find: "[one](https://b.example/2)", occurrence: occurrence(0),
			refusal: `find "[one](https://b.example/2)" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[one](https://a.example/1)") with occurrence 0, ` +
				`or with no link markup ("one") with occurrence 1`,
			leaves: []string{"Someone wrote [ two](https://a.example/1) today.\n", "Someone wrote [ two](https://a.example/1) today.\n"},
		},
		{
			shape: "part of the link", name: "whose text stands inside a word before the link", markdown: "Someone wrote [one two](https://a.example/1) today.\n",
			op: "replace", find: "[one](https://b.example/2)", occurrence: occurrence(0),
			refusal: `find "[one](https://b.example/2)" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[one](https://a.example/1)") with occurrence 0` + replaces +
				`, or with no link markup ("one") with occurrence 1` + inside,
			leaves: []string{"Someone wrote NEW[ two](https://a.example/1) today.\n", "Someone wrote [NEW two](https://a.example/1) today.\n"},
		},
		{
			shape: "part of the link", name: "in the second link holding its text", markdown: "I wrote [one](https://a.example/1) and [one two](https://a.example/1) today.\n",
			op: "delete", find: "[one](https://b.example/2)", occurrence: occurrence(1),
			refusal: `find "[one](https://b.example/2)" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[one](https://a.example/1)") with occurrence 1, ` +
				`or with no link markup ("one") with occurrence 1`,
			leaves: []string{
				"I wrote [one](https://a.example/1) and [ two](https://a.example/1) today.\n",
				"I wrote [one](https://a.example/1) and [ two](https://a.example/1) today.\n",
			},
		},
		{
			shape: "part of the link", name: "in the second link holding its text", markdown: "I wrote [one](https://a.example/1) and [one two](https://a.example/1) today.\n",
			op: "replace", find: "[one](https://b.example/2)", occurrence: occurrence(1),
			refusal: `find "[one](https://b.example/2)" ` + another +
				`to keep the text find names, quote it under the link's own URL and title ("[one](https://a.example/1)") with occurrence 1` + replaces +
				`, or with no link markup ("one") with occurrence 1` + inside,
			leaves: []string{
				"I wrote [one](https://a.example/1) and NEW[ two](https://a.example/1) today.\n",
				"I wrote [one](https://a.example/1) and [NEW two](https://a.example/1) today.\n",
			},
		},
		{
			shape: "part of the link", name: "holding a bold run", markdown: "I wrote [one **two** three](https://a.example/1) today.\n",
			op: "delete", find: "[one **two**](https://b.example/2)",
			refusal: `find "[one **two**](https://b.example/2)" spells the link "one two three" with another URL or title than the one it has; ` +
				`to keep the text find names, quote it under the link's own URL and title ("[one **two**](https://a.example/1)"), or with no link markup ("one **two**")`,
			leaves: []string{"I wrote [ three](https://a.example/1) today.\n", "I wrote [ three](https://a.example/1) today.\n"},
		},
		{
			shape: "part of the link", name: "holding a bold run", markdown: "I wrote [one **two** three](https://a.example/1) today.\n",
			op: "replace", find: "[one **two**](https://b.example/2)",
			refusal: `find "[one **two**](https://b.example/2)" spells the link "one two three" with another URL or title than the one it has; ` +
				`to keep the text find names, quote it under the link's own URL and title ("[one **two**](https://a.example/1)")` + replaces +
				`, or with no link markup ("one **two**")` + inside,
			leaves: []string{"I wrote NEW[ three](https://a.example/1) today.\n", "I wrote [NEW three](https://a.example/1) today.\n"},
		},
		{
			// The find's occurrence counts each place `one two` stands under a link, the first
			// link's whole text and then this one; a find spelling the bold run counts only this
			// one, so each is named with occurrence 0, where it matches this text.
			shape: "part of the link", name: "whose text stands before it under a link with no bold run",
			markdown: "I wrote [one two](https://a.example/1) and [one **two** three](https://a.example/1) today.\n",
			op:       "delete", find: "[one two](https://b.example/2)", occurrence: occurrence(1),
			refusal: `find "[one two](https://b.example/2)" spells the link "one two three" with another URL or title than the one it has; ` +
				`to keep the text find names, quote it under the link's own URL and title ("[one **two**](https://a.example/1)") with occurrence 0, ` +
				`or with no link markup ("one **two**") with occurrence 0`,
			leaves: []string{
				"I wrote [one two](https://a.example/1) and [ three](https://a.example/1) today.\n",
				"I wrote [one two](https://a.example/1) and [ three](https://a.example/1) today.\n",
			},
		},
		{
			shape: "the whole link and text", name: "before and after it", markdown: titled, op: "delete", find: `wrote [one two](https://b.example/2 "Spec") today`,
			refusal: `find "wrote [one two](https://b.example/2 \"Spec\") today" ` + crosses +
				`write the whole link as it is in find ("wrote [one two](https://a.example/1 \"Spec\") today")`,
			leaves: []string{"I .\n"},
		},
		{
			shape: "the whole link and text", name: "before and after it", markdown: titled, op: "replace", find: `wrote [one two](https://b.example/2 "Spec") today`,
			refusal: `find "wrote [one two](https://b.example/2 \"Spec\") today" ` + crosses +
				`write the whole link as it is in find ("wrote [one two](https://a.example/1 \"Spec\") today")` + replaces,
			leaves: []string{"I NEW.\n"},
		},
		{
			shape: "the whole link and text", name: "after it", markdown: plain, op: "delete", find: "[one two](https://b.example/2) today",
			refusal: `find "[one two](https://b.example/2) today" ` + crosses + `write the whole link as it is in find ("[one two](https://a.example/1) today")`,
			leaves:  []string{"I wrote .\n"},
		},
		{
			shape: "the whole link and text", name: "after it", markdown: plain, op: "replace", find: "[one two](https://b.example/2) today",
			refusal: `find "[one two](https://b.example/2) today" ` + crosses + `write the whole link as it is in find ("[one two](https://a.example/1) today")` + replaces,
			leaves:  []string{"I wrote NEW.\n"},
		},
		{
			shape: "part of the link and text", name: "before it", markdown: plain, op: "delete", find: "wrote [one](https://b.example/2)",
			refusal: `find "wrote [one](https://b.example/2)" ` + crosses +
				`to keep the text find names, write the part of the link it covers under the link's own URL and title ("wrote [one](https://a.example/1)"), ` +
				`or with no link markup ("wrote one")`,
			leaves: []string{"I [ two](https://a.example/1) today.\n", "I [ two](https://a.example/1) today.\n"},
		},
		{
			// A replace's find with no link markup that covers part of the link and text outside
			// it crosses the link's edge, so the refusal does not name it.
			shape: "part of the link and text", name: "before it", markdown: plain, op: "replace", find: "wrote [one](https://b.example/2)",
			refusal: `find "wrote [one](https://b.example/2)" ` + crosses +
				`to keep the text find names, write the part of the link it covers under the link's own URL and title ("wrote [one](https://a.example/1)")` + replaces,
			leaves: []string{"I NEW[ two](https://a.example/1) today.\n"},
		},
		{
			shape: "part of the link and text", name: "after it", markdown: titled, op: "delete", find: `[two](https://b.example/2 "Spec") today`,
			refusal: `find "[two](https://b.example/2 \"Spec\") today" ` + crosses +
				`to keep the text find names, write the part of the link it covers under the link's own URL and title ("[two](https://a.example/1 \"Spec\") today"), ` +
				`or with no link markup ("two today")`,
			leaves: []string{"I wrote [one ](https://a.example/1 \"Spec\").\n", "I wrote [one ](https://a.example/1 \"Spec\").\n"},
		},
		{
			shape: "part of the link and text", name: "after it", markdown: titled, op: "replace", find: `[two](https://b.example/2 "Spec") today`,
			refusal: `find "[two](https://b.example/2 \"Spec\") today" ` + crosses +
				`to keep the text find names, write the part of the link it covers under the link's own URL and title ("[two](https://a.example/1 \"Spec\") today")` + replaces,
			leaves: []string{"I wrote [one ](https://a.example/1 \"Spec\")NEW.\n"},
		},
		{
			// The text with no link markup matches `wrote one` before the link first, so it is
			// named with occurrence 1.
			shape: "part of the link and text", name: "before it, which stands bare before the link too", markdown: "I wrote one, then I wrote [one two](https://a.example/1) today.\n",
			op: "delete", find: "wrote [one](https://b.example/2)",
			refusal: `find "wrote [one](https://b.example/2)" ` + crosses +
				`to keep the text find names, write the part of the link it covers under the link's own URL and title ("wrote [one](https://a.example/1)"), ` +
				`or with no link markup ("wrote one") with occurrence 1`,
			leaves: []string{"I wrote one, then I [ two](https://a.example/1) today.\n", "I wrote one, then I [ two](https://a.example/1) today.\n"},
		},
		{
			// The find runs from the link into a struck span, whose markup no find spells, so a
			// replace's find over that text crosses the struck span's edge however it is written.
			shape: "part of the link and text", name: "after it, which a struck span holds", markdown: "I wrote [one two](https://a.example/1) ~~three four~~ today.\n",
			op: "replace", find: "[two](https://b.example/2) three",
			refusal: `find "[two](https://b.example/2) three" ` + crosses +
				`no find written from the document's text covers the text find names and is taken there: read the document again and quote what it holds`,
		},
		{
			shape: "a bare link", name: "alone", markdown: bare, op: "delete", find: "[https://a.example/1](https://b.example/2)",
			refusal: `find "[https://a.example/1](https://b.example/2)" spells the link "https://a.example/1" with another URL or title than the one it has; ` +
				`write the link as it is in find ("https://a.example/1")`,
			leaves: []string{"Read  today.\n"},
		},
		{
			shape: "a bare link", name: "alone", markdown: bare, op: "replace", find: "[https://a.example/1](https://b.example/2)",
			refusal: `find "[https://a.example/1](https://b.example/2)" spells the link "https://a.example/1" with another URL or title than the one it has; ` +
				`write the link as it is in find ("https://a.example/1")` + inside,
			leaves: []string{"Read [NEW](https://a.example/1) today.\n"},
		},
		{
			shape: "a bare link", name: "in a longer find", markdown: bare, op: "delete", find: "Read [https://a.example/1](https://b.example/2) today",
			refusal: `find "Read [https://a.example/1](https://b.example/2) today" crosses the edge of the link "https://a.example/1": ` +
				`it covers text inside the link and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`write the whole link as it is in find ("Read https://a.example/1 today")`,
			leaves: []string{".\n"},
		},
		{
			shape: "a bare link", name: "in a longer find", markdown: bare, op: "replace", find: "Read [https://a.example/1](https://b.example/2) today",
			refusal: `find "Read [https://a.example/1](https://b.example/2) today" crosses the edge of the link "https://a.example/1": ` +
				`it covers text inside the link and text outside it, and it spells the link with another URL or title than the one it has; ` +
				`write the whole link as it is in find ("Read https://a.example/1 today")` + replaces,
			leaves: []string{"NEW.\n"},
		},
		{
			shape: "a link's edge", name: "the whole link and text before it", markdown: "Sami wrote [this](https://example.com/Sami) today.\n",
			op: "replace", find: "wrote this",
			refusal: `find "wrote this" crosses the edge of the link "this": it covers text inside the link and text outside it, and ` +
				`text written over both would not keep the link whole; to keep the text find names, ` +
				`spell the link's markup over the part of it find covers ("wrote [this](https://example.com/Sami)")` + replaces,
			leaves: []string{"Sami NEW today.\n"},
		},
		{
			shape: "a link's edge", name: "part of the link and text before it", markdown: plain, op: "replace", find: "wrote one",
			refusal: `find "wrote one" crosses the edge of the link "one two": it covers text inside the link and text outside it, and ` +
				`text written over both would not keep the link whole; to keep the text find names, ` +
				`spell the link's markup over the part of it find covers ("wrote [one](https://a.example/1)")` + replaces,
			leaves: []string{"I NEW[ two](https://a.example/1) today.\n"},
		},
		{
			shape: "a link's edge", name: "a second link, past one spelled", markdown: "I wrote [this](https://a.example/1) and [that](https://b.example/2) today.\n",
			op: "replace", find: "wrote [this](https://a.example/1) and that",
			refusal: `find "wrote [this](https://a.example/1) and that" crosses the edge of the link "that": it covers text inside the link and text outside it, and ` +
				`text written over both would not keep the link whole; to keep the text find names, ` +
				`spell the link's markup over the part of it find covers ("wrote [this](https://a.example/1) and [that](https://b.example/2)")` + replaces,
			leaves: []string{"I NEW today.\n"},
		},
		{
			shape: "a code span's end", name: "with text after it", markdown: "Run `make said all` now.\n", op: "replace", find: "all now",
			refusal: `find "all now" crosses the edge of the code span "make said all": it covers text inside the code span and text outside it, and ` +
				"text written over both would not keep the code span whole; to keep the text find names, " +
				"spell the code span's markup over the part of it find covers (\"`all` now\")" + replaces,
			leaves: []string{"Run `make said `NEW.\n"},
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
				`"Sami said ", would meet the written text at a space, where markdown cannot write the bold span's delimiters; move that space out of the bold span`,
		},
		{
			shape: "a bold span's end", name: "cut inside a word", markdown: "**Sami said bold** now.\n", op: "replace", find: "old now",
			refusal: `find "old now" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; to keep the text find names, ` +
				`spell the bold span's markup over the part of it find covers ("**old** now")` + replaces,
			leaves: []string{"**Sami said b**NEW.\n"},
		},
		{
			shape: "a bold span's start", name: "cut before a space", markdown: "Then **Sami said bold**.\n", op: "replace", find: "Then Sami",
			refusal: `find "Then Sami" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; no find keeps the text find names, since the part of the bold span it leaves, ` +
				`" said bold", would meet the written text at a space, where markdown cannot write the bold span's delimiters; move that space out of the bold span`,
		},
		{
			shape: "a bold span's start", name: "cut inside a word", markdown: "Then **Sami said bold**.\n", op: "replace", find: "Then Sa",
			refusal: `find "Then Sa" crosses the edge of the bold span "Sami said bold": it covers text inside the bold span and text outside it, and ` +
				`text written over both would not keep the bold span whole; to keep the text find names, ` +
				`spell the bold span's markup over the part of it find covers ("Then **Sa**")` + replaces,
			leaves: []string{"NEW**mi said bold**.\n"},
		},
		{
			shape: "an italic span's end", name: "cut after a space", markdown: "*Sami said it* now.\n", op: "replace", find: "it now",
			refusal: `find "it now" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; no find keeps the text find names, since the part of the italic span it leaves, ` +
				`"Sami said ", would meet the written text at a space, where markdown cannot write the italic span's delimiters; move that space out of the italic span`,
		},
		{
			shape: "an italic span's end", name: "cut inside a word", markdown: "*Sami said it* now.\n", op: "replace", find: "t now",
			refusal: `find "t now" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; to keep the text find names, ` +
				`spell the italic span's markup over the part of it find covers ("*t* now")` + replaces,
			leaves: []string{"*Sami said i*NEW.\n"},
		},
		{
			shape: "an italic span's start", name: "cut before a space", markdown: "Then *Sami said it*.\n", op: "replace", find: "Then Sami",
			refusal: `find "Then Sami" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; no find keeps the text find names, since the part of the italic span it leaves, ` +
				`" said it", would meet the written text at a space, where markdown cannot write the italic span's delimiters; move that space out of the italic span`,
		},
		{
			shape: "an italic span's start", name: "cut inside a word", markdown: "Then *Sami said it*.\n", op: "replace", find: "Then Sa",
			refusal: `find "Then Sa" crosses the edge of the italic span "Sami said it": it covers text inside the italic span and text outside it, and ` +
				`text written over both would not keep the italic span whole; to keep the text find names, ` +
				`spell the italic span's markup over the part of it find covers ("Then *Sa*")` + replaces,
			leaves: []string{"NEW*mi said it*.\n"},
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
			edit := func(find string, occurrence *int) []model.EditOp {
				if test.op == "delete" {
					return []model.EditOp{{Op: "delete", Find: find, Occurrence: occurrence}}
				}
				return []model.EditOp{{Op: "replace", Find: find, With: "NEW", Occurrence: occurrence}}
			}
			actor := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, test.markdown)
			before, _ := latestVersionOf(t, service.store, artifactID)
			_, err := joinedApplyOps(service, artifactID, edit(test.find, test.occurrence), actor, nil)
			read, readErr := service.Text(context.Background(), artifactID)
			if readErr != nil {
				t.Fatalf("read the document: %v", readErr)
			}
			var crossed *ErrFindCrossesSpan
			if !errors.As(err, &crossed) {
				t.Fatalf("%s %q in %q = %v, want it refused with the refusal\n%s", test.op, test.find, test.markdown, err, test.refusal)
			}
			// A refused edit stores no version: the latest is the one before the seed, or the
			// seed's own if a settlement has stored it since.
			if stored, _ := latestVersionOf(t, service.store, artifactID); (stored != before && stored != test.markdown) || read != test.markdown {
				t.Fatalf("after the refused %s %q:\n stored markdown %q, was %q\n read markdown   %q\nwant both unchanged",
					test.op, test.find, stored, before, read)
			}
			refusal := strings.TrimPrefix(err.Error(), "operation 0: ")
			if refusal != test.refusal {
				t.Errorf("%s %q in %q: refusal\n%s\nwant\n%s", test.op, test.find, test.markdown, refusal, test.refusal)
			}
			// Each find the refusal given names, in parentheses as Go quotes it, is sent in the
			// find's place, with the occurrence the refusal names after it, so a find it names that
			// does not keep the range fails here whatever text the row wants.
			named := regexp.MustCompile(`\("((?:[^"\\]|\\.)*)"\)(?: with occurrence (\d+))?`).FindAllStringSubmatch(refusal, -1)
			if len(named) != len(test.leaves) {
				t.Errorf("the refusal names %d finds, want %d: %s", len(named), len(test.leaves), refusal)
			}
			for index, quoted := range named {
				find, err := strconv.Unquote(`"` + quoted[1] + `"`)
				if err != nil {
					t.Fatalf("unquote %s: %v", quoted[0], err)
				}
				var occurrence *int
				if quoted[2] != "" {
					at, _ := strconv.Atoi(quoted[2])
					occurrence = &at
				}
				t.Run(find, func(t *testing.T) {
					service, artifactID := newTestService(t)
					seedServiceText(t, service, artifactID, test.markdown)
					if _, err := joinedApplyOps(service, artifactID, edit(find, occurrence), actor, nil); err != nil {
						t.Fatalf("%s %q, which the refusal of %q names, in %q: %v, want it taken", test.op, find, test.find, test.markdown, err)
					}
					if index >= len(test.leaves) {
						t.Fatalf("%s %q, which the refusal of %q names and the row does not, in %q leaves %q",
							test.op, find, test.find, test.markdown, settledMarkdown(t, service.store, artifactID, ""))
					}
					stored := settledMarkdown(t, service.store, artifactID, test.leaves[index])
					read, err := service.Text(context.Background(), artifactID)
					if err != nil {
						t.Fatalf("read the document: %v", err)
					}
					if want := test.leaves[index]; stored != want || read != want {
						t.Fatalf("%s %q, which the refusal of %q names, in %q:\n stored markdown %q\n read markdown   %q\nwant %q",
							test.op, find, test.find, test.markdown, stored, read, want)
					}
				})
			}
		})
	}
}

// settledMarkdown is the markdown of artifactID's latest version once it reads want, or the latest
// a settlement has stored when a ceiling passes: a settlement may store the seed's own version
// before the version of the write after it, so the write's is not always version 2.
func settledMarkdown(t *testing.T, database *store.Store, artifactID, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		stored, _ := latestVersionOf(t, database, artifactID)
		if stored == want || !time.Now().Before(deadline) {
			return stored
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A delete's find and an insert's or a move's anchor write no text over their match, so the
// shapes a replace's find is refused for crossing a span's edge
// (TestReplaceAcrossAMarkedSpansEdgeIsRefused) are taken from them, as they were before any quote
// was weighed against the spans it crosses: a quote across a bold, an italic or a code span's end,
// and plain text over a link (`wrote this` over `wrote [this](…)`). The delete removes the text
// it matched, as it did, a bold or an italic span it cut to end on a space keeping its delimiter
// with a character reference; the insert and the moved block land after the paragraph, which
// keeps its spans.
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
				waitForDocumentVersion(t, service.store, artifactID, 2)
				stored, _ := latestVersionOf(t, service.store, artifactID)
				read, err := service.Text(context.Background(), artifactID)
				if err != nil {
					t.Fatalf("read the document: %v", err)
				}
				if want := op.want(shape.paragraph, shape.deleted); stored != want || read != want {
					t.Fatalf("%s %q in %q:\n stored markdown %q\n read markdown   %q\nwant %q",
						op.op, shape.quote, markdown, stored, read, want)
				}
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
