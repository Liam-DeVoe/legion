package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
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
// prompt as its marked prefix and its candidates after it; the fresh reader's call carries no
// mark.
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
	if b := phrases.blocks(t); len(b) != 2 || !strings.Contains(b[0].Text, markPhrases) || !reflect.DeepEqual(b[0].CacheControl, fiveMinuteMark) ||
		!strings.HasPrefix(b[1].Text, "<candidates>") || b[1].CacheControl != nil || phrases.marks != 1 {
		t.Errorf("the phrases call's blocks are %+v, want its prompt marked, then its candidates", b)
	}
	for _, r := range stand.Requests() {
		if strings.Contains(r.Text, markReader) && strings.Contains(string(r.Body), "cache_control") {
			t.Error("the fresh reader's call carries a cache mark")
		}
	}
}

// The writer's call sends Dispatch's record and its session part first, the session part with
// the one cache mark, then its own text and Dispatch's message, unmarked: change 3's next round
// with the same prompt reads the record and the session from the cache.
func TestTheWritersCallSendsTheRecordAndSessionFirstMarkedForTheCache(t *testing.T) {
	fake := newFakeDispatch(t)
	run, stand := changeRig(t, []string{changeRules}, fake)
	apply(t, run, "question/comment:c2@20")
	w := decodeTurn(t, stand.Requests()[0])
	b := w.blocks(t)
	if len(b) != 3 || !strings.HasPrefix(b[0].Text, "<record>") || !strings.HasPrefix(b[1].Text, "<writer_session>") ||
		!strings.Contains(b[2].Text, "<your_text>") || !strings.Contains(b[2].Text, "<dispatch_message>") {
		t.Fatalf("the writer's blocks are %+v, want the record, the session part, then its text and the message", b)
	}
	if b[0].CacheControl != nil || !reflect.DeepEqual(b[1].CacheControl, fiveMinuteMark) || b[2].CacheControl != nil || w.marks != 1 {
		t.Errorf("the writer's marks: %v, %v, %v (%d in all), want the session part's alone", b[0].CacheControl, b[1].CacheControl, b[2].CacheControl, w.marks)
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

// Two judgements of a case that send the same prefix do not start together: the second waits
// until the first has its reply, so it reads the prefix the first wrote to the cache.
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
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	id := cfg.Models.Roles.Grader.ID
	model, err := claude.New(stand.URL, keyFile(t), id, "", cfg.Prices.Prices[id].agentloop(), &llm.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := ParseGraderInputs("full")
	if err != nil {
		t.Fatal(err)
	}
	grader := NewGrader(model, nil, inputs)
	base := GradeInput{Question: "What is the zork train?", Original: "The zork train is ready.", Text: "It is ready.", Record: "<record>\nThe zork train is the queue.\n</record>\n"}
	var wg sync.WaitGroup
	grades := make([]Grade, 2)
	for i, j := range []Judgement{JLost, JAdded} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := base
			in.Judgement = j
			grades[i] = grader.Grade(context.Background(), in)
		}()
	}
	wg.Wait()
	for i, g := range grades {
		if g.Error != "" {
			t.Fatalf("grade %d: %s", i, g.Error)
		}
	}
	if len(arrived) != 2 {
		t.Fatalf("%d calls, want 2", len(arrived))
	}
	if gap := arrived[1].Sub(arrived[0]); gap < delay {
		t.Errorf("the second call came %s after the first, want after the first's reply (%s)", gap, delay)
	}
}
