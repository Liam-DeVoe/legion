package going

// record.go reads the record the bake-off (cmd/going-bakeoff) writes: one entry for each
// decision model it measured, with the two thresholds fitted on that model's own answers. A
// deployment names a model and nothing else, and a model with no entry here is no decision model
// (R11), so no threshold reaches the server that nobody measured.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed decision_models.json
var decisionModelsJSON []byte

// R7's floors and its least case counts: a model is chosen only where it reaches NamedFloor on
// the cases that name their action and OtherFloor on the rest at a p95 under P95CeilingMS,
// measured on at least LeastCases cases of which at least LeastChartCases are chart-worthy. The
// bake-off chooses by them and the record's own test holds the chosen entry to them.
const (
	NamedFloor      = 0.90
	OtherFloor      = 0.70
	P95CeilingMS    = 500.0 // milliseconds, exclusive
	LeastCases      = 80
	LeastChartCases = 20
)

// Record is one measured model: its two fitted thresholds and the measurement they were fitted
// on. A threshold is nil where its fit does not hold: a model with no action fit is never chosen
// and acts on nothing, and one with no display fit draws a chart only on an explicit request
// (R5). Chosen marks the model the measurement named (R7). A run may ask every case more than
// once (Repeats); every share, accuracy, latency and display rate here then pools every answer of
// every repeat, and both thresholds are fitted on them all.
type Record struct {
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
	// ActionThreshold is the action fit's value, nil where none holds, and ActionShare the
	// share of all the answers, every case in every repeat, at or above it.
	ActionThreshold *float64 `json:"action_threshold"`
	ActionShare     float64  `json:"action_share"`
	// ChartThreshold is the chart-display fit's value, nil where none holds, and ChartShare
	// the share of the answers to chart-worthy cases at or above it.
	ChartThreshold *float64 `json:"chart_threshold"`
	ChartShare     float64  `json:"chart_share"`
	// MeasuredAt is when the run measured the model, and ReleaseDate the release date the
	// endpoint gave for the model then, both RFC 3339.
	MeasuredAt  string `json:"measured_at"`
	ReleaseDate string `json:"release_date"`
	// The case counts: every case run, those whose words name their action, the rest, the
	// chart-worthy ones, each case counted once however many times it was asked; and the calls
	// that gave no answer, over every repeat.
	Cases            int `json:"cases"`
	NamedCases       int `json:"named_cases"`
	OtherCases       int `json:"other_cases"`
	ChartWorthyCases int `json:"chart_worthy_cases"`
	Unanswered       int `json:"unanswered"`
	// NamedAccuracy and OtherAccuracy are the action question's accuracy on the cases that
	// name their action and on the rest.
	NamedAccuracy float64 `json:"named_accuracy"`
	OtherAccuracy float64 `json:"other_accuracy"`
	// P50MS and P95MS are the latency of one decision call, in milliseconds.
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	// DisplayRates is how the model answered the display question, for each label.
	DisplayRates []DisplayRate `json:"display_rates"`
	// Repeats is how many times the run asked the model every case, and RepeatRights the right
	// choices of each repeat, in the order the repeats ran.
	Repeats      int           `json:"repeats"`
	RepeatRights []RepeatRight `json:"repeat_rights"`
	// TotalSpread is how the total of right choices spread over the repeats.
	TotalSpread Spread `json:"total_spread"`
	// InconsistentCases is every case the model chose right in some repeats and wrong in
	// others.
	InconsistentCases []CaseConsistency `json:"inconsistent_cases"`
	// PairedTests is the paired test of the model against each other model of the run, over
	// the same cases and repeats.
	PairedTests []PairedTest `json:"paired_tests"`
	Chosen      bool         `json:"chosen"`
	// ChosenBy is, on the chosen entry, the branch of R7's rule that named it (ChosenOnly,
	// ChosenReal, ChosenNoise) or ChosenByPerson; it is empty on every other entry.
	ChosenBy string `json:"chosen_by,omitempty"`
}

// The branches of R7's rule, which a chosen Record's ChosenBy names. Only the models that meet
// the floors with an action fit count.
const (
	// ChosenOnly is a model that was the one to meet the floors.
	ChosenOnly = "only"
	// ChosenReal is the better-answering of the two leading models, whose difference the paired
	// test found real.
	ChosenReal = "real"
	// ChosenNoise is the model the ranking named (the most right choices, then a display fit,
	// then the lower p95), where the paired test found the two leading models' difference noise.
	ChosenNoise = "noise"
	// ChosenByPerson is a model a person named over the rule's (going-bakeoff --choose).
	ChosenByPerson = "choose"
)

// RepeatRight is one repeat's right choices: on the cases that name their action, on the rest,
// and in all.
type RepeatRight struct {
	Named int `json:"named"`
	Other int `json:"other"`
	Total int `json:"total"`
}

// CaseConsistency is a case's right choices over a run's repeats: Right of Of repeats.
type CaseConsistency struct {
	Words string `json:"words"`
	Right int    `json:"right"`
	Of    int    `json:"of"`
}

// DisplayRate is the share of the answers to the cases of one display label the model answered
// one way: Of answers, one for each such case in each repeat, and Count of them were answered
// Answered.
type DisplayRate struct {
	// Label is chart_worthy or not_chart_worthy.
	Label string `json:"label"`
	// Answered is chart, prose, or unanswered for a call that gave no answer.
	Answered string  `json:"answered"`
	Count    int     `json:"count"`
	Of       int     `json:"of"`
	Rate     float64 `json:"rate"`
}

// The labels and answers a DisplayRate names beyond the display question's own choices.
const (
	LabelChartWorthy    = "chart_worthy"
	LabelNotChartWorthy = "not_chart_worthy"
	DisplayUnanswered   = "unanswered"
)

// decisionRecords is the embedded record, read once.
var decisionRecords = sync.OnceValues(func() ([]Record, error) {
	var records []Record
	if err := json.Unmarshal(decisionModelsJSON, &records); err != nil {
		return nil, fmt.Errorf("going: decision_models.json: %w", err)
	}
	return records, nil
})

// DecisionRecord is the entry for the model of that name, and whether the record holds one. A
// record the binary cannot read holds no model, which TestTheRecordReadsBack keeps from
// shipping.
func DecisionRecord(model string) (Record, bool) {
	records, err := decisionRecords()
	if err != nil {
		return Record{}, false
	}
	for _, record := range records {
		if record.Model == model {
			return record, true
		}
	}
	return Record{}, false
}
