package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
)

// wireBlock is one text block of an Anthropic request, with its cache mark.
type wireBlock struct {
	Text         string         `json:"text"`
	CacheControl map[string]any `json:"cache_control"`
}

// wireTurn is the part of an Anthropic request that a test of its cache layout reads.
type wireTurn struct {
	System   []wireBlock `json:"system"`
	Messages []struct {
		Role    string      `json:"role"`
		Content []wireBlock `json:"content"`
	} `json:"messages"`
	OutputConfig struct {
		Format struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	} `json:"output_config"`
	marks int
}

// fiveMinuteMark is the cache mark of a call's Shared end.
var fiveMinuteMark = map[string]any{"type": "ephemeral", "ttl": "5m"}

func decodeTurn(t *testing.T, r modeltest.Request) wireTurn {
	t.Helper()
	var w wireTurn
	if err := json.Unmarshal(r.Body, &w); err != nil {
		t.Fatalf("the request is no Anthropic request: %v\n%s", err, r.Body)
	}
	w.marks = strings.Count(string(r.Body), `"cache_control"`)
	return w
}

// blocks are the text blocks of the request's one user turn.
func (w wireTurn) blocks(t *testing.T) []wireBlock {
	t.Helper()
	if len(w.Messages) != 1 || w.Messages[0].Role != "user" {
		t.Fatalf("the request's messages are %+v, want the one user turn", w.Messages)
	}
	return w.Messages[0].Content
}

