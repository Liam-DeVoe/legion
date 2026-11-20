package specform

import (
	"fmt"
	"maps"
	"strings"
	"testing"
)

// Each way out that a finding gives is a form that every rule takes. For each finding of each text
// below, the test applies each of its ways out, as the change of the text that the finding's detail
// is written from (wayOut), and judges the text that results by every rule (Check): it must give
// no finding that the text does not give, word for word, and fewer findings of the finding's own
// rule. So no refusal suggests a form that a rule refuses again, as the condition's first way out
// once did ("Before you plan, file an issue or post a finding. Then run the search." was refused
// like the sentence it replaced), nor one that another rule refuses, as "that" before the clause of
// an instruction of 20 words, which makes it 21, or a space after the stop of a statement of two
// sentences, which makes it three. Each text gives at least one way out, so a finding that stops
// giving any fails here too. The rows after the first sixteen are the forms whose ways out another
// rule refuses, each with the way out that is left.
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
		procedure("Compare the input and output the tools write for each run of the merge test against the fixture server today."),
		procedure("Read each open approval request of the root and record its version in the table of the review for today."),
		procedure("Before you plan the work of the week, file an issue or post a finding in the stream, run the search."),
		procedure("Read the diff or run the tests."),
		procedure("Read the log and, if it fails, send the report."),
		statement("The service reads Web.Config at its start. It logs each read."),
		statement("Copy the report with scp report.txt deploy@build.example.com: to the host of the build each night after the tests of the merge pass today."),
		// Two findings of two rules in one sentence: each way out leaves the other finding as it
		// was, so each is offered.
		statement("Dispatch mails ada@example.com about Web.Config."),
	} {
		c := planted()
		judge := func(text Text) []broken {
			in := Input{Texts: []Text{text}}
			people := peopleNames(c, in)
			return breaks(c, people, text, newFootnoteJudge(c, people, in.Texts))
		}
		type ruleDetail struct {
			rule   Rule
			detail string
		}
		count := func(text Text) map[ruleDetail]int {
			found := map[ruleDetail]int{}
			for _, each := range Check(c, Input{Texts: []Text{text}}) {
				found[ruleDetail{each.Rule, each.Detail}]++
			}
			return found
		}
		before := count(text)
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
				own, ownAfter := 0, 0
				for key, times := range count(changed) {
					if times > before[key] {
						t.Errorf("%q: the way out %q of %s gives a finding that the text does not give: %s: %s", text.Body, changed.Body, each.rule, key.rule, key.detail)
					}
					if key.rule == each.rule {
						ownAfter += times
					}
				}
				for key, times := range before {
					if key.rule == each.rule {
						own += times
					}
				}
				if ownAfter >= own {
					t.Errorf("%q: the way out %q of %s is refused again by its own rule", text.Body, changed.Body, each.rule)
				}
			}
		}
		if offered == 0 {
			t.Errorf("%q: no finding gives a way out", text.Body)
		}
	}
}

// A way out stays where it leaves the findings of the other rules as they were: one sentence that
// names a person and holds a stop with no space after it gives each finding every way out. And a
// way out goes where another rule refuses its text: in a statement of two sentences, a space after
// the stop makes three, so that finding gives the code font alone; in a sentence of 23 words, the
// user and the host apart make 26, so the person's finding gives the two paths alone.
func TestAWayOutStaysWhereTheOtherFindingsStayAsTheyWere(t *testing.T) {
	for _, test := range []struct {
		body string
		ways map[Rule]int
	}{
		{"Dispatch mails ada@example.com about Web.Config.", map[Rule]int{rulePerson: 3, ruleStopNoSpace: 2}},
		{"Dispatch mails ada@example.com about Web.Config. It mails her once.", map[Rule]int{rulePerson: 3, ruleStopNoSpace: 1}},
		{"Copy the report with scp report.txt deploy@build.example.com: to the host of the build each night after the tests of the merge pass today.",
			map[Rule]int{rulePerson: 2}},
	} {
		text := statement(test.body)
		c := planted()
		in := Input{Texts: []Text{text}}
		people := peopleNames(c, in)
		got := map[Rule]int{}
		for _, each := range breaks(c, people, text, newFootnoteJudge(c, people, in.Texts)) {
			for _, way := range each.ways {
				if way != (wayOut{}) {
					got[each.rule]++
				}
			}
		}
		if !maps.Equal(got, test.ways) {
			t.Errorf("%q: the ways out offered of each rule are %v, want %v", test.body, got, test.ways)
		}
	}
}

// The judge reads the ways out of the first maxJudgedInSentence findings of one sentence, so it
// reads a sentence a bounded number of times however many findings it holds. A finding after them
// is still refused and named, and its detail says that Dispatch offers no way out of it, not that
// none exists (pastNote). Control: the eighth finding of the sentence gives its ways out.
func TestAFindingPastTheEighthOfASentenceIsRefusedWithNoWayOutOffered(t *testing.T) {
	var words, names []string
	for index := 1; index <= maxJudgedInSentence+1; index++ {
		words = append(words, fmt.Sprintf("a%d.B", index))
		names = append(names, fmt.Sprintf("a%d@example.com", index))
	}
	for _, test := range []struct {
		rule  Rule
		body  string
		names []string
		base  func(name string) string
	}{
		{ruleStopNoSpace, "Dispatch reads " + strings.Join(words, " ") + " at boot.", words,
			func(word string) string { return stopNoSpaceAnd(word, "") }},
		{rulePerson, "Dispatch mails " + strings.Join(names, " ") + " each day.", names,
			func(name string) string { return personBreak(name, offer{}).detail }},
	} {
		var details []string
		for _, each := range Check(planted(), Input{Texts: []Text{statement(test.body)}}) {
			if each.Rule == test.rule {
				details = append(details, each.Detail)
			}
		}
		if len(details) != len(test.names) {
			t.Fatalf("%q: %d findings of %s, want %d: %q", test.body, len(details), test.rule, len(test.names), details)
		}
		last := len(details) - 1
		const note = ". Dispatch offers ways out for the first 8 findings of a sentence, so it offers none for this one, though one can exist"
		if want := test.base(test.names[last]) + note; details[last] != want {
			t.Errorf("%q: the finding after the %dth is %q, want %q", test.body, maxJudgedInSentence, details[last], want)
		}
		if eighth := details[last-1]; strings.Contains(eighth, note) || eighth == test.base(test.names[last-1]) {
			t.Errorf("%q: the %dth finding is %q, want its ways out and no note", test.body, maxJudgedInSentence, eighth)
		}
	}
}
