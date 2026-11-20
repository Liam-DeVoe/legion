package specform

import (
	"fmt"
	"maps"
	"strings"
	"testing"
)

// noted is a text of kind with body that refers to one footnote, labelled 1, whose body is note.
func noted(kind Kind, body, note string) Text {
	return Text{Node: "planted", Kind: kind, Body: body, Where: where, Notes: []*Footnote{{Label: "1", Body: note}}}
}

// placedIn is the label of the footnote whose finding detail is, from the place that the footnote
// judge writes in front of it (footnoteJudge.placed), or "" for a finding of the text's own words.
func placedIn(detail string) string {
	for _, prefix := range []string{"in its footnote [^", "in the footnote [^"} {
		if rest, ok := strings.CutPrefix(detail, prefix); ok {
			label, _, _ := strings.Cut(rest, "]")
			return label
		}
	}
	return ""
}

// withWayOut is text with way applied at each place of its finding, whose detail is detail: in each
// place of the words of the text, or of the footnote that the finding names (placedIn), as a
// writer who follows the finding changes the words wherever they stand. It reports false where
// those words do not hold way.from.
func withWayOut(text Text, detail string, way wayOut) (Text, bool) {
	label := placedIn(detail)
	if label == "" {
		changed := text
		changed.Body = strings.ReplaceAll(text.Body, way.from, way.to)
		return changed, strings.Contains(text.Body, way.from)
	}
	changed, found := text, false
	changed.Notes = make([]*Footnote, len(text.Notes))
	for index, note := range text.Notes {
		copied := *note
		if note.Label == label {
			found = strings.Contains(note.Body, way.from)
			copied.Body = strings.ReplaceAll(note.Body, way.from, way.to)
		}
		changed.Notes[index] = &copied
	}
	return changed, found
}

// Each way out that a finding gives is a form that every rule takes. For each finding of each text
// below, the test applies each of its ways out, as the change of the text that the finding's detail
// is written from (wayOut), at each place of the finding's words (withWayOut), and judges the text
// that results by every rule (Check): it must give no finding that the text does not give, word
// for word, and fewer findings of the finding's own rule. So no refusal suggests a form that a rule
// refuses again, as the condition's first way out once did ("Before you plan, file an issue or
// post a finding. Then run the search." was refused like the sentence it replaced), nor one that
// another rule refuses, as "that" before the clause of an instruction of 20 words, which makes it
// 21, or a space after the stop of a statement of two sentences, which makes it three. Each text
// gives at least one way out, so a finding that stops giving any fails here too. The rows after the
// first sixteen are the forms whose ways out another rule refuses, each with the way out that is
// left: in the sentence as the rules read it, code font and all; at each place of one finding; and
// in a footnote, with the sentences of the statement that refers to it.
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
		// The sentence as the rules read it, with what code font holds: a span of code that holds a
		// stop and a space ends no sentence, so the user and the host apart, which make 22 words of
		// an instruction, are no way out; and in a statement of two sentences, the name in code font
		// is still a way out of Web.Config.
		procedure("Send the log of `make test. all` to deploy@build.example.com after each run of the nightly merge test of the week every day."),
		procedure("Send the log of `git commit -m 'Fix it. Row added'` to deploy@build.example.com after each run of the nightly merge test of the week every day."),
		statement("Dispatch reads `one. two` and Web.Config at boot. It logs each read."),
		// One finding at two places, whose way out the writer takes at both: the user and the host
		// apart make the second sentence 28 words, so they are no way out, in either order of the
		// two sentences.
		statement("Dispatch mails ada@example.com each day. The report of the night build goes to ada@example.com with the log of the build and the list of the tests that failed last night."),
		statement("The report of the night build goes to ada@example.com with the log of the build and the list of the tests that failed last night. Dispatch mails ada@example.com each day."),
		statement("The build copies the report with scp to deploy@build.example.com: each night. " +
			"The build server keeps each copy of the report for a week or two at most, and the release job copies it to deploy@build.example.com: again."),
		// A footnote's sentences count in the statement that refers to it, so a space after the stop
		// of a footnote of one sentence, under a statement of one or two, makes three or four.
		noted(KindStatement, "Dispatch keeps each record.", "It reads Web.Config at boot."),
		noted(KindStatement, "Dispatch keeps each record. It reads them at boot.", "It reads Web.Config at boot."),
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
		row := text.Body
		for _, note := range text.Notes {
			row += fmt.Sprintf(" [^%s]: %s", note.Label, note.Body)
		}
		offered := 0
		for _, each := range judge(text) {
			for _, way := range each.ways {
				if way == (wayOut{}) {
					continue
				}
				offered++
				changed, ok := withWayOut(text, each.detail, way)
				if !ok {
					t.Errorf("%q: the way out of %s changes %q, which the words of its finding do not hold", row, each.rule, way.from)
					continue
				}
				own, ownAfter := 0, 0
				for key, times := range count(changed) {
					if times > before[key] {
						t.Errorf("%q: the way out %q to %q of %s gives a finding that the text does not give: %s: %s", row, way.from, way.to, each.rule, key.rule, key.detail)
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
					t.Errorf("%q: the way out %q to %q of %s is refused again by its own rule", row, way.from, way.to, each.rule)
				}
			}
		}
		if offered == 0 {
			t.Errorf("%q: no finding gives a way out", row)
		}
	}
}

