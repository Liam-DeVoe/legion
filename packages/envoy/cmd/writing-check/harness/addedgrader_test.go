package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
)

// The fixture of parentRequests beside the golden calls' (requests_test.go): the new text, with a
// sentence the phrases judgement reads, and a definition.
const (
	fixtureNewText    = "The zork train, the queue of pull requests, is ready.\n\nIt merges at noon. This names the problem."
	fixtureDefinition = "- zork train: The queue of pull requests."
)

// markCheck is a sentence of verify's check prompt (grade-added-verify.md).
const markCheck = "A first check listed facts of the new text that it found in no input."

// gradeFixture is judgement j's input on the fixture of parentRequests: every part a judgement
// may read, with the session part of changeSessions' comment.
func gradeFixture(j Judgement, session claude.SessionText) GradeInput {
	return GradeInput{Judgement: j, Reply: j == JFirstParagraph || j == JAnswerStart, Question: goldenQuestion, Answer: goldenAnswer,
		Original: goldenText, Text: fixtureNewText, Definition: fixtureDefinition, WriterAnswer: writerAnswer,
		Record: goldenRecord, Session: session, OwnWords: []string{"zork"}}
}

// goldenWire is the SHA-256 of one call's system prompt and of its user message.
type goldenWire struct{ system, user string }

// parentRequest is one judgement's request under one input mode: the grade's input_sha256, then
// each call's system prompt and user message.
type parentRequest struct {
	mode  string
	j     Judgement
	input string
	calls []goldenWire
}

