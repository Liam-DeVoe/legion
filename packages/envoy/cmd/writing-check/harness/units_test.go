package harness

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheCandidateListAndThePriceTable(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	roles := cfg.Models.Roles
	if roles.Grader.ID != "claude-opus-5" || roles.Writer.ID != "claude-opus-5-5" || roles.Reader.ID != "claude-sonnet-4-6" {
		t.Errorf("the roles of Decision 20: %+v", roles)
	}
	if len(cfg.Models.Candidates) != 6 || len(cfg.Models.Pending) != 4 {
		t.Errorf("six candidates today and INFRA-3483's four pending: %d, %d", len(cfg.Models.Candidates), len(cfg.Models.Pending))
	}
	for _, bad := range []struct{ name, candidates string }{
		{"a role that is a candidate", strings.Replace(string(candidatesFile), "reader: {family: claude, id: claude-sonnet-4-6}", "reader: {family: claude, id: claude-opus-4-8}", 1)},
		{"a role of another family", strings.Replace(string(candidatesFile), "reader: {family: claude, id: claude-sonnet-4-6}", "reader: {family: foundry, id: grok-4.7}", 1)},
		{"the grader playing the writer", strings.Replace(string(candidatesFile), "writer: {family: claude, id: claude-opus-5-5}", "writer: {family: claude, id: claude-opus-5}", 1)},
		{"a model with no price", strings.Replace(string(candidatesFile), "id: claude-haiku-5-5", "id: claude-haiku-9", 1)},
	} {
		if _, err := parseConfig([]byte(bad.candidates), pricesFile); err == nil {
			t.Errorf("%s was let through", bad.name)
		}
	}
}

func TestPlantedOutputsAreMarked(t *testing.T) {
	yes, no := true, false
	five, minus := 5, -1
	for _, tc := range []struct {
		name  string
		p     Planted
		grade Grade
		want  bool
	}{
		{"a wrong answer the grader fails", Planted{Judgement: JAnswered, Planted: PlantedWrong}, Grade{Verdict: &no}, true},
		{"a wrong answer the grader passes", Planted{Judgement: JAnswered, Planted: PlantedWrong}, Grade{Verdict: &yes}, false},
		{"a right definition the grader passes", Planted{Judgement: JDefine, Planted: PlantedRight}, Grade{Verdict: &yes}, true},
		{"a harder text the grader finds harder", Planted{Judgement: JHarder, Planted: PlantedWrong}, Grade{Verdict: &yes}, true},
		{"a lost fact the grader names", Planted{Judgement: JLost, Planted: PlantedWrong, Marker: "noon"}, Grade{Count: 1, Items: []Item{{Quote: "at Noon", Note: "the time"}}}, true},
		{"a lost fact the grader misses", Planted{Judgement: JLost, Planted: PlantedWrong, Marker: "noon"}, Grade{Count: 1, Items: []Item{{Quote: "the date"}}}, false},
		{"a right text the grader lists nothing in", Planted{Judgement: JPhrases, Planted: PlantedRight}, Grade{}, true},
		{"a right text the grader lists a phrase in", Planted{Judgement: JPhrases, Planted: PlantedRight}, Grade{Count: 1}, false},
		{"an answer start near the planted one", Planted{Judgement: JAnswerStart, Planted: PlantedWrong, WordsBefore: &five}, Grade{Found: &yes, WordsBefore: &[]int{8}[0]}, true},
		{"an answer start far from it", Planted{Judgement: JAnswerStart, Planted: PlantedWrong, WordsBefore: &five}, Grade{Found: &yes, WordsBefore: &[]int{40}[0]}, false},
		{"an answer start not found in the text", Planted{Judgement: JAnswerStart, Planted: PlantedRight, WordsBefore: &five}, Grade{Found: &yes, WordsBefore: &minus}, false},
		{"a grade that failed", Planted{Judgement: JAnswered, Planted: PlantedRight}, Grade{Verdict: &yes, Error: "the call failed"}, false},
	} {
		if got := tc.p.correct(tc.grade); got != tc.want {
			t.Errorf("%s: correct = %t, want %t", tc.name, got, tc.want)
		}
	}
	if err := (Planted{ID: "x", Judgement: JLost, Planted: PlantedWrong, Output: "text"}).check(); err == nil {
		t.Error("a wrong list output with no marker was let through")
	}
}

