package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/going"
)

// score pools every repeat: each repeat's right choices add up to the count R7's rule ranks by,
// each case is counted once however many times it was asked, the accuracies divide by every
// answer, and the latencies and both fits take every answer of every repeat, not the last
// repeat's alone.
func TestScorePoolsEveryRepeat(t *testing.T) {
	closing := testCase{Words: "close DEMO-12", Action: "close_issue", NamesAction: true}
	trend := testCase{Words: "how is DEMO trending", Action: going.ActionNone, ChartWorthy: true}
	status := testCase{Words: "what changed on DEMO-12", Action: going.ActionNone}
	chose := func(c testCase, action string, confidence float64, display string, displayConfidence float64, ms int) answer {
		return answer{
			c: c, answered: true,
			action:  agentloop.Decision{Choice: action, Confidence: confidence},
			display: agentloop.Decision{Choice: display, Confidence: displayConfidence},
			latency: time.Duration(ms) * time.Millisecond,
		}
	}
	m := &measured{record: going.Record{Model: "demo-model", Repeats: 2}, repeats: [][]answer{
		{
			chose(closing, "close_issue", 0.9, going.DisplayProse, 0.9, 100),
			chose(trend, going.ActionNone, 0.6, going.DisplayChart, 0.7, 200),
			chose(status, "close_issue", 0.7, going.DisplayProse, 0.9, 300),
		},
		{
			{c: closing, fault: "timeout: no answer", latency: 400 * time.Millisecond},
			chose(trend, going.ActionNone, 0.8, going.DisplayChart, 0.9, 500),
			chose(status, going.ActionNone, 0.95, going.DisplayProse, 0.9, 600),
		},
	}}
	score(m)
	r := &m.record

	if want := []going.RepeatRight{{Named: 1, Other: 1, Total: 2}, {Named: 0, Other: 2, Total: 2}}; !slices.Equal(r.RepeatRights, want) {
		t.Errorf("the repeats' right choices are %+v, want %+v", r.RepeatRights, want)
	}
	if r.Right() != 4 {
		t.Errorf("the right choices pooled over the repeats are %d, want 4", r.Right())
	}
	if want := [][]bool{{true, true, false}, {false, true, true}}; !slices.EqualFunc(m.rights, want, slices.Equal) {
		t.Errorf("the right choices the paired test pairs are %v, want %v", m.rights, want)
	}
	if r.Cases != 3 || r.NamedCases != 1 || r.OtherCases != 2 || r.ChartWorthyCases != 1 || r.Unanswered != 1 {
		t.Errorf("the counts are %d cases, %d named, %d other, %d chart-worthy, %d unanswered; want 3, 1, 2, 1 and 1",
			r.Cases, r.NamedCases, r.OtherCases, r.ChartWorthyCases, r.Unanswered)
	}
	if r.NamedAccuracy != 0.5 || r.OtherAccuracy != 0.75 {
		t.Errorf("the accuracies are %v named and %v on the rest, want 1 of 2 and 3 of 4 answers", r.NamedAccuracy, r.OtherAccuracy)
	}
	if want := (going.Spread{Mean: 2, Min: 2, Max: 2}); r.TotalSpread != want {
		t.Errorf("the spread is %+v, want %+v", r.TotalSpread, want)
	}
	if want := []going.CaseConsistency{{Words: closing.Words, Right: 1, Of: 2}, {Words: status.Words, Right: 1, Of: 2}}; !slices.Equal(r.InconsistentCases, want) {
		t.Errorf("the cases answered both ways are %+v, want %+v", r.InconsistentCases, want)
	}
	if r.P50MS != 300 || r.P95MS != 600 {
		t.Errorf("the latencies are p50 %v and p95 %v ms, want 300 and 600 over all six calls", r.P50MS, r.P95MS)
	}
	// Over both repeats a wrong choice at 0.7 leaves 3 of 6 answers at or above 0.8, under 60%,
	// so no action fit holds; the second repeat alone would fit 0.8 on 2 of 3.
	if r.ActionThreshold != nil || r.ActionShare != 0.5 {
		t.Errorf("the action fit is %v on a share of %v, want none on 3 of 6 answers", threshold(r.ActionThreshold), r.ActionShare)
	}
	if r.ChartThreshold == nil || *r.ChartThreshold != 0.7 || r.ChartShare != 1 {
		t.Errorf("the chart fit is %v on a share of %v, want 0.7 on both chart-worthy answers", threshold(r.ChartThreshold), r.ChartShare)
	}
	for _, want := range []going.DisplayRate{
		{Label: going.LabelChartWorthy, Answered: going.DisplayChart, Count: 2, Of: 2, Rate: 1},
		{Label: going.LabelNotChartWorthy, Answered: going.DisplayUnanswered, Count: 1, Of: 4, Rate: 0.25},
	} {
		if !slices.Contains(r.DisplayRates, want) {
			t.Errorf("the display rates %+v lack %+v", r.DisplayRates, want)
		}
	}
	if r.MeetsFloors() {
		t.Error("a model right on half the named answers meets R7's floors")
	}
}

