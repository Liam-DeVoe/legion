package going_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/sjawhar/envoy/going"
)

// TestTheRecordReadsBack reads each entry the bake-off wrote back through DecisionRecord, by its
// model's name, with the thresholds the file holds, and holds the chosen entry to R7: one model is
// chosen, it was measured on at least 80 cases and 20 chart-worthy ones, it reaches 90% on the
// cases that name their action and 70% on the rest at a p95 under 500 ms, and its action
// threshold holds.
func TestTheRecordReadsBack(t *testing.T) {
	raw, err := os.ReadFile("decision_models.json")
	if err != nil {
		t.Fatal(err)
	}
	var file []struct {
		Model           string   `json:"model"`
		ActionThreshold *float64 `json:"action_threshold"`
		ChartThreshold  *float64 `json:"chart_threshold"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decision_models.json: %v", err)
	}
	if len(file) == 0 {
		t.Fatal("the record holds no measured model")
	}
	chosen := 0
	for _, entry := range file {
		record, held := going.DecisionRecord(entry.Model)
		if !held {
			t.Errorf("DecisionRecord(%q) holds no entry, though the file has one", entry.Model)
			continue
		}
		if !sameThreshold(record.ActionThreshold, entry.ActionThreshold) || !sameThreshold(record.ChartThreshold, entry.ChartThreshold) {
			t.Errorf("%s reads back thresholds %v and %v, want the file's %v and %v", entry.Model,
				show(record.ActionThreshold), show(record.ChartThreshold), show(entry.ActionThreshold), show(entry.ChartThreshold))
		}
		for _, v := range []*float64{record.ActionThreshold, record.ChartThreshold} {
			if v != nil && (*v < 0 || *v > 1) {
				t.Errorf("%s holds the threshold %v, outside a confidence's 0 to 1", entry.Model, *v)
			}
		}
		if !record.Chosen {
			continue
		}
		chosen++
		switch {
		case record.ActionThreshold == nil:
			t.Errorf("the chosen %s has no action threshold", record.Model)
		case record.Cases < going.LeastCases || record.ChartWorthyCases < going.LeastChartCases:
			t.Errorf("the chosen %s was measured on %d cases, %d chart-worthy; R7 needs %d and %d",
				record.Model, record.Cases, record.ChartWorthyCases, going.LeastCases, going.LeastChartCases)
		case record.NamedAccuracy < going.NamedFloor || record.OtherAccuracy < going.OtherFloor || record.P95MS >= going.P95CeilingMS:
			t.Errorf("the chosen %s reaches %.3f named, %.3f on the rest at p95 %.0f ms; R7 needs %v, %v and under %v",
				record.Model, record.NamedAccuracy, record.OtherAccuracy, record.P95MS, going.NamedFloor, going.OtherFloor, going.P95CeilingMS)
		}
	}
	if chosen != 1 {
		t.Errorf("%d entries are chosen, want one", chosen)
	}
	if _, held := going.DecisionRecord("a-model-nobody-measured"); held {
		t.Error("an unmeasured model reads back an entry")
	}
}

// Each entry's repeats agree with its pooled numbers, each paired test is the mirror of the
// other model's, and the chosen entry is the one R7's rule names: where the rule named it rather
// than a person, no model that meets the floors chose right more often over the repeats, and the
// branch it was named by is the verdict of its paired test against the best of the others.
func TestTheRecordHoldsItsRepeatsAndTheRuleThatChose(t *testing.T) {
	raw, err := os.ReadFile("decision_models.json")
	if err != nil {
		t.Fatal(err)
	}
	var names []struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &names); err != nil {
		t.Fatalf("decision_models.json: %v", err)
	}
	models := make([]string, 0, len(names))
	entries := map[string]going.Record{}
	right := map[string]int{}
	for _, name := range names {
		record, _ := going.DecisionRecord(name.Model)
		models = append(models, record.Model)
		entries[record.Model] = record
		if record.Repeats < 1 || len(record.RepeatRights) != record.Repeats {
			t.Errorf("%s holds %d repeats and %d repeat counts, want at least one and one count for each", record.Model, record.Repeats, len(record.RepeatRights))
			continue
		}
		named, other := 0, 0
		totals := make([]int, 0, record.Repeats)
		for _, counted := range record.RepeatRights {
			if counted.Total != counted.Named+counted.Other || counted.Named > record.NamedCases || counted.Other > record.OtherCases {
				t.Errorf("%s holds the repeat count %+v of %d named and %d other cases", record.Model, counted, record.NamedCases, record.OtherCases)
			}
			named, other = named+counted.Named, other+counted.Other
			totals = append(totals, counted.Total)
		}
		right[record.Model] = named + other
		if !sameShare(record.NamedAccuracy, named, record.NamedCases*record.Repeats) || !sameShare(record.OtherAccuracy, other, record.OtherCases*record.Repeats) {
			t.Errorf("%s pools %d named and %d other right choices over %d repeats, but holds the accuracies %v and %v",
				record.Model, named, other, record.Repeats, record.NamedAccuracy, record.OtherAccuracy)
		}
		if want := going.SpreadOf(totals); record.TotalSpread != want {
			t.Errorf("%s holds the spread %+v, want %+v from its repeats", record.Model, record.TotalSpread, want)
		}
		for _, c := range record.InconsistentCases {
			if c.Of != record.Repeats || c.Right < 1 || c.Right >= c.Of {
				t.Errorf("%s holds %+v as a case right in some repeats and wrong in others", record.Model, c)
			}
		}
	}
	meets := func(r going.Record) bool {
		return r.ActionThreshold != nil && r.NamedAccuracy >= going.NamedFloor && r.OtherAccuracy >= going.OtherFloor && r.P95MS < going.P95CeilingMS
	}
	for _, model := range models {
		record := entries[model]
		if len(record.PairedTests) != len(models)-1 {
			t.Errorf("%s holds %d paired tests, want one against each of the %d other models", model, len(record.PairedTests), len(models)-1)
		}
		for _, test := range record.PairedTests {
			mirror, held := pairedAgainst(entries[test.Against], model)
			switch {
			case !held:
				t.Errorf("%s holds a test against %q, which holds none against it", model, test.Against)
			case test.Better != mirror.Worse || test.Worse != mirror.Better || test.PValue != mirror.PValue || test.Real != mirror.Real:
				t.Errorf("%s against %s reads %+v, but the mirror reads %+v", model, test.Against, test, mirror)
			case test.Better-test.Worse != right[model]-right[test.Against]:
				t.Errorf("%s against %s: %d pairs against %d, but the right choices differ by %d", model, test.Against, test.Better, test.Worse, right[model]-right[test.Against])
			}
		}
		if !record.Chosen {
			if record.ChosenBy != "" {
				t.Errorf("%s is not chosen, yet names the branch %q", model, record.ChosenBy)
			}
			continue
		}
		var best *going.Record
		for _, other := range models {
			if o := entries[other]; other != model && meets(o) && (best == nil || right[other] > right[best.Model]) {
				best = &o
			}
		}
		switch record.ChosenBy {
		case going.ChosenByPerson:
		case going.ChosenOnly:
			if best != nil {
				t.Errorf("%s is chosen as the one model meeting the floors, but %s meets them too", model, best.Model)
			}
		case going.ChosenReal, going.ChosenNoise:
			if best == nil {
				t.Errorf("%s is chosen by a paired test, but no other model meets the floors", model)
				break
			}
			test, _ := pairedAgainst(record, best.Model)
			if right[model] < right[best.Model] || test.Real != (record.ChosenBy == going.ChosenReal) {
				t.Errorf("%s is chosen by %q with %d right choices, against %s's %d and a test reading %+v",
					model, record.ChosenBy, right[model], best.Model, right[best.Model], test)
			}
		default:
			t.Errorf("%s is chosen by %q, which is no branch of R7's rule", model, record.ChosenBy)
		}
	}
}

// A record entry with every field set reads back through JSON as it was written: no two fields
// share a name, so the fields the server reads (the model, both thresholds, chosen) come back
// beside every field a repeated run adds.
func TestARecordEntryRoundTrips(t *testing.T) {
	action, chart := 0.54, 0.32
	want := going.Record{
		Model: "demo-model", Endpoint: "https://decide.example",
		ActionThreshold: &action, ActionShare: 0.98, ChartThreshold: &chart, ChartShare: 1,
		MeasuredAt: "2026-11-16T12:00:00Z", ReleaseDate: "2026-10-22T02:00:00+00:00",
		Cases: 117, NamedCases: 45, OtherCases: 72, ChartWorthyCases: 25, Unanswered: 1,
		NamedAccuracy: 0.97, OtherAccuracy: 0.99, P50MS: 108.8, P95MS: 178.2,
		DisplayRates:      []going.DisplayRate{{Label: going.LabelChartWorthy, Answered: going.DisplayChart, Count: 50, Of: 50, Rate: 1}},
		Repeats:           2,
		RepeatRights:      []going.RepeatRight{{Named: 44, Other: 71, Total: 115}, {Named: 45, Other: 71, Total: 116}},
		TotalSpread:       going.Spread{Mean: 115.5, Min: 115, Max: 116, SD: 0.7071},
		InconsistentCases: []going.CaseConsistency{{Words: "close DEMO-12", Right: 1, Of: 2}},
		PairedTests: []going.PairedTest{{
			Against: "other-model", Pairs: 234, Better: 1, Worse: 3, PValue: 0.625, MeanDifference: -1, Interval: &[2]float64{-13.7, 11.7},
		}},
		Chosen: true, ChosenBy: going.ChosenNoise,
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got going.Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the entry reads back as\n%+v\nwant\n%+v", got, want)
	}
}

// pairedAgainst is the record's paired test against the named model.
func pairedAgainst(record going.Record, model string) (going.PairedTest, bool) {
	for _, test := range record.PairedTests {
		if test.Against == model {
			return test, true
		}
	}
	return going.PairedTest{}, false
}

// sameShare is whether share is right of n, as the bake-off divides it.
func sameShare(share float64, right, n int) bool {
	return n > 0 && share == float64(right)/float64(n)
}

func sameThreshold(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func show(v *float64) any {
	if v == nil {
		return "null"
	}
	return *v
}
