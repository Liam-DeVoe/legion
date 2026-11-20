package specform

import (
	"strings"
	"testing"
)

// Each way out that a finding gives is a form its rule takes. For each finding of each text below,
// the test applies each of its ways out, as the change of the text that the finding's detail is
// written from (wayOut), and judges the text that results by the same rule: it must give no finding
// of that rule. So no refusal suggests a form that its rule refuses again, as the condition's first
// way out once did ("Before you plan, file an issue or post a finding. Then run the search." was
// refused like the sentence it replaced). Each text gives at least one way out, so a finding that
// stops giving any fails here too.
func TestEachWayOutOfARefusalPasses(t *testing.T) {
	procedure := func(body string) Text { return Text{Node: "v-run", Kind: KindProcedure, Body: body, Where: where} }
	for _, text := range []Text{
		procedure("Read each open approval request and record its version."),
		procedure("Read each open approval request and record the document, its version, its author and the reason that the request gives for approval."),
		procedure("Read the log, record its size."),
		procedure("Run the test, then read the log."),
		procedure("If the run fails, read the log and send the report."),
		procedure("Run `go test` and read the log."),
		procedure("Read the log and run `go test ./...` on it."),
		procedure("Before you plan, file an issue or post a finding, run the search."),
		procedure("After the run ends, read the log, record the time and send the report."),
		procedure("Run the test of the step, name `lint`, with the race detector."),
		procedure("Compare the input and output the tools write."),
		procedure("Read the log and record the date, then send the report and close the issue."),
		statement("Dispatch mails ada@example.com about each change."),
		statement("Copy the report with scp report.txt deploy@build.example.com: to the host."),
		statement("The service reads Web.Config at its start."),
		statement("Dispatch writes each comment.Dispatch reads it later."),
	} {
		c := planted()
		judge := func(text Text) []broken {
			in := Input{Texts: []Text{text}}
			people := peopleNames(c, in)
			return breaks(c, people, text, newFootnoteJudge(c, people, in.Texts))
		}
		offered := 0
		for _, each := range judge(text) {
			for _, way := range each.ways {
				if way == (wayOut{}) {
					continue
				}
				offered++
				if !strings.Contains(text.Body, way.from) {
					t.Errorf("%q: the way out of %s changes %q, which the text does not hold", text.Body, each.rule, way.from)
					continue
				}
				changed := text
				changed.Body = strings.Replace(text.Body, way.from, way.to, 1)
				for _, again := range judge(changed) {
					if again.rule == each.rule {
						t.Errorf("%q: the way out %q of %s is refused again: %s", text.Body, changed.Body, each.rule, again.detail)
					}
				}
			}
		}
		if offered == 0 {
			t.Errorf("%q: no finding gives a way out", text.Body)
		}
	}
}