func TestProbeRows(t *testing.T) {
	m := ModelEntry{Family: "foundry", ID: "grok-4.7"}
	ok := probeCall{Status: http.StatusOK, Served: "grok-4.7", Seconds: 1}
	if row := probeRow(m, "candidate", []probeCall{ok, ok}); !row.Pass || row.OK != 2 {
		t.Errorf("two answers of the id asked for: %+v", row)
	}
	notFound := probeCall{Status: http.StatusNotFound, Reason: routeNotFound}
	if row := probeRow(m, "candidate", []probeCall{ok, notFound}); row.Pass || row.RouteNotFound != 1 {
		t.Errorf("a route_not_found passed: %+v", row)
	}
	other := probeCall{Status: http.StatusOK, Served: "grok-4.6"}
	if row := probeRow(m, "candidate", []probeCall{ok, other}); row.Pass {
		t.Errorf("an answer by another model passed: %+v", row)
	}
	busy := probeCall{Status: http.StatusTooManyRequests, Error: "slow down"}
	if row := probeRow(m, "candidate", []probeCall{ok, busy}); !row.Pass {
		t.Errorf("a 429 among answers failed the probe: %+v", row)
	}
	for _, tc := range []struct {
		requested, served string
		want              bool
	}{
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001", true},
		{"claude-opus-5", "claude-opus-5-20261101", true},
		{"claude-opus-5", "anthropic/claude-opus-5", true},
		{"Kimi-K3.1-Pro", "kimi-k3-1-pro", true},
		{"claude-opus-5", "claude-sonnet-5-5", false},
	} {
		if got := servedMatches(tc.requested, tc.served); got != tc.want {
			t.Errorf("servedMatches(%q, %q) = %t", tc.requested, tc.served, got)
		}
	}
}

// TestAReplysOwnChecksDecide: a check the grader marks true fails the verdict whatever the
// verdict field says, and an item the grader marks defined is no finding.
func TestAReplysOwnChecksDecide(t *testing.T) {
	var g Grade
	reply := `{"1_asked":"what it means","2_given":"the words again","3_restates_question":true,"4_points_elsewhere":false,` +
		`"5_other_question":false,"6_contradicts_record":false,"7_reason":"r","8_answers":true}`
	if err := judgementSpecs[JAnswered].read([]byte(reply), &g, ""); err != nil {
		t.Fatal(err)
	}
	if g.Verdict == nil || *g.Verdict || len(g.Failed) != 1 || g.Failed[0] != "restates_question" || g.Notes["given"] != "the words again" {
		t.Errorf("an answer that restates the question: %+v", g)
	}
	g = Grade{}
	reply = `{"undefined":[{"1_word":"session","2_defined_in_text":false,"3_defined_in_record":false,"4_common_meaning":true,"5_why":"w"},` +
		`{"1_word":"zork","2_defined_in_text":false,"3_defined_in_record":false,"4_common_meaning":false,"5_why":"made up"}]}`
	if err := judgementSpecs[JUndefined].read([]byte(reply), &g, ""); err != nil {
		t.Fatal(err)
	}
	if g.Count != 1 || g.Items[0].Quote != "zork" || g.Dropped != 1 {
		t.Errorf("the undefined words: %+v", g)
	}
	// A reply missing a check is no reply of the schema.
	if err := judgementSpecs[JAnswered].read([]byte(`{"7_reason":"r","8_answers":true}`), &Grade{}, ""); err == nil {
		t.Error("a reply with no checks was read")
	}
	// The schema asks for the fields in the prompt's order, the verdict last.
	schema := string(judgementSpecs[JAnswered].schema())
	if i, j := strings.Index(schema, `"1_asked"`), strings.Index(schema, `"8_answers"`); i < 0 || j < i {
		t.Errorf("the answered schema: %s", schema)
	}
}
