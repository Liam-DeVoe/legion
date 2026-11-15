package reviewer

import (
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// pictureAsk is an ask whose question holds a picture line and links. Its options carry no id, as
// the ask's row holds them (AskSubject).
func pictureAsk() Text {
	return Text{
		Kind: KindAsk,
		Body: "Finding from tester\nThe chart shows the wait. See https://ci.test/run/7 and [the log](https://ci.test/log/7).\n" +
			"![the wait](https://ci.test/chart.png)\nRef: dispatch://PRJ-1/comment/c1",
		Options: []model.AskOption{{Label: "Ship"}, {Label: "Hold", Description: "Wait"}},
	}
}

// Each rule of what a rewrite keeps: a rewrite that breaks it is not applied, with its reason,
// and one that keeps every rule applies.
func TestEachBreakOfWhatARewriteKeepsIsNotApplied(t *testing.T) {
	ask := pictureAsk()
	edit := func(change func(*Text)) Text {
		rewrite := ask
		rewrite.Options = slices.Clone(ask.Options)
		change(&rewrite)
		return rewrite
	}
	for _, test := range []struct {
		name    string
		rewrite Text
		rule    Keep
		reason  string
	}{
		{"one option fewer", edit(func(r *Text) { r.Options = r.Options[:1] }), KeepOptionCount,
			"the rewrite has 1 option, and the ask has 2"},
		{"the options in another order", edit(func(r *Text) { r.Options[0], r.Options[1] = r.Options[1], r.Options[0] }), KeepOptionOrder,
			`the rewrite puts the options in the order "Hold", "Ship", and the ask has them in the order "Ship", "Hold"`},
		{"an option's label", edit(func(r *Text) { r.Options[1].Label = "Hold it" }), KeepOptionLabels,
			`option 2 has the label "Hold it" in the rewrite, and "Hold" in the ask`},
		{"single to multiple choice", edit(func(r *Text) { r.Multiple = true }), KeepChoice,
			"the rewrite makes the single-choice ask a multiple-choice ask"},
		{"the first line", edit(func(r *Text) { r.Body = strings.Replace(r.Body, "Finding from tester", "A finding from the tester", 1) }), KeepFirstLine,
			`the rewrite changes the first line "Finding from tester", which has 12 words or fewer`},
		{"the Ref: line", edit(func(r *Text) {
			r.Body = strings.Replace(r.Body, "Ref: dispatch://PRJ-1/comment/c1", "Ref: dispatch://PRJ-1/comment/c1 (the finding)", 1)
		}), KeepRefLine,
			`the rewrite drops or changes the line "Ref: dispatch://PRJ-1/comment/c1"`},
		{"the picture line", edit(func(r *Text) {
			r.Body = strings.Replace(r.Body, "![the wait](https://ci.test/chart.png)\n", "The chart is at https://ci.test/chart.png.\n", 1)
		}), KeepPictureLines, `the rewrite drops or changes the picture line "![the wait](https://ci.test/chart.png)"`},
		{"a bare link", edit(func(r *Text) { r.Body = strings.Replace(r.Body, "See https://ci.test/run/7 and", "See", 1) }), KeepLinks,
			`the rewrite drops the link "https://ci.test/run/7"`},
		{"a Markdown link's address", edit(func(r *Text) { r.Body = strings.Replace(r.Body, "[the log](https://ci.test/log/7)", "the log", 1) }), KeepLinks,
			`the rewrite drops the link "https://ci.test/log/7"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Preserve(Record{Text: ask}, test.rewrite)
			if got == nil || got.Rule != test.rule || got.Reason != test.reason {
				t.Fatalf("Preserve = %+v, want not applied for %s: %s", got, test.rule, test.reason)
			}
			if got.Error() != "the rewrite is not applied: "+test.reason {
				t.Errorf("error = %q", got.Error())
			}
		})
	}

	kept := edit(func(r *Text) {
		r.Body = strings.Replace(r.Body, "The chart shows the wait.", "The test waits 40 s for the store.", 1)
		r.Options[1].Description = "Wait until the store answers in under 1 s"
	})
	if got := Preserve(Record{Text: ask}, kept); got != nil {
		t.Errorf("a rewrite that keeps every rule: %v", got)
	}
	// An option's id is the server's (optionWords), so Preserve weighs none: a rewrite, which
	// writes no id, of an ask whose options carry the part ids a read shows keeps every rule.
	read := pictureAsk()
	read.Options[0].ID, read.Options[1].ID = "b-2", "b-3"
	if got := Preserve(Record{Text: read}, kept); got != nil {
		t.Errorf("a rewrite of an ask whose options carry ids: %v", got)
	}
}

// The first line is kept only where the question has more than one line and that line has 12
// words or fewer; a comment keeps its first line by the same rule.
func TestTheFirstLineIsKeptWhereItIsShortAndNotAlone(t *testing.T) {
	long := "This first line holds thirteen words, one more than the bound of twelve here\nThe rest."
	for _, test := range []struct {
		name          string
		original, now Text
		want          Keep
	}{
		{"an ask of one line", Text{Kind: KindAsk, Body: "Ship?"}, Text{Kind: KindAsk, Body: "Ship it today?"}, ""},
		{"an ask whose first line is long", Text{Kind: KindAsk, Body: long}, Text{Kind: KindAsk, Body: "Short.\nThe rest."}, ""},
		{"a comment's first line", Text{Kind: KindComment, Body: "Finding from tester\nThis names the problem."},
			Text{Kind: KindComment, Body: "Finding from the tester\nThe test fails."}, KeepCommentFirstLine},
		{"a comment that keeps its first line", Text{Kind: KindComment, Body: "Finding from tester\nThis names the problem."},
			Text{Kind: KindComment, Body: "Finding from tester\nThe test fails."}, ""},
		{"a comment of one line", Text{Kind: KindComment, Body: "This names the problem."}, Text{Kind: KindComment, Body: "The test fails."}, ""},
		{"a message keeps nothing", Text{Kind: KindMessage, Body: "Finding from tester\nThis names the problem."},
			Text{Kind: KindMessage, Body: "The test fails."}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Preserve(Record{Text: test.original}, test.now)
			if (got == nil) != (test.want == "") || (got != nil && got.Rule != test.want) {
				t.Errorf("Preserve = %+v, want %q", got, test.want)
			}
		})
	}
}

// A decision block keeps each option line its document's ask-edit route does not rewrite: one
// that holds a link, bold, code or a picture, or lines below it (docs.LostOptionWords).
func TestADecisionBlockKeepsTheOptionLinesItsDocumentHolds(t *testing.T) {
	markdown := "# Plan\n\n:::ask{#blk urgency=\"med\" multiple=\"false\"}\nShould we ship?\n\n" +
		"- Ship: Release it\n- Hold: see [the memo](https://memo.test)\n:::\n"
	record := Record{
		Text: Text{Kind: KindDecisionBlock, Body: "Should we ship?", Options: []model.AskOption{
			{Label: "Ship", Description: "Release it"}, {Label: "Hold", Description: "see the memo"},
		}},
		Document: &Document{Name: "plan", Markdown: markdown, Holds: true, BlockID: "blk"},
	}
	plain := record.Text
	plain.Options = []model.AskOption{{Label: "Ship", Description: "Release it today"}, {Label: "Hold", Description: "see the memo"}}
	if got := Preserve(record, plain); got != nil {
		t.Errorf("a rewrite of a plain option line: %v", got)
	}
	marked := record.Text
	marked.Options = []model.AskOption{{Label: "Ship", Description: "Release it"}, {Label: "Hold", Description: "wait for the memo"}}
	got := Preserve(record, marked)
	if got == nil || got.Rule != KeepOptionLines || !strings.Contains(got.Reason, `the edit rewrites the line of the option "Hold", which holds more than its plain text`) {
		t.Fatalf("Preserve = %+v, want the option line the document keeps", got)
	}

	// The block's own options keep their count, order, labels and choice first.
	fewer := record.Text
	fewer.Options = fewer.Options[:1]
	if got := Preserve(record, fewer); got == nil || got.Rule != KeepOptionCount {
		t.Errorf("Preserve = %+v, want the option count", got)
	}
	// A record without the block's document weighs no option line; the write weighs it again.
	if got := Preserve(Record{Text: record.Text}, marked); got != nil {
		t.Errorf("Preserve with no document = %v", got)
	}
	// A block the document does not hold is not applied either.
	missing := record
	missing.Document = &Document{Markdown: "# Plan\n", Holds: true, BlockID: "blk"}
	if got := Preserve(missing, marked); got == nil || got.Rule != KeepOptionLines || !strings.HasPrefix(got.Reason, "the decision block cannot be read in its document") {
		t.Errorf("Preserve of a block its document lacks = %+v", got)
	}
}