// parentRequests are the requests of every judgement on gradeFixture as the harness sent them
// before --added-grader: its code at commit afdb071c, run on the fixture against a stand-in that
// answers each judgement (allJudgementRules), under the full and the final input modes.
var parentRequests = []parentRequest{
	{"full", JAnswered, "0eeecf0de6c0d83e95ca8488a4b1714f10c0da5a79a202d92a8732944cb4456e", []goldenWire{{"cf36249b1970b10d14e64cf08820a29d602e5929a1f76361bc54680a2c782ef4", "5885d96945177e26eadeb4284b204e4b9248bea8b471576740c371725e07027a"}}},
	{"full", JFirstParagraph, "f7e709d01c49f759f3cb9a61fc5886df878d1cce9c3616ef632daf12c1db0350", []goldenWire{{"1a66660d68641316a2f603b0d96d4b390acc6e4ffc271eb9f32796347c73faaa", "5885d96945177e26eadeb4284b204e4b9248bea8b471576740c371725e07027a"}}},
	{"full", JLost, "709f23bd3823d2d77305cf0b3fa6baf176a7012d351d2df082cd3c428a24545d", []goldenWire{{"61e39e1ce2168056537289d021ff60875a4a14dde0cf05f3aca417d8f3e07c18", "1fd7e9c78e460334072aa9d67b897805a2e6555e1360aec61d0d98bbda3a68ff"}}},
	{"full", JAdded, "5286a22cd592b652bf8e203749a1a7b3a701b3af50de652ccb79c5c0db555972", []goldenWire{{"d41bd06c8aea8dbb67606f84fc0c1a9499a49d1a8011ab2c5ca6fc71bf7a99cd", "1fd7e9c78e460334072aa9d67b897805a2e6555e1360aec61d0d98bbda3a68ff"}}},
	{"full", JPhrases, "26d25ae556c25de1566f14301bf81e4f6e8f39c29a6e14c9a9a56a581d055a7a", []goldenWire{{"532192416bb6a75de0a6853b2b040493fc030b0a6914d6d2f33583e0770f0454", "8bdbc9c2a8ce0a7986b82ca1426e59936b09c4aa3b29fda0669f9ea72ba45689"}}},
	{"full", JUndefined, "d22e4dd8a7b6c6b2ff26cb450d25697642ea7b6a6679f0ccf8f6552318eea591", []goldenWire{{"f06f28c01def94b18d00102838d13591551e1015f0f19f91d1a8e2d5cab81ad6", "a6016045e58c43d1c17aaf8849041c319861e2d6e728a1f09993920bb51e8b5c"}}},
	{"full", JAnswerStart, "bfa21846ec58be120cd822776c545eaaf9623f5415c851f83ea3ff43efecddb0", []goldenWire{{"99b1154953d07b613794b7442fa3ef3a7c4e2a40a10dadbc862169997a1dc61d", "3e87e38776fec9c543dec4c935712360d1d15bfa8fdf7dfbbc981dc0d421f0dd"}}},
	{"full", JHarder, "101f799128e0c0a3ae01b021db60521ed7059866ae3dcc812a3f62e7c811d22a", []goldenWire{{"bf394ff3b4d42cf9a72d08f8e83272bc08ebb371f931a3022d063ad7f3cc8098", "1fd7e9c78e460334072aa9d67b897805a2e6555e1360aec61d0d98bbda3a68ff"}}},
	{"full", JDefine, "f24235eb1c74b8cc63744a11bd38f8bd53358e24d3167f71d59b4856d93b06d6", []goldenWire{{"932ba2cd31271bdf8b336fe677e8a5fb9bac846b13d9d8e090c8f2cf30cd4c48", "eefd5fb2a9884a6a025ddb4d5f896c31e03f9fa3fcc4ede3b36a8e6f1b5fe1e3"}}},
	{"final", JAnswered, "1ce5e09daae80855a49e56e39faef36e8664ca3f36d8391343e3ad76b7448a0a", []goldenWire{{"91703a03414b4aae981b6f7f44a139aef57a8d64a5ceaffa03b47735db04335a", "7b86256d53eb8ed15ce3ce704ecdbe8f5237535baa2fbb61b437bed7e5fbb1da"}}},
	{"final", JFirstParagraph, "5c215b8f53e401c6b08f39a4c1bb49e9e22a57ef1ece21bb2b6f9ce359efdd14", []goldenWire{{"d273a5cabd549ace7bfc42222b83c3c20a62275648eb9cb292cf5a1bb97de4ac", "7b86256d53eb8ed15ce3ce704ecdbe8f5237535baa2fbb61b437bed7e5fbb1da"}}},
	{"final", JLost, "709f23bd3823d2d77305cf0b3fa6baf176a7012d351d2df082cd3c428a24545d", []goldenWire{{"61e39e1ce2168056537289d021ff60875a4a14dde0cf05f3aca417d8f3e07c18", "1fd7e9c78e460334072aa9d67b897805a2e6555e1360aec61d0d98bbda3a68ff"}}},
	{"final", JAdded, "5286a22cd592b652bf8e203749a1a7b3a701b3af50de652ccb79c5c0db555972", []goldenWire{{"d41bd06c8aea8dbb67606f84fc0c1a9499a49d1a8011ab2c5ca6fc71bf7a99cd", "1fd7e9c78e460334072aa9d67b897805a2e6555e1360aec61d0d98bbda3a68ff"}}},
	{"final", JPhrases, "26d25ae556c25de1566f14301bf81e4f6e8f39c29a6e14c9a9a56a581d055a7a", []goldenWire{{"532192416bb6a75de0a6853b2b040493fc030b0a6914d6d2f33583e0770f0454", "8bdbc9c2a8ce0a7986b82ca1426e59936b09c4aa3b29fda0669f9ea72ba45689"}}},
	{"final", JUndefined, "d22e4dd8a7b6c6b2ff26cb450d25697642ea7b6a6679f0ccf8f6552318eea591", []goldenWire{{"f06f28c01def94b18d00102838d13591551e1015f0f19f91d1a8e2d5cab81ad6", "a6016045e58c43d1c17aaf8849041c319861e2d6e728a1f09993920bb51e8b5c"}}},
	{"final", JAnswerStart, "c4672bf5aa698051f49682dd5888d8a341a19993485ac333d4f277610ed44329", []goldenWire{{"a5a794b81c2df40ae8a8ad1e05792e216a7fd2cb2813609adc777f03e88eae99", "18e6ace5c6d0dc91b789f42a26690a19e08da02e504a1fa5c66bf2d8eaaee337"}}},
	{"final", JHarder, "b15c1c5886c84ec97e7826d00176ccf410177c0508344b723e4cac30698ebf0f", []goldenWire{{"f1279e04da9e8f2dc0e87eb0b507eb27d809d04197ad0b3ef9a65fe9c0bc781e", "1dd92cfe55cb9302ae615dd9b244ffc743f04a65fa07e4da72d2417f449a2985"}}},
	{"final", JDefine, "f24235eb1c74b8cc63744a11bd38f8bd53358e24d3167f71d59b4856d93b06d6", []goldenWire{{"932ba2cd31271bdf8b336fe677e8a5fb9bac846b13d9d8e090c8f2cf30cd4c48", "eefd5fb2a9884a6a025ddb4d5f896c31e03f9fa3fcc4ede3b36a8e6f1b5fe1e3"}}},
}