// request is the one request of stand that holds each of texts.
func request(t *testing.T, stand *modeltest.Model, texts ...string) modeltest.Request {
	t.Helper()
	var found []modeltest.Request
	for _, r := range stand.Requests() {
		all := true
		for _, s := range texts {
			all = all && strings.Contains(r.Text, s)
		}
		if all {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d requests hold %q, want 1", len(found), texts)
	}
	return found[0]
}

// A grading call sends the system prompt every judgement shares, then the case's parts that it
// reads (the question, the record, the session part), each a block of its own, with the one cache
// mark, of five minutes, on the last of them; then the judgement's own prompt and its subject,
// unmarked. The provider caches the system prompt and those parts as one prefix. Two judgements
// of a case that read the same parts send that prefix byte for byte, with the one schema every
// judgement shares, so the second reads it from the cache. The phrases judgement sends its own
// prompt as its prefix and its candidates after it; the fixture's one phrases call carries no
// mark, since no other call of the run sends that prompt. The fresh reader's call carries no mark.
func TestAGradingCallSendsTheCasesPartsFirstMarkedForTheCache(t *testing.T) {
	build, sessions := writeFixture(t)
	stand := modeltest.New(t, graderRules(t)...)
	out := t.TempDir()
	code, _, stderr := runHarness("score", "--cases", build, "--sessions", sessions, "--out", out, "--grader-inputs", "full",
		"--gateway", stand.URL, "--gateway-key-file", keyFile(t), "--draws", "50", "--exposure-log", filepath.Join(t.TempDir(), "exposure.log"))
	if code != 0 {
		t.Fatalf("score = %d:\n%s", code, stderr)
	}
	system, err := render("grader", promptData{})
	if err != nil {
		t.Fatal(err)
	}
	// The question case's undefined and answered calls, both reading every part of the case.
	undefined := decodeTurn(t, request(t, stand, markUndefined, "What is the zork train?"))
	answered := decodeTurn(t, request(t, stand, markAnswered, "What is the zork train?"))
	for name, w := range map[string]wireTurn{"undefined": undefined, "answered": answered} {
		if len(w.System) != 1 || strings.TrimSpace(w.System[0].Text) != strings.TrimSpace(system) || w.System[0].CacheControl != nil {
			t.Errorf("%s: the system prompt is %+v, want the grader's common prompt, unmarked", name, w.System)
		}
		b := w.blocks(t)
		if len(b) != 4 || !strings.HasPrefix(b[0].Text, "<question>") || !strings.HasPrefix(b[1].Text, "<record>") || !strings.HasPrefix(b[2].Text, "<writer_session>") {
			t.Fatalf("%s: the user turn's blocks are %+v, want the question, the record, the session part and the judgement", name, b)
		}
		for i, block := range b {
			want := map[string]any(nil)
			if i == 2 {
				want = fiveMinuteMark
			}
			if !reflect.DeepEqual(block.CacheControl, want) {
				t.Errorf("%s: block %d carries the mark %v, want %v", name, i, block.CacheControl, want)
			}
		}
		if w.marks != 1 {
			t.Errorf("%s: the request carries %d cache marks, want 1", name, w.marks)
		}
		if !strings.Contains(string(w.OutputConfig.Format.Schema), `"anyOf"`) {
			t.Errorf("%s: the reply schema is %s, want the judgements' common one", name, w.OutputConfig.Format.Schema)
		}
	}
	if u, a := undefined.blocks(t), answered.blocks(t); !reflect.DeepEqual(u[:3], a[:3]) || string(undefined.OutputConfig.Format.Schema) != string(answered.OutputConfig.Format.Schema) {
		t.Error("the case's two judgements send different prefixes")
	}
	if !strings.Contains(undefined.blocks(t)[3].Text, markUndefined) || !strings.Contains(undefined.blocks(t)[3].Text, "<text>") {
		t.Errorf("undefined's last block is %q, want its prompt and its text", undefined.blocks(t)[3].Text)
	}

	phrases := decodeTurn(t, request(t, stand, markPhrases, "This names the problem."))
	if b := phrases.blocks(t); len(b) != 2 || !strings.Contains(b[0].Text, markPhrases) || b[0].CacheControl != nil ||
		!strings.HasPrefix(b[1].Text, "<candidates>") || b[1].CacheControl != nil || phrases.marks != 0 {
		t.Errorf("the phrases call's blocks are %+v, want its prompt, then its candidates, with no mark", b)
	}
	for _, r := range stand.Requests() {
		if strings.Contains(r.Text, markReader) && strings.Contains(string(r.Body), "cache_control") {
			t.Error("the fresh reader's call carries a cache mark")
		}
	}
}

// The writer's call sends Dispatch's record and its session part first, then its own text and
// Dispatch's message. Change 2 calls the writer once for a case, so its call carries no mark.
// Change 3 calls it once in each round of review: each of its calls marks the session part, so
// the next round reads the record and the session from the cache.
func TestTheWriterMarksTheRecordAndSessionWhereItCallsAgain(t *testing.T) {
	layout := func(t *testing.T, w wireTurn) []wireBlock {
		t.Helper()
		b := w.blocks(t)
		if len(b) != 3 || !strings.HasPrefix(b[0].Text, "<record>") || !strings.HasPrefix(b[1].Text, "<writer_session>") ||
			!strings.Contains(b[2].Text, "<your_text>") || !strings.Contains(b[2].Text, "<dispatch_message>") {
			t.Fatalf("the writer's blocks are %+v, want the record, the session part, then its text and the message", b)
		}
		return b
	}
	run, stand := changeRig(t, []string{changeRules}, newFakeDispatch(t))
	apply(t, run, "question/comment:c2@20")
	w := decodeTurn(t, stand.Requests()[0])
	if b := layout(t, w); b[0].CacheControl != nil || b[1].CacheControl != nil || b[2].CacheControl != nil || w.marks != 0 {
		t.Errorf("change 2's writer marks: %v, %v, %v (%d in all), want none", b[0].CacheControl, b[1].CacheControl, b[2].CacheControl, w.marks)
	}

	finding := []reviewer.Finding{{RuleSet: reviewer.RuleNoslop, Quote: "spin up", Why: "A phrasal verb.", Fix: "start"}}
	found := draftAnswer{Findings: finding, Rewrite: &wording{Body: "We start the node."}}
	run, stand = changeRig(t, []string{changeFindings}, newFakeDispatch(t, found, found))
	apply(t, run, "question/comment:c2@20")
	if len(stand.Requests()) != 2 {
		t.Fatalf("change 3 made %d writer calls, want one in each of its two rounds", len(stand.Requests()))
	}
	for i, r := range stand.Requests() {
		w := decodeTurn(t, r)
		if b := layout(t, w); b[0].CacheControl != nil || !reflect.DeepEqual(b[1].CacheControl, fiveMinuteMark) || b[2].CacheControl != nil || w.marks != 1 {
			t.Errorf("change 3's writer call %d marks: %v, %v, %v (%d in all), want the session part's alone", i, b[0].CacheControl, b[1].CacheControl, b[2].CacheControl, w.marks)
		}
	}
}

// The writer marks the calls of a prompt where the lanes that make the change of a case can call
// it two or more times in all: change 3, once in each round of review, with two rounds or more.
// Changes 2, 5, 6 and 7a, change 4 (which plays no writer) and change 3 with one round call each
// prompt at most once, so a run of one model leaves them unmarked; a lockstep of two models marks
// change 7a's and change 3's.
func TestOnlyAChangeThatCallsTheWriterAgainMarksItsCalls(t *testing.T) {
	for _, c := range []struct {
		changes []string
		rounds  int
		lanes   int
		want    []string
	}{
		{[]string{changeRules}, 2, 1, nil},
		{[]string{changeFindings}, 2, 1, []string{promptFindings}},
		{[]string{changeFindings}, 3, 1, []string{promptFindings}},
		{[]string{changeFindings}, 1, 1, nil},
		{[]string{changeFindings}, 1, 2, []string{promptFindings}},
		{[]string{changeReviewRewrite}, 2, 1, nil},
		{[]string{changeReviewRewrite}, 2, 2, nil},
		{[]string{changeRewrite}, 2, 1, nil},
		{[]string{changeDefine}, 2, 1, nil},
		{[]string{changeCheck}, 2, 1, nil},
		{[]string{changeCheck}, 2, 2, []string{promptRewrite}},
		{[]string{changeRules, changeFindings, changeRewrite}, 2, 1, []string{promptFindings}},
		{[]string{changeRules, changeCheck}, 2, 1, []string{promptRewrite}},
	} {
		plan, err := planOf(strings.Join(c.changes, "+"), c.changes)
		if err != nil {
			t.Fatal(err)
		}
		w := &Writer{Calls: plan.writerCalls(c.rounds)}
		var got []string
		for _, p := range []string{promptRewrite, promptFindings, promptAnswer} {
			if w.marked(p, c.lanes) {
				got = append(got, p)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("changes %v with %d rounds and %d lanes mark %v, want %v", c.changes, c.rounds, c.lanes, got, c.want)
		}
	}
}

// A reply of the judgements' common schema in another judgement's shape is asked again at once,
// and the grade and the figures count it.
func TestAReplyInAnotherJudgementsShapeIsAskedAgain(t *testing.T) {
	build, sessions := writeFixture(t)
	other := modeltest.Rule{Once: true, Contains: markHarder, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{
		"1_asked": "x", "2_given": "y", "3_restates_question": false, "4_points_elsewhere": false,
		"5_other_question": false, "6_contradicts_record": false, "7_reason": "z", "8_answers": true})}}
	stand := modeltest.New(t, append([]modeltest.Rule{other}, graderRules(t)...)...)
	out := t.TempDir()
	code, _, stderr := runHarness("score", "--cases", build, "--sessions", sessions, "--out", out,
		"--gateway", stand.URL, "--gateway-key-file", keyFile(t), "--draws", "50", "--exposure-log", filepath.Join(t.TempDir(), "exposure.log"))
	if code != 0 {
		t.Fatalf("score = %d:\n%s", code, stderr)
	}
	results, err := readResults(filepath.Join(out, "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	g := results["plain/ask:p3@30"].Grades[JHarder]
	if g == nil || g.Error != "" || g.Verdict == nil || *g.Verdict || g.Call == nil || g.Call.OtherShape != 1 || g.Call.Attempts != 2 {
		t.Fatalf("the harder grade is %+v, want its verdict after one reply in another shape", g)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range readFigures(t, out).Models {
		if u.Model == cfg.Models.Roles.Grader.ID && u.OtherShape != 1 {
			t.Errorf("the figures count %d replies in another shape, want 1", u.OtherShape)
		}
	}
}

// The grader marks a prefix only where the run's plan holds two or more calls of it. Two
// judgements of a case that send the same prefix, planned together, both carry the mark, and
// the second waits until the first has its reply, so it reads the prefix the first wrote to the
// cache. Graded with no plan, each call sends its prefix alone: neither carries a mark, and
// neither waits for the other.
func TestTheJudgementsOfOnePrefixFollowTheFirst(t *testing.T) {
	const delay = 300 * time.Millisecond
	var mu sync.Mutex
	var arrived []time.Time
	at := func(modeltest.Request) bool {
		mu.Lock()
		arrived = append(arrived, time.Now())
		mu.Unlock()
		return true
	}
	stand := modeltest.New(t,
		modeltest.Rule{Contains: markLost, Match: at, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"lost": []any{}}), Delay: delay}},
		modeltest.Rule{Contains: markAdded, Match: at, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"added": []any{}}), Delay: delay}},
	)
	grader := standInGrader(t, stand, "full")
	base := GradeInput{Question: "What is the zork train?", Original: "The zork train is ready.", Text: "It is ready.", Record: "<record>\nThe zork train is the queue.\n</record>\n"}
	var ins []GradeInput
	for _, j := range []Judgement{JLost, JAdded} {
		in := base
		in.Judgement = j
		ins = append(ins, in)
	}
	gradeAll := func(g *Grader) {
		t.Helper()
		var wg sync.WaitGroup
		grades := make([]Grade, len(ins))
		for i, in := range ins {
			wg.Add(1)
			go func() {
				defer wg.Done()
				grades[i] = g.Grade(context.Background(), in)
			}()
		}
		wg.Wait()
		for i, g := range grades {
			if g.Error != "" {
				t.Fatalf("grade %d: %s", i, g.Error)
			}
		}
	}
	grader.Plan(ins, nil)
	gradeAll(grader)
	if len(arrived) != 2 {
		t.Fatalf("%d calls, want 2", len(arrived))
	}
	if gap := arrived[1].Sub(arrived[0]); gap < delay {
		t.Errorf("the second call came %s after the first, want after the first's reply (%s)", gap, delay)
	}
	for i, r := range stand.Requests() {
		if w := decodeTurn(t, r); w.marks != 1 {
			t.Errorf("planned call %d carries %d cache marks, want 1", i, w.marks)
		}
	}

	mu.Lock()
	arrived = nil
	mu.Unlock()
	before := len(stand.Requests())
	gradeAll(standInGrader(t, stand, "full"))
	if len(arrived) != 2 {
		t.Fatalf("%d calls, want 2", len(arrived))
	}
	if gap := arrived[1].Sub(arrived[0]); gap >= delay {
		t.Errorf("an unplanned call came %s after the other, want at once: an unmarked call waits for none", gap)
	}
	for i, r := range stand.Requests()[before:] {
		if w := decodeTurn(t, r); w.marks != 0 {
			t.Errorf("unplanned call %d carries %d cache marks, want none", i, w.marks)
		}
	}
}

