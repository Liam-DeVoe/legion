package eval

import (
	"testing"
)

// TestAnAnswerIsRenderedAsTheBarDrawsItsLinks: on the seed, each link the bar draws as an item is
// written as the bar draws it. A link to a done issue, by its reference or by its dashboard path,
// is its chip: its key, "Done" and its title. A link to a key the store does not hold is the bare
// key, and one to a document it does not hold the bare reference. A link to a document or a file
// is its name, one to a spec the issue's title, one to an ask the head of its question, and one to
// a comment the head of its text with its reference in short form. A link the bar draws as no
// item, a picture, and a link in code stay as written.
func TestAnAnswerIsRenderedAsTheBarDrawsItsLinks(t *testing.T) {
	seed, err := LoadSeed(Files)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, answer, want string }{
		{
			"a done issue by its reference",
			"[DEMO-11](dispatch://DEMO-11) is closed.",
			"[DEMO-11 · Done · Board: card titles cut off at two lines] is closed.",
		},
		{
			"a done issue by its dashboard path, a title and an autolink",
			"See [the card titles](/issues/DEMO-11 \"card titles\") and <dispatch://DEMO-12>.",
			"See [DEMO-11 · Done · Board: card titles cut off at two lines] and [DEMO-12 · In progress · Export: CSV and PDF].",
		},
		{
			"an unknown key and an unknown document",
			"Filed as [DEMO-404, done](dispatch://DEMO-404); the log is [the log](/issues/DEMO-16/artifacts/no-such-log).",
			"Filed as [DEMO-404]; the log is [DEMO-16/artifact/no-such-log].",
		},
		{
			"a document, a file and a spec",
			"Read [the handbook](/projects/OPS/documents/oncall-handbook), [the error](dispatch://DEMO-23/artifact/upload-error-txt) and [its spec](dispatch://DEMO-3/spec).",
			"Read [On-call handbook], [upload-error.txt] and [Back-links in issue specs].",
		},
		{
			"an ask and a comment",
			"[the ask](/issues/DEMO-3/asks/" + ItemID("ask", "a01") + ") and [the note](dispatch://DEMO-16/comment/" + ItemID("comment", "relates-DEMO-16-DEMO-22") + ")",
			"[Should the back-link come first in the spec, above the…] and [Related to DEMO-22.]",
		},
		{
			"links the bar draws as no item",
			"[OPS-3](dispatch://OPS/OPS-3), [DEMO-1](https://dispatch.example/issues/DEMO-1), ![shot](dispatch://DEMO-11) and dispatch://DEMO-11.",
			"[OPS-3](dispatch://OPS/OPS-3), [DEMO-1](https://dispatch.example/issues/DEMO-1), ![shot](dispatch://DEMO-11) and dispatch://DEMO-11.",
		},
		{
			"links in code",
			"`[DEMO-11](dispatch://DEMO-11)`\n\n```\n[DEMO-11](dispatch://DEMO-11)\n```\n[DEMO-11](dispatch://DEMO-11)",
			"`[DEMO-11](dispatch://DEMO-11)`\n\n```\n[DEMO-11](dispatch://DEMO-11)\n```\n[DEMO-11 · Done · Board: card titles cut off at two lines]",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := RenderAnswer(c.answer, seed); got != c.want {
				t.Errorf("RenderAnswer(%q)\n= %q\nwant %q", c.answer, got, c.want)
			}
		})
	}
}

// TestATrysStoreRendersAnAnswerAsTheSeedDoes: the seeded store, as a try's grade reads it, renders
// every calibration answer of labels.json as the seed does, and a store renders an issue it holds
// in the status it holds.
func TestATrysStoreRendersAnAnswerAsTheSeedDoes(t *testing.T) {
	_, seed, tasks, before := seededStore(t)
	labels, err := LoadLabels(Files, tasks)
	if err != nil {
		t.Fatal(err)
	}
	for i, answer := range labels.Answers {
		if fromStore, fromSeed := RenderAnswer(answer.Answer, before), RenderAnswer(answer.Answer, seed); fromStore != fromSeed {
			t.Errorf("answer %d (%s) renders on the store as\n%q\nand on the seed as\n%q", i+1, answer.Task, fromStore, fromSeed)
		}
	}
	issue := before.Issues["DEMO-12"]
	issue.Status = "needs_review"
	before.Issues["DEMO-12"] = issue
	if got, want := RenderAnswer("[DEMO-12](/issues/DEMO-12)", before), "[DEMO-12 · Needs review · Export: CSV and PDF]"; got != want {
		t.Errorf("the store renders DEMO-12 as %q, want %q", got, want)
	}
}