// standGrader is the grader claude-opus-5 at stand, with no fallback, reading the context mode
// gives, under the added grader added.
func standGrader(t *testing.T, stand *modeltest.Model, mode, added string) *Grader {
	t.Helper()
	model, err := claude.New(stand.URL, keyFile(t), "claude-opus-5", "", agentloop.Price{}, &llm.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := ParseGraderInputs(mode)
	if err != nil {
		t.Fatal(err)
	}
	g := NewGrader(model, nil, inputs)
	g.Added = added
	return g
}

// gradeTurns grades in with g, taking a grade of done as GradeOrReuse does, and answers the grade
// and the calls it made at stand.
func gradeTurns(t *testing.T, stand *modeltest.Model, g *Grader, in GradeInput, done ...*Grade) (Grade, []wireTurn) {
	t.Helper()
	before := len(stand.Requests())
	grade := g.GradeOrReuse(t.Context(), in, done)
	var turns []wireTurn
	for _, r := range stand.Requests()[before:] {
		turns = append(turns, decodeTurn(t, r))
	}
	return grade, turns
}

// The input_sha256 of the variants' grades on gradeFixture at commit 2e1d5c44, where inputs-first
// and verify were first measured: the same under the full and the final modes, which give lost and
// added the full inputs. A run of later code takes the grades of a run of that commit.
const (
	inputsFirstLostSHA256  = "21286d07e0454ea6b5695c33a1335471e6983dccb0186dce8c5af11b77397c36"
	inputsFirstAddedSHA256 = "d8f6a9570e6f364a2eb5735304687dae89d97d5dd9ff6c36232b64cdbf7143ef"
	verifyAddedSHA256      = "2e924e870f9c119978b14836dd69078fce1a4e51cde4dc438efbf47667bf46d8"
)

// Under the old added grader, the default, every judgement sends what it sent before the flag:
// its system prompt and user message byte for byte, and its grade's input_sha256. So does every
// judgement that the other variants do not change: all but lost and added under inputs-first and
// inputs-first-verify, and all but added under verify.
func TestEveryJudgementTheVariantDoesNotChangeSendsItsRequestOfBefore(t *testing.T) {
	stand := modeltest.New(t, allJudgementRules(t)...)
	session := changeSessions(t)["question/comment:c2@20"]
	for _, added := range append([]string{""}, AddedGraders...) {
		changed := 0
		for _, row := range parentRequests {
			g := standGrader(t, stand, row.mode, added)
			if g.addedGrader(row.j) != AddedOld {
				changed++
				continue
			}
			grade, turns := gradeTurns(t, stand, g, gradeFixture(row.j, session))
			if grade.Error != "" || grade.InputSHA256 != row.input || grade.AddedGrader != "" || grade.Verify != nil {
				t.Errorf("--added-grader %q, %s %s: the grade is %+v, want no error and the input_sha256 %s", added, row.mode, row.j, grade, row.input)
			}
			if len(turns) != len(row.calls) {
				t.Fatalf("--added-grader %q, %s %s: %d calls, want %d", added, row.mode, row.j, len(turns), len(row.calls))
			}
			for i, w := range turns {
				if sha256Text(w.system(t)) != row.calls[i].system || sha256Text(w.user(t)) != row.calls[i].user || w.marks != 0 {
					t.Errorf("--added-grader %q, %s %s: call %d is not the one of before:\n%s\n%s", added, row.mode, row.j, i, w.system(t), w.user(t))
				}
			}
		}
		want := map[string]int{"": 0, AddedOld: 0, AddedInputsFirst: 4, AddedVerify: 2, AddedInputsFirstVerify: 4}[added]
		if changed != want {
			t.Errorf("--added-grader %q changes %d of the rows, want %d", added, changed, want)
		}
	}
}

// inputs-first sends lost and added their own system prompt and reply schema, and the user
// message's parts in another order: the question, the record, the writer's answer and the
// session part, then the original text and, last, the new text. The message is the old one's
// bytes in that order. The grade's input_sha256 differs from the old one's, even where the bytes
// are the same (lost under trimmed inputs reads the two texts alone), so --grades-from never
// takes a grade of one variant for the other. inputs-first-verify sends lost and added the same
// requests: its lost grade is inputs-first's, of the same input_sha256; its added grade, whose
// first call lists no fact here, has its own, and names inputs-first's as its first call's.
func TestInputsFirstMovesOnlyTheTextsOfLostAndAdded(t *testing.T) {
	stand := modeltest.New(t, allJudgementRules(t)...)
	session := changeSessions(t)["question/comment:c2@20"]
	for _, variant := range []string{AddedInputsFirst, AddedInputsFirstVerify} {
		for _, row := range parentRequests {
			if row.j != JLost && row.j != JAdded {
				continue
			}
			old, oldTurns := gradeTurns(t, stand, standGrader(t, stand, row.mode, AddedOld), gradeFixture(row.j, session))
			grade, turns := gradeTurns(t, stand, standGrader(t, stand, row.mode, variant), gradeFixture(row.j, session))
			if len(oldTurns) != 1 || len(turns) != 1 {
				t.Fatalf("%s, %s %s: %d and %d calls, want one each", variant, row.mode, row.j, len(oldTurns), len(turns))
			}
			want, pinned := variant, inputsFirstAddedSHA256
			if row.j == JLost {
				want, pinned = AddedInputsFirst, inputsFirstLostSHA256
			}
			if grade.Error != "" || grade.AddedGrader != want || grade.InputSHA256 == old.InputSHA256 {
				t.Errorf("%s, %s %s: the grade is %+v, want %s's with an input_sha256 of its own", variant, row.mode, row.j, grade, want)
			}
			switch {
			case want == AddedInputsFirst && grade.InputSHA256 != pinned:
				t.Errorf("%s, %s %s: the input_sha256 %s is not inputs-first's of commit 2e1d5c44, %s", variant, row.mode, row.j, grade.InputSHA256, pinned)
			case want == AddedInputsFirstVerify && (grade.InputSHA256 == pinned || grade.Verify == nil || grade.Verify.FirstSHA256 != pinned || grade.Verify.Call != nil):
				t.Errorf("%s, %s %s: the grade is %+v, want inputs-first's call as its first, an input_sha256 of its own, and no check", variant, row.mode, row.j, grade)
			}
			w := turns[0]
			if sha256Text(w.system(t)) != row.calls[0].system || !sameJSON(t, w.OutputConfig.Format.Schema, judgementSpecs[row.j].schema()) || w.marks != 0 {
				t.Errorf("%s, %s %s: the system prompt or the schema is not the old one:\n%s", variant, row.mode, row.j, w.system(t))
			}
			user, oldUser := w.user(t), oldTurns[0].user(t)
			start := strings.TrimRight(tag("question", goldenQuestion)+goldenRecord+tag("writer_answer", writerAnswer), "\n") + "\n\n<" + claude.SessionTag + ">\n"
			end := "\n</" + claude.SessionTag + ">\n" + tag("original_text", goldenText) + tag("new_text", fixtureNewText)
			if !strings.HasPrefix(user, start) || !strings.HasSuffix(user, end) || len(user) != len(oldUser) {
				t.Errorf("%s, %s %s: the user message is\n%s\nwant the question, the record, the writer's answer, the session part, then the two texts, in the old message's bytes", variant, row.mode, row.j, user)
			}
			for _, part := range []string{tag("question", goldenQuestion), goldenRecord, tag("original_text", goldenText), tag("new_text", fixtureNewText), "SESSIONFACT"} {
				if !strings.Contains(oldUser, part) || !strings.Contains(user, part) {
					t.Errorf("%s, %s %s: the part %q is not in both messages", variant, row.mode, row.j, part)
				}
			}
		}
	}

	in := gradeFixture(JLost, session)
	old, oldTurns := gradeTurns(t, stand, standGrader(t, stand, InputsTrimmed, AddedOld), in)
	first, turns := gradeTurns(t, stand, standGrader(t, stand, InputsTrimmed, AddedInputsFirst), in, &old)
	if len(turns) != 1 || oldTurns[0].user(t) != turns[0].user(t) || first.Reused || first.InputSHA256 == old.InputSHA256 {
		t.Errorf("trimmed lost: inputs-first made %d calls, the same message: %t, reused: %t; want one call of the same message, with an input_sha256 of its own",
			len(turns), len(turns) == 1 && oldTurns[0].user(t) == turns[0].user(t), first.Reused)
	}
	// The two variants' lost requests are one request: each takes the other's grade.
	taken, turns := gradeTurns(t, stand, standGrader(t, stand, InputsTrimmed, AddedInputsFirstVerify), in, &first)
	if len(turns) != 0 || !taken.Reused || taken.AddedGrader != AddedInputsFirst {
		t.Errorf("trimmed lost: inputs-first-verify made %d calls with inputs-first's grade, the grade %+v; want it taken", len(turns), taken)
	}
	made, _ := gradeTurns(t, stand, standGrader(t, stand, InputsTrimmed, AddedInputsFirstVerify), in)
	if back, turns := gradeTurns(t, stand, standGrader(t, stand, InputsTrimmed, AddedInputsFirst), in, &made); len(turns) != 0 || !back.Reused {
		t.Errorf("trimmed lost: inputs-first made %d calls with inputs-first-verify's grade; want it taken", len(turns))
	}
}

// listedFacts are the added call's facts on the fixture of the verify test, and checkReply the
// check's reply: the first is held by the original text, with its words in Markdown; the second
// by the session part alone; the third is said to be held with words that no input holds; the
// fourth is held by no input.
var listedFacts = []Item{
	{Quote: "It merges at noon.", Note: "The train merges at noon."},
	{Quote: "the queue of pull requests", Note: "The zork train is the queue of pull requests."},
	{Quote: "is ready", Note: "The train is ready."},
	{Quote: "This names the problem.", Note: "A problem exists."},
}

func verifyRules(t *testing.T) []modeltest.Rule {
	t.Helper()
	var added []map[string]string
	for _, f := range listedFacts {
		added = append(added, map[string]string{"1_quote": f.Quote, "2_fact": f.Note})
	}
	check := []map[string]any{
		{"1_number": "1", "2_input_words": "It merges at **noon**.", "3_held": true},
		{"1_number": "2", "2_input_words": "SESSIONFACT: the zork train", "3_held": true},
		{"1_number": "2", "2_input_words": "", "3_held": false},
		{"1_number": "3", "2_input_words": "The train ships on Friday.", "3_held": true},
		{"1_number": "4", "2_input_words": "", "3_held": false},
	}
	return append([]modeltest.Rule{
		{Contains: markCheck, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"facts": check})}},
		{Contains: markAdded, Match: func(r modeltest.Request) bool { return strings.Contains(r.Text, fixtureNewText) },
			Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"added": added})}},
	}, allJudgementRules(t)...)
}