// standInGrader is the grader of candidates.yaml at the stand-in, with no fallback, reading the
// context the input modes give.
func standInGrader(t *testing.T, stand *modeltest.Model, inputs string) *Grader {
	t.Helper()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	id := cfg.Models.Roles.Grader.ID
	model, err := claude.New(stand.URL, keyFile(t), id, "", cfg.Prices.Prices[id].agentloop(), &llm.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	modes, err := ParseGraderInputs(inputs)
	if err != nil {
		t.Fatal(err)
	}
	return NewGrader(model, nil, modes)
}

// Under the final input modes, each judgement of the question case sends a prefix of its own,
// and so does the answer case's answer_start and undefined: those calls carry no mark. The
// answer case's answered and first_paragraph read the same parts (the question and the record):
// both carry the mark. Every call sends the same blocks in the same order either way, and the
// figures count the grader's marked and unmarked calls.
func TestAPrefixThatOneCallSendsGoesUnmarked(t *testing.T) {
	build, sessions := writeFixture(t)
	stand := modeltest.New(t, allJudgementRules(t)...)
	out := t.TempDir()
	code, stdout, stderr := runHarness("score", "--cases", build, "--sessions", sessions, "--out", out,
		"--gateway", stand.URL, "--gateway-key-file", keyFile(t), "--draws", "50", "--exposure-log", filepath.Join(t.TempDir(), "exposure.log"))
	if code != 0 {
		t.Fatalf("score = %d:\n%s", code, stderr)
	}
	const firstParagraph = "read only the first paragraph of the text"
	for _, c := range []struct {
		name   string
		texts  []string
		blocks []string
		marked bool
	}{
		{"the question case's answered", []string{markAnswered, "What is the zork train?"}, []string{"<question>", "<record>"}, false},
		{"the question case's undefined", []string{markUndefined, "What is the zork train?"}, []string{"<question>", "<record>", "<writer_session>"}, false},
		{"the answer case's answer_start", []string{markStart}, []string{"<question>"}, false},
		{"the answer case's undefined", []string{markUndefined, "What is the blocker?"}, []string{"<question>", "<record>", "<writer_session>"}, false},
		{"the answer case's answered", []string{markAnswered, "What is the blocker?", "from the text alone"}, []string{"<question>", "<record>"}, true},
		{"the answer case's first_paragraph", []string{markAnswered, "What is the blocker?", firstParagraph}, []string{"<question>", "<record>"}, true},
	} {
		w := decodeTurn(t, request(t, stand, c.texts...))
		b := w.blocks(t)
		if len(b) != len(c.blocks)+1 {
			t.Fatalf("%s: the user turn's blocks are %+v, want %v and the judgement", c.name, b, c.blocks)
		}
		for i, start := range c.blocks {
			if !strings.HasPrefix(b[i].Text, start) {
				t.Errorf("%s: block %d starts %.20q, want %s", c.name, i, b[i].Text, start)
			}
			want := map[string]any(nil)
			if c.marked && i == len(c.blocks)-1 {
				want = fiveMinuteMark
			}
			if !reflect.DeepEqual(b[i].CacheControl, want) {
				t.Errorf("%s: block %d carries the mark %v, want %v", c.name, i, b[i].CacheControl, want)
			}
		}
		wantMarks := 0
		if c.marked {
			wantMarks = 1
		}
		if w.marks != wantMarks {
			t.Errorf("%s: the request carries %d cache marks, want %d", c.name, w.marks, wantMarks)
		}
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	results, err := readResults(filepath.Join(out, "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// The plain and the phrase cases send one record in their undefined calls, and the run counts
	// a prefix over every case that plans it, so whether those two go marked depends on the
	// order the cases plan in. The question and the answer cases share no prefix with another.
	for id, want := range map[string][]Judgement{"question/comment:q1@11": nil, "answer/comment:a2@20": {JAnswered, JFirstParagraph}} {
		for j, g := range results[id].Grades {
			if g.Call == nil {
				continue
			}
			wantMarked, wantUnmarked := 0, 1
			if slices.Contains(want, j) {
				wantMarked, wantUnmarked = 1, 0
			}
			if g.Call.Marked != wantMarked || g.Call.Unmarked != wantUnmarked {
				t.Errorf("%s's %s grade holds %d marked and %d unmarked calls, want %d and %d", id, j, g.Call.Marked, g.Call.Unmarked, wantMarked, wantUnmarked)
			}
		}
	}
	marked, unmarked := 0, 0
	for _, r := range results {
		for _, g := range r.Grades {
			if g.Call != nil {
				marked += g.Call.Marked
				unmarked += g.Call.Unmarked
			}
		}
	}
	for _, u := range readFigures(t, out).Models {
		if u.Model == cfg.Models.Roles.Grader.ID && (u.Marked != marked || u.Unmarked != unmarked) {
			t.Errorf("the figures count %d marked and %d unmarked grader calls, want %d and %d", u.Marked, u.Unmarked, marked, unmarked)
		}
		if u.Model == cfg.Models.Roles.Reader.ID && (u.Marked != 0 || u.Unmarked != u.Calls) {
			t.Errorf("the figures count %d marked and %d unmarked reader calls of %d, want every one unmarked", u.Marked, u.Unmarked, u.Calls)
		}
	}
	if !strings.Contains(stdout, "| Calls | Marked | Unmarked |") {
		t.Errorf("scores.md's model table has no marked and unmarked columns:\n%s", stdout)
	}
}

// The phrases prompt is the same for every text, so its count spans the run: with two texts
// that have candidates planned, each text's call carries the mark; with one, its call carries
// none.
func TestThePhrasesPromptIsMarkedWhereTheRunSendsItAgain(t *testing.T) {
	stand := modeltest.New(t, modeltest.Rule{Contains: markPhrases, Reply: modeltest.Reply{Text: asJSON(t, map[string]any{"candidates": []any{}})}})
	texts := []string{"This names the problem.", "The test shows the fault."}
	var ins []GradeInput
	for _, text := range texts {
		ins = append(ins, GradeInput{Judgement: JPhrases, Text: text})
	}
	grader := standInGrader(t, stand, "final")
	grader.Plan(ins, nil)
	for _, in := range ins {
		if g := grader.Grade(context.Background(), in); g.Error != "" || g.Call == nil || g.Call.Marked != 1 || g.Call.Unmarked != 0 {
			t.Errorf("%q with two texts planned: %+v, want one marked call", in.Text, g)
		}
	}
	alone := standInGrader(t, stand, "final")
	alone.Plan(ins[:1], nil)
	before := len(stand.Requests())
	if g := alone.Grade(context.Background(), ins[0]); g.Error != "" || g.Call == nil || g.Call.Marked != 0 || g.Call.Unmarked != 1 {
		t.Errorf("%q alone: %+v, want one unmarked call", ins[0].Text, g)
	}
	if w := decodeTurn(t, stand.Requests()[before]); w.marks != 0 {
		t.Errorf("the phrases call of a text alone carries %d cache marks, want none", w.marks)
	}
}

// A grade's inputs do not hold the cache mark: the request digest, which a grade records as its
// input_sha256 and which --grades-from matches, is the same with the mark set and unset.
func TestTheRequestDigestDoesNotHoldTheMark(t *testing.T) {
	stand := modeltest.New(t)
	grader := standInGrader(t, stand, "full")
	c, err := grader.prepare(GradeInput{Judgement: JUndefined, Question: "What is the zork train?", Text: "It is ready.", Record: "<record>\nThe zork train is the queue.\n</record>\n"})
	if err != nil {
		t.Fatal(err)
	}
	unmarked := grader.digest(c)
	c.req.Mark = true
	if marked := grader.digest(c); marked != unmarked {
		t.Errorf("the digest is %s with the mark and %s without it, want one digest", marked, unmarked)
	}
}