// A way out stays where it leaves the findings of the other rules as they were: one sentence that
// names a person and holds a stop with no space after it gives each finding every way out. And a
// way out goes where another rule refuses its text: in a statement of two sentences, a space after
// the stop makes three, so that finding gives the code font alone; in a sentence of 23 words, the
// user and the host apart make 26, so the person's finding gives the two paths alone. The judge
// reads the sentence that the rules read, with its code font, so a stop and a space in code font
// change no way out. It reads each place of a finding, so an address in a short sentence and in
// one of 25 words gives the two paths alone, and an address at two places that each way out keeps
// in bounds gives all three. It reads a footnote's sentences with those of each statement that
// refers to it, so a space in a footnote of one sentence under a statement of one is no way out,
// while under a claim, or under a statement whose own words are code font alone, it is.
func TestAWayOutStaysWhereTheOtherFindingsStayAsTheyWere(t *testing.T) {
	procedure := func(body string) Text { return Text{Node: "v-run", Kind: KindProcedure, Body: body, Where: where} }
	for _, test := range []struct {
		text Text
		ways map[Rule]int
	}{
		{statement("Dispatch mails ada@example.com about Web.Config."), map[Rule]int{rulePerson: 3, ruleStopNoSpace: 2}},
		{statement("Dispatch mails ada@example.com about Web.Config. It mails her once."), map[Rule]int{rulePerson: 3, ruleStopNoSpace: 1}},
		{statement("Copy the report with scp report.txt deploy@build.example.com: to the host of the build each night after the tests of the merge pass today."),
			map[Rule]int{rulePerson: 2}},
		{statement("Dispatch reads `one. two` and Web.Config at boot. It logs each read."), map[Rule]int{ruleStopNoSpace: 1}},
		{procedure("Send the log of `make test. all` to deploy@build.example.com after each run of the nightly merge test of the week every day."),
			map[Rule]int{rulePerson: 2}},
		{procedure("Send the log of `make test all` to deploy@build.example.com after each run of the nightly merge test of the week every day."),
			map[Rule]int{rulePerson: 2}},
		{statement("Dispatch mails ada@example.com each day. The report of the night build goes to ada@example.com with the log of the build and the list of the tests that failed last night."),
			map[Rule]int{rulePerson: 2}},
		{statement("Dispatch mails ada@example.com each day. It mails ada@example.com at night."), map[Rule]int{rulePerson: 3}},
		{noted(KindStatement, "Dispatch keeps each record.", "It reads Web.Config at boot."), map[Rule]int{ruleStopNoSpace: 1}},
		{noted(KindStatement, "Dispatch keeps each record.", "It mails ada@example.com at boot."), map[Rule]int{rulePerson: 3}},
		{noted(KindClaim, "Dispatch keeps each record.", "It reads Web.Config at boot."), map[Rule]int{ruleStopNoSpace: 2}},
		{noted(KindStatement, "`Dispatch`.", "It reads Web.Config at boot."), map[Rule]int{ruleStopNoSpace: 2}},
	} {
		c := planted()
		in := Input{Texts: []Text{test.text}}
		people := peopleNames(c, in)
		got := map[Rule]int{}
		for _, each := range breaks(c, people, test.text, newFootnoteJudge(c, people, in.Texts)) {
			for _, way := range each.ways {
				if way != (wayOut{}) {
					got[each.rule]++
				}
			}
		}
		if !maps.Equal(got, test.ways) {
			t.Errorf("%q (footnotes %d): the ways out offered of each rule are %v, want %v", test.text.Body, len(test.text.Notes), got, test.ways)
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