// verify makes the old added request, then, where it lists a fact, the check: one call that reads
// the inputs as the added call read them, with no new text, and the listed facts after the session
// part. The grade keeps a fact no input holds, and a fact whose held words no input holds as
// given; it drops a fact whose held words the original text or the session part holds. It records
// both lists and the check's call. Where the first call lists no fact, there is no check. An old
// added grade of the same inputs is the first call's grade: the run makes the check alone.
func TestVerifyChecksTheListedFactsAgainstTheInputs(t *testing.T) {
	stand := modeltest.New(t, verifyRules(t)...)
	session := changeSessions(t)["question/comment:c2@20"]
	in := gradeFixture(JAdded, session)
	verify := standGrader(t, stand, InputsFull, AddedVerify)
	grade, turns := gradeTurns(t, stand, verify, in)
	if len(turns) != 2 {
		t.Fatalf("verify made %d calls, want the added call and the check", len(turns))
	}
	golden := parentRequests[slices.IndexFunc(parentRequests, func(r parentRequest) bool { return r.mode == InputsFull && r.j == JAdded })]
	if sha256Text(turns[0].system(t)) != golden.calls[0].system || sha256Text(turns[0].user(t)) != golden.calls[0].user {
		t.Errorf("verify's first call is not the old added request:\n%s", turns[0].user(t))
	}
	check := turns[1]
	checkReq, err := checkRequest(promptData{HasQuestion: true, HasRecord: true, HasSession: true, WriterAnswer: true})
	if err != nil {
		t.Fatal(err)
	}
	if system := check.system(t); system != checkReq.System || !strings.Contains(system, markCheck) || !strings.Contains(system, dispatchSentence) || !strings.Contains(system, dataSentence) {
		t.Errorf("the check's system prompt is\n%s", system)
	}
	if !sameJSON(t, check.OutputConfig.Format.Schema, checkReq.Schema) || check.marks != 0 {
		t.Errorf("the check's schema is %s", check.OutputConfig.Format.Schema)
	}
	user := check.user(t)
	start := strings.TrimRight(tag("question", goldenQuestion)+tag("original_text", goldenText)+goldenRecord+tag("writer_answer", writerAnswer), "\n") + "\n\n<" + claude.SessionTag + ">\n"
	end := "\n</" + claude.SessionTag + ">\n" + tag("listed_facts",
		"1. Fact: The train merges at noon.\n   Words of the new text: It merges at noon.\n"+
			"2. Fact: The zork train is the queue of pull requests.\n   Words of the new text: the queue of pull requests\n"+
			"3. Fact: The train is ready.\n   Words of the new text: is ready\n"+
			"4. Fact: A problem exists.\n   Words of the new text: This names the problem.\n")
	if !strings.HasPrefix(user, start) || !strings.HasSuffix(user, end) || strings.Contains(user, "<new_text>") {
		t.Errorf("the check's user message is\n%s\nwant the inputs, the session part, then the listed facts, and no new text", user)
	}

	v := grade.Verify
	if grade.Error != "" || v == nil || grade.AddedGrader != AddedVerify || grade.Reused {
		t.Fatalf("the grade is %+v", grade)
	}
	if !slices.Equal(v.Listed, listedFacts) || !slices.Equal(grade.Items, listedFacts[2:]) || grade.Count != 2 {
		t.Errorf("listed %+v, kept %+v (count %d); want the four facts, and the last two kept", v.Listed, grade.Items, grade.Count)
	}
	held := []HeldFact{{Item: listedFacts[0], InputWords: "It merges at **noon**."}, {Item: listedFacts[1], InputWords: "SESSIONFACT: the zork train"}}
	if !slices.Equal(v.Held, held) || !slices.Equal(v.NotFound, []HeldFact{{Item: listedFacts[2], InputWords: "The train ships on Friday."}}) {
		t.Errorf("held %+v, held with words not found %+v", v.Held, v.NotFound)
	}
	if v.Call == nil || v.Call.Model != "claude-opus-5" || v.Call.Input == 0 || v.FirstReused || v.FirstSHA256 != golden.input ||
		grade.InputSHA256 != verifyAddedSHA256 || len(grade.ownCalls()) != 2 {
		t.Errorf("the check's record: %+v; the grade's input_sha256 %s (want verify's of commit 2e1d5c44, %s), its calls %d", v, grade.InputSHA256, verifyAddedSHA256, len(grade.ownCalls()))
	}

	// An old grade of the same inputs is the first call: the run makes the check alone.
	old, _ := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedOld), in)
	again, turns := gradeTurns(t, stand, verify, in, &old)
	if len(turns) != 1 || !strings.Contains(turns[0].system(t), markCheck) || again.Reused || !again.Verify.FirstReused ||
		again.Count != 2 || again.InputSHA256 != grade.InputSHA256 || len(again.ownCalls()) != 1 || again.ownCalls()[0] != *again.Verify.Call {
		t.Errorf("with the old grade: %d calls, the grade %+v", len(turns), again)
	}
	// A verify grade of the same inputs is taken whole; an old run does not take it.
	whole, turns := gradeTurns(t, stand, verify, in, &grade)
	if len(turns) != 0 || !whole.Reused || len(whole.ownCalls()) != 0 {
		t.Errorf("with the verify grade: %d calls, reused %t", len(turns), whole.Reused)
	}
	if _, turns := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedOld), in, &grade); len(turns) != 1 {
		t.Errorf("the old grader made %d calls with a verify grade of the same inputs, want 1", len(turns))
	}

	// A first call that lists no fact makes no check.
	none := gradeFixture(JAdded, session)
	none.Text = "The zork train is ready.\n\nIt merges at noon today."
	nothing, turns := gradeTurns(t, stand, verify, none)
	if len(turns) != 1 || nothing.Error != "" || nothing.Verify == nil || nothing.Verify.Call != nil || nothing.Count != 0 || len(nothing.ownCalls()) != 1 {
		t.Errorf("with no listed fact: %d calls, the grade %+v", len(turns), nothing)
	}
	oldNone, _ := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedOld), none)
	if taken, turns := gradeTurns(t, stand, verify, none, &oldNone); len(turns) != 0 || !taken.Reused || !taken.Verify.FirstReused {
		t.Errorf("with an old grade that lists no fact: %d calls, the grade %+v", len(turns), taken)
	}
}