// A run the rule decides is written with the rule's model chosen, by the key that named it.
func TestARunTheRuleDecidesIsWrittenWithItsChoice(t *testing.T) {
	for _, tc := range []struct {
		name      string
		records   []going.Record
		model, by string
	}{
		{"by the right choices", []going.Record{floored("model-a", true, 103), floored("model-b", false, 104)}, "model-b", going.ChosenByRightChoices},
		{"by the display fit", []going.Record{floored("model-a", false, 104), floored("model-b", true, 104)}, "model-b", going.ChosenByDisplayFit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "decision_models.json")
			if err := conclude(io.Discard, tc.records, "a test run", "", "", out); err != nil {
				t.Fatal(err)
			}
			if chosen := chosenIn(t, out); len(chosen) != 1 || chosen[0].Model != tc.model || chosen[0].ChosenBy != tc.by || chosen[0].ChosenReason != "" {
				t.Errorf("the record chooses %+v, want %s by %s", chosen, tc.model, tc.by)
			}
		})
	}
}

// A full tie on right choices and on the display fit is written with no model chosen, and the
// tool fails, saying the architect names one; a tie-break on that record then names one of the
// level models, with the reason kept.
func TestAFullTieWaitsForTheArchitect(t *testing.T) {
	out := filepath.Join(t.TempDir(), "decision_models.json")
	tied := []going.Record{floored("model-a", true, 101, 100), floored("model-b", true, 100, 101)}
	err := conclude(io.Discard, tied, "a test run", "", "", out)
	if err == nil || !strings.Contains(err.Error(), "--tie-break") {
		t.Fatalf("a full tie concludes with %v, want an error naming --tie-break", err)
	}
	if chosen := chosenIn(t, out); len(chosen) != 0 {
		t.Fatalf("a full tie is written with %+v chosen, want none", chosen)
	}
	written, err := readRecords(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := conclude(io.Discard, written, "a test run", "model-b", "the cheaper of two level models", out); err != nil {
		t.Fatal(err)
	}
	if chosen := chosenIn(t, out); len(chosen) != 1 || chosen[0].Model != "model-b" || chosen[0].ChosenBy != going.ChosenByPerson || chosen[0].ChosenReason != "the cheaper of two level models" {
		t.Errorf("the broken tie chooses %+v, want model-b by a person, with the reason", chosen)
	}
}

// Nothing is written where no model meets the floors or a tie-break is refused: on a run the
// rule decides, for a model outside the tie, or with no reason.
func TestARunWithNoChoiceToRecordWritesNothing(t *testing.T) {
	level := []going.Record{floored("model-a", true, 101), floored("model-b", true, 101)}
	unfloored := floored("model-a", true, 117)
	unfloored.P95MS = going.P95CeilingMS
	for _, tc := range []struct {
		name          string
		records       []going.Record
		model, reason string
	}{
		{"no model meeting the floors", []going.Record{unfloored}, "", ""},
		{"a tie-break on a run the right choices decide", []going.Record{floored("model-a", true, 102), floored("model-b", true, 101)}, "model-b", "a reason"},
		{"a tie-break on a run the display fit decides", []going.Record{floored("model-a", true, 101), floored("model-b", false, 101)}, "model-b", "a reason"},
		{"a tie-break for a model outside the tie", level, "model-c", "a reason"},
		{"a tie-break with no reason", level, "model-a", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "decision_models.json")
			if err := conclude(io.Discard, tc.records, "a test run", tc.model, tc.reason, out); err == nil {
				t.Error("conclude takes it")
			}
			if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a record is written (%v)", err)
			}
		})
	}
}

// floored is a measured model that meets R7's floors, with its right choices in each repeat and
// a display fit or none.
func floored(model string, fit bool, rights ...int) going.Record {
	action, chart := 0.5, 0.1
	r := going.Record{Model: model, Cases: 117, ActionThreshold: &action, NamedAccuracy: 0.95, OtherAccuracy: 0.9, P95MS: 150, Repeats: len(rights)}
	for _, right := range rights {
		r.RepeatRights = append(r.RepeatRights, going.RepeatRight{Other: right, Total: right})
	}
	if fit {
		r.ChartThreshold = &chart
	}
	return r
}

// chosenIn is the entries the written record marks chosen.
func chosenIn(t *testing.T, path string) []going.Record {
	t.Helper()
	records, err := readRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(records, func(r going.Record) bool { return !r.Chosen })
}
