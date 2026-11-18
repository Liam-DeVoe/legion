package specform

import (
	"strings"
	"testing"
)

// The procedure of a check is an instruction in ASD-STE100 (R49): each of its sentences gives one
// instruction and has at most 20 words, where a statement, a claim, a purpose and a definition
// have at most 25. A sentence is an instruction when it starts with a verb in its base form, with
// no comma after it, and the first word of its object; after a condition that a comma ends, it
// starts after the comma. A second instruction is the same pair after "and", "or", "then" or a
// comma. Two verbs of one object, as in "Push and hold the switch", are one instruction, as
// ASD-STE100 lets two actions at the same time be. An adverb, a preposition or a question word is
// no verb. The same words in a claim break neither rule. The stated limits: a verb that ends in -s,
// -ed or -ing is no base form, so "Seed the database and run the test" names no instruction; a
// second instruction after a relative clause, as in "Read each request that Dispatch opens and
// record its version", is the reviewer's to name; and a condition ends at its first comma, so a
// list of verbs in it reads as two instructions.
func TestAProcedureGivesOneInstructionInEachSentenceOfTwentyWordsAtMost(t *testing.T) {
	procedure := func(body string) Text { return Text{Node: "v-run", Kind: KindProcedure, Body: body, Where: where} }
	twoInstructions := func(second string) string {
		return `a sentence gives two instructions, and a sentence of a procedure gives one: "` + second +
			`". Write each instruction in a sentence of its own`
	}
	const twenty = "Run the merge test against a fixture server that holds two approved specs of the same issue and three requests."
	const twentyOne = "Run the merge test against a fixture server that holds two approved specs of the same issue and three open requests."
	for _, test := range []struct {
		name string
		text Text
		want []Finding
	}{
		{"a procedure of 21 words", procedure(twentyOne), []Finding{finding(procedure(twentyOne), ruleSentenceWords,
			`a sentence has 21 words, and a sentence of a procedure has at most 20: "Run the merge test against a fixture server"`)}},
		{"a procedure of two instructions", procedure("Read each open approval request and record its version."),
			[]Finding{finding(procedure("Read each open approval request and record its version."), ruleInstructions, twoInstructions("record its version"))}},
		{"the review's procedure of two instructions in 22 words",
			procedure("Read each open approval request and record the document, its version, its author and the reason that the request gives for approval."),
			[]Finding{
				finding(procedure("Read each open approval request and record the document, its version, its author and the reason that the request gives for approval."),
					ruleSentenceWords, `a sentence has 22 words, and a sentence of a procedure has at most 20: "Read each open approval request and record the"`),
				finding(procedure("Read each open approval request and record the document, its version, its author and the reason that the request gives for approval."),
					ruleInstructions, twoInstructions("record the document, its version, its author and")),
			}},
		{"a second instruction after a comma", procedure("Read the log, record its size."),
			[]Finding{finding(procedure("Read the log, record its size."), ruleInstructions, twoInstructions("record its size"))}},
		{"a second instruction after then", procedure("Run the test, then read the log."),
			[]Finding{finding(procedure("Run the test, then read the log."), ruleInstructions, twoInstructions("read the log"))}},
		{"a second instruction after a condition", procedure("If the run fails, read the log and send the report."),
			[]Finding{finding(procedure("If the run fails, read the log and send the report."), ruleInstructions, twoInstructions("send the report"))}},
		{"an object in code font", procedure("Run `go test` and read the log."),
			[]Finding{finding(procedure("Run `go test` and read the log."), ruleInstructions, twoInstructions("read the log"))}},
		{"a second instruction of an object in code font", procedure("Read the log and run `go test ./...` on it."),
			[]Finding{finding(procedure("Read the log and run `go test ./...` on it."), ruleInstructions, twoInstructions("run `go test ./...` on it"))}},
		{"the control: a procedure of 20 words and one instruction", procedure(twenty), nil},
		{"the control: two sentences of one instruction each", procedure("Read each open approval request. Record its version."), nil},
		{"the control: two verbs of one object", procedure("Push and hold the switch."), nil},
		{"the control: two objects of one verb", procedure("Read the log and the report of each run."), nil},
		{"the control: a procedure that describes a program", procedure("A test against a Dispatch server from the head of the pull request."), nil},
		{"the control: a relative clause whose verbs a plural noun joins", procedure("Count the requests that open and close the same day."), nil},
		{"the control: an adverb after a comma", procedure("Open an ask on the root issue, never a child."), nil},
		{"the control: a question word after and", procedure("Check the label, the status and whether the document is the spec."), nil},
		{"the control: a list of nouns after a word and a comma", procedure("Instead, this skill reads each word and keeps it."), nil},
		{"the control: a word and a comma at the start", procedure("Locally, the tests read each file and record the time."), nil},
		{"the stated limit: a list of verbs in a condition", procedure("Before you plan, file an issue or post a finding, run the search."),
			[]Finding{finding(procedure("Before you plan, file an issue or post a finding, run the search."), ruleInstructions, twoInstructions("post a finding, run the search"))}},
		{"the control: a claim of two instructions", Text{Node: "v-run", Kind: KindClaim, Body: "Read each open approval request and record its version.", Where: where}, nil},
		{"the control: a claim of 21 words", Text{Node: "v-run", Kind: KindClaim, Body: twentyOne, Where: where}, nil},
		{"the stated limit: a base form that ends in -ed", procedure("Seed the database and run the test."), nil},
		{"the stated limit: a second instruction after a relative clause", procedure("Read each request that Dispatch opens and record its version."), nil},
	} {
		if got := Check(planted(), Input{Texts: []Text{test.text}}); !sameFindings(got, test.want) {
			t.Errorf("%s: Check = %#v, want %#v", test.name, got, test.want)
		}
	}
	if words := len(strings.Fields(twenty)); words != 20 {
		t.Fatalf("the control has %d words, want 20", words)
	}
}