// inputs-first-verify makes inputs-first's added request, then, where it lists a fact, verify's
// check: the first call is inputs-first's, and the check verify's, byte for byte. The grade keeps
// and drops the facts as verify does, and has an input_sha256 of its own, which no other variant
// takes, and which takes no grade of another variant whole. An inputs-first added grade of the
// same inputs is its first call's grade: the run makes the check alone, and the figures count it
// as they count verify's. Where the first call lists no fact, there is no check.
func TestInputsFirstVerifyChecksTheFactsOfTheInputsFirstCall(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	stand := modeltest.New(t, verifyRules(t)...)
	session := changeSessions(t)["question/comment:c2@20"]
	in := gradeFixture(JAdded, session)
	both := standGrader(t, stand, InputsFull, AddedInputsFirstVerify)
	grade, turns := gradeTurns(t, stand, both, in)
	first, firstTurns := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedInputsFirst), in)
	verified, verifyTurns := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedVerify), in)
	old, _ := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedOld), in)
	if len(turns) != 2 || len(firstTurns) != 1 || len(verifyTurns) != 2 {
		t.Fatalf("inputs-first-verify made %d calls, inputs-first %d, verify %d; want 2, 1 and 2", len(turns), len(firstTurns), len(verifyTurns))
	}
	if turns[0].system(t) != firstTurns[0].system(t) || turns[0].user(t) != firstTurns[0].user(t) ||
		!strings.HasSuffix(turns[0].user(t), tag("original_text", goldenText)+tag("new_text", fixtureNewText)) {
		t.Errorf("the first call is not inputs-first's added call:\n%s", turns[0].user(t))
	}
	if turns[1].system(t) != verifyTurns[1].system(t) || turns[1].user(t) != verifyTurns[1].user(t) ||
		!sameJSON(t, turns[1].OutputConfig.Format.Schema, verifyTurns[1].OutputConfig.Format.Schema) || !strings.Contains(turns[1].system(t), markCheck) {
		t.Errorf("the second call is not verify's check:\n%s", turns[1].user(t))
	}
	v := grade.Verify
	if grade.Error != "" || v == nil || grade.AddedGrader != AddedInputsFirstVerify || grade.Reused {
		t.Fatalf("the grade is %+v", grade)
	}
	if !slices.Equal(v.Listed, listedFacts) || !slices.Equal(grade.Items, verified.Items) || !slices.Equal(v.Held, verified.Verify.Held) ||
		!slices.Equal(v.NotFound, verified.Verify.NotFound) || grade.Count != 2 {
		t.Errorf("listed %+v, kept %+v, held %+v; want verify's sort of the four facts", v.Listed, grade.Items, v.Held)
	}
	digests := []string{old.InputSHA256, first.InputSHA256, verified.InputSHA256}
	if v.FirstSHA256 != inputsFirstAddedSHA256 || first.InputSHA256 != inputsFirstAddedSHA256 || slices.Contains(digests, grade.InputSHA256) ||
		v.Call == nil || v.FirstReused || len(grade.ownCalls()) != 2 {
		t.Errorf("the grade's input_sha256 %s, first %s, among the other variants' %v; its check %+v and %d calls", grade.InputSHA256, v.FirstSHA256, digests, v.Call, len(grade.ownCalls()))
	}

	// An inputs-first grade of the same inputs is the first call: the run makes the check alone.
	again, turns := gradeTurns(t, stand, both, in, &first)
	if len(turns) != 1 || !strings.Contains(turns[0].system(t), markCheck) || again.Reused || !again.Verify.FirstReused || again.Count != 2 ||
		again.InputSHA256 != grade.InputSHA256 || again.AddedGrader != AddedInputsFirstVerify || len(again.ownCalls()) != 1 || again.ownCalls()[0] != *again.Verify.Call {
		t.Errorf("with the inputs-first grade: %d calls, the grade %+v", len(turns), again)
	}
	line := CaseResult{CaseID: "question/comment:q1@11", Group: GroupQuestion, SplitGroup: "TREE-1", Grades: map[Judgement]*Grade{JAdded: &again}}
	if f := computeFigures(RunParams{Groups: []string{GroupQuestion}}, []CaseResult{line}, 1, 1, 10, cfg); f.Run.ReusedFirstCalls != 1 || f.Run.ReusedGrades != 0 {
		t.Errorf("the figures count %d first calls and %d grades taken, want 1 and 0", f.Run.ReusedFirstCalls, f.Run.ReusedGrades)
	}
	// Its own grade is taken whole. The old and the verify grades are not taken, as first call or
	// whole; and neither inputs-first nor verify takes its grade.
	if whole, turns := gradeTurns(t, stand, both, in, &grade); len(turns) != 0 || !whole.Reused || len(whole.ownCalls()) != 0 {
		t.Errorf("with its own grade: %d calls, reused %t", len(turns), whole.Reused)
	}
	if _, turns := gradeTurns(t, stand, both, in, &old, &verified); len(turns) != 2 {
		t.Errorf("with the old and the verify grades: %d calls, want 2", len(turns))
	}
	if _, turns := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedInputsFirst), in, &grade); len(turns) != 1 {
		t.Errorf("inputs-first made %d calls with an inputs-first-verify grade, want 1", len(turns))
	}
	if _, turns := gradeTurns(t, stand, standGrader(t, stand, InputsFull, AddedVerify), in, &grade); len(turns) != 2 {
		t.Errorf("verify made %d calls with an inputs-first-verify grade, want 2", len(turns))
	}

	// A first call that lists no fact makes no check.
	none := gradeFixture(JAdded, session)
	none.Text = "The zork train is ready.\n\nIt merges at noon today."
	nothing, turns := gradeTurns(t, stand, both, none)
	if len(turns) != 1 || !strings.HasSuffix(turns[0].user(t), tag("new_text", none.Text)) || nothing.Error != "" || nothing.Verify == nil ||
		nothing.Verify.Call != nil || nothing.Count != 0 || len(nothing.ownCalls()) != 1 {
		t.Errorf("with no listed fact: %d calls, the grade %+v", len(turns), nothing)
	}
}

// grader-test --added-grader verify and inputs-first-verify check each added output's listed
// facts, say the variant in the report and in grader-test.json, and count the check's call in the
// cost. Under inputs-first-verify, lost and added send the new text last; under verify, neither
// does. Another variant name is refused before any call.
func TestGraderTestTakesTheAddedGrader(t *testing.T) {
	build, sessions := writeFixture(t)
	const output = "The zork train is the queue of pull requests."
	for _, variant := range []string{AddedVerify, AddedInputsFirstVerify} {
		stand := modeltest.New(t, append([]modeltest.Rule{
			{Contains: markCheck, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"facts": []map[string]any{{"1_number": "1", "2_input_words": "", "3_held": false}}})}},
			{Contains: markAdded, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"added": []map[string]string{{"1_quote": "the queue of pull requests", "2_fact": "The zork train is the queue."}}})}},
		}, allJudgementRules(t)...)...)
		out := t.TempDir()
		code, stdout, stderr := runHarness("grader-test", "--planted", writePlanted(t), "--cases", build, "--sessions", sessions, "--out", out,
			"--added-grader", variant, "--gateway", stand.URL, "--gateway-key-file", keyFile(t))
		if code != 0 {
			t.Fatalf("grader-test --added-grader %s = %d:\n%s", variant, code, stderr)
		}
		if !strings.Contains(stdout, "The added grader is "+variant+" (--added-grader).") || !strings.Contains(stdout, "| added-wrong | added | wrong | lists \"zork\" | 1 listed, 1 kept after the check | true |") {
			t.Errorf("%s: the report:\n%s", variant, stdout)
		}
		if n := len(requestsOf(stand, markCheck)); n != 2 {
			t.Errorf("%s: %d checks, want one for each added output", variant, n)
		}
		graded := 0
		for _, r := range stand.Requests() {
			if strings.Contains(r.Text, markLost) || strings.Contains(r.Text, markAdded) {
				graded++
				if last := strings.HasSuffix(decodeTurn(t, r).user(t), tag("new_text", output)); last != inputsFirstLayout(variant) {
					t.Errorf("%s: a lost or added call sends the new text last: %t", variant, last)
				}
			}
		}
		if graded != 4 {
			t.Errorf("%s: %d lost and added calls, want 4", variant, graded)
		}
		data, err := os.ReadFile(filepath.Join(out, "grader-test.json"))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Added   string          `json:"added_grader"`
			Results []PlantedResult `json:"results"`
		}
		if err := json.Unmarshal(data, &record); err != nil || record.Added != variant {
			t.Errorf("%s: grader-test.json records the added grader %q (%v)", variant, record.Added, err)
		}
		var calls, checks int
		for _, r := range record.Results {
			calls += len(r.Grade.ownCalls())
			if r.Grade.Verify != nil && r.Grade.Verify.Call != nil {
				checks++
			}
		}
		if calls != len(stand.Requests()) || checks != 2 {
			t.Errorf("%s: the results hold %d calls (%d checks), the stand-in got %d", variant, calls, checks, len(stand.Requests()))
		}
	}
	stand := modeltest.New(t, allJudgementRules(t)...)
	if code, _, stderr := runHarness("grader-test", "--planted", writePlanted(t), "--cases", build, "--sessions", sessions, "--out", t.TempDir(),
		"--added-grader", "newest", "--gateway", stand.URL, "--gateway-key-file", keyFile(t)); code != 2 || !strings.Contains(stderr, "--added-grader is old, inputs-first, verify, inputs-first-verify") {
		t.Errorf("grader-test --added-grader newest = %d:\n%s", code, stderr)
	}
	if n := len(stand.Requests()); n != 0 {
		t.Errorf("the refused test made %d calls", n)
	}
}
