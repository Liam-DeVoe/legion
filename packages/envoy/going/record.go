package going

// record.go reads the record the bake-off (cmd/going-bakeoff) writes: one entry for each
// decision model it measured, with the two thresholds fitted on that model's own answers. A
// deployment names a model and nothing else, and a model with no entry here is no decision model
// (R11), so no threshold reaches the server that nobody measured. It also holds R7's choice rule
// (Choose), which the bake-off names the chosen model by and the record's own test holds the
// committed choice to, and the writer of the record's bytes (EncodeRecords).

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

//go:embed decision_models.json
var decisionModelsJSON []byte

// R7's floors and its least case counts: a model is a candidate only where it reaches NamedFloor
// on the cases that name their action and OtherFloor on the rest at a p95 under P95CeilingMS
// (MeetsFloors), measured on at least LeastCases cases of which at least LeastChartCases are
// chart-worthy. The floors admit a model and rank nothing: a p95 well under the ceiling counts
// for no more than one just under it. An action fit is no floor: a candidate without one ranks
// as any other, and where it ranks first the rule names no model until R5's repair gives it one
// (Choose).
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
// (R5). Chosen marks the model R7's rule named (Choose), or the architect where it named none.
// A run may ask every case more than once (Repeats); every share, accuracy, latency and display
// rate here then pools every answer of every repeat, and both thresholds are fitted on them all.
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
	// the same cases and repeats. It is evidence of how far a lead in right choices can be
	// trusted, recorded beside the choice; it names no model.
	PairedTests []PairedTest `json:"paired_tests"`
	// Chosen marks the model the choice named, and ChosenBy how (ChosenByRightChoices,
	// ChosenByDisplayFit or ChosenByPerson). ChosenReason is the architect's reason for a model
	// they named, on a ChosenByPerson entry alone. Both are empty on every entry not chosen.
	Chosen       bool   `json:"chosen"`
	ChosenBy     string `json:"chosen_by,omitempty"`
	ChosenReason string `json:"chosen_reason,omitempty"`
}

// How the chosen Record was named, which its ChosenBy holds: by the key of R7's rule that named
// it, or by the architect where the rule names none.
const (
	// ChosenByRightChoices is the candidate with the most right choices pooled over every
	// repeat, alone at that count and with an action fit; the one model that meets the floors,
	// with an action fit, is that too.
	ChosenByRightChoices = "right_choices"
	// ChosenByDisplayFit is R7's tiebreak: of the candidates level at the most right choices,
	// the one alone in having a display fit.
	ChosenByDisplayFit = "display_fit"
	// ChosenByPerson is the candidate the architect named where the rule names none, the
	// leading candidates being level on right choices and alike in the display fit, each with an
	// action fit (going-bakeoff --tie-break); the entry's ChosenReason says why.
	ChosenByPerson = "person"
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

// Right is the model's right choices pooled over every repeat: the count R7's rule ranks by.
func (r *Record) Right() int {
	right := 0
	for _, counted := range r.RepeatRights {
		right += counted.Total
	}
	return right
}

// MeetsFloors is whether the model is a candidate of R7's rule: it reaches NamedFloor on the
// cases that name their action and OtherFloor on the rest at a p95 under P95CeilingMS. Its
// action fit is no floor, and a candidate without one is ranked as any other (Choose).
func (r *Record) MeetsFloors() bool {
	return r.NamedAccuracy >= NamedFloor && r.OtherAccuracy >= OtherFloor && r.P95MS < P95CeilingMS
}

// DisplayFit is whether the model's chart-display fit holds: the key of R7's tiebreak.
func (r *Record) DisplayFit() bool {
	return r.ChartThreshold != nil
}

// PairedAgainst is the model's paired test against the named model, and whether it holds one.
func (r *Record) PairedAgainst(model string) (PairedTest, bool) {
	for _, test := range r.PairedTests {
		if test.Against == model {
			return test, true
		}
	}
	return PairedTest{}, false
}

// Choice is what R7's rule names over one run's records (Choose), or the architect's choice
// where the rule names none (Break).
type Choice struct {
	// Model is the model named and By how, a ChosenBy value; both are empty where none is.
	// Reason is the architect's, on a Choice they made alone.
	Model, By, Reason string
	// Level is the candidates ranked first, in the records' order: those with the most right
	// choices and, where some of those have a display fit and some none, those with one. It
	// holds one model where the ranking puts one first, every model of a full tie where it puts
	// several, and nothing where no model meets the floors.
	Level []string
	// Unfit is the models of Level with no action fit. Where it holds any, the rule names no
	// model and no person may: R5's repair is owed first.
	Unfit []string
	// Why is, where the rule names no model, why: no model meets the floors, the models ranked
	// first include one with no action fit, or they are a full tie. A person's choice keeps the
	// Why of the tie it broke. It is empty where the rule names a model.
	Why string
}

// Choose is R7's choice rule over one run's records. Every model that meets R7's three floors
// is a candidate (MeetsFloors), ranked by its right choices pooled over every repeat; where the
// most right choices are level, R7 prefers a model with a display fit, as a tiebreak only, so
// the level candidates with a display fit rank first. Where one candidate ranks first and has an
// action fit, it is named (ChosenByRightChoices, or ChosenByDisplayFit where the tiebreak put it
// first). Where a candidate ranked first has no action fit, the rule names no model, not the
// next one with a fit: that model acts on nothing, and R5's repair, correcting the catalogue's
// descriptions until a value fits, is owed before a model can be named. Where several rank
// first, each with an action fit, the run is a full tie and the rule names none: the architect
// names one of Level (Break). Nothing else decides: the paired test says how far a lead in right
// choices can be trusted and names no model, p95 is a floor and no key, and the records' order
// names nothing, Level keeping it only to print by.
func Choose(records []Record) Choice {
	most := -1
	var level []int // indexes of records
	for i := range records {
		if !records[i].MeetsFloors() {
			continue
		}
		switch right := records[i].Right(); {
		case right > most:
			most, level = right, append(level[:0], i)
		case right == most:
			level = append(level, i)
		}
	}
	if len(level) == 0 {
		return Choice{Why: "no model meets R7's floors"}
	}
	by := ChosenByRightChoices
	if len(level) > 1 {
		by = ChosenByDisplayFit
		if slices.ContainsFunc(level, func(i int) bool { return records[i].DisplayFit() }) {
			level = slices.DeleteFunc(level, func(i int) bool { return !records[i].DisplayFit() })
		}
	}
	choice := Choice{Level: make([]string, len(level))}
	for n, i := range level {
		choice.Level[n] = records[i].Model
		if records[i].ActionThreshold == nil {
			choice.Unfit = append(choice.Unfit, records[i].Model)
		}
	}
	first := &records[level[0]]
	count := fmt.Sprintf("%d of %d right choices pooled over every repeat", most, first.Cases*first.Repeats)
	fits := "none of them has a display fit"
	if first.DisplayFit() {
		fits = "each has a display fit"
	}
	const repair = "R5's repair, correcting the catalogue's descriptions until a value fits, is owed before a model can be named"
	switch {
	case len(level) == 1 && len(choice.Unfit) == 0:
		choice.Model, choice.By = first.Model, by
	case len(level) == 1 && by == ChosenByRightChoices:
		choice.Why = fmt.Sprintf("%s ranks first among the models that meet R7's floors, with the most right choices pooled over every repeat (%d of %d), and has no action fit, so R7's rule names no model: %s", first.Model, most, first.Cases*first.Repeats, repair)
	case len(level) == 1:
		choice.Why = fmt.Sprintf("%s ranks first among the models that meet R7's floors, alone with a display fit of those level at %s, and has no action fit, so R7's rule names no model: %s", first.Model, count, repair)
	case len(choice.Unfit) > 0:
		choice.Why = fmt.Sprintf("%s rank first among the models that meet R7's floors, level at %s and %s, and %s %s no action fit, so R7's rule names no model and no person may: %s",
			nameList(choice.Level), count, fits, nameList(choice.Unfit), hasHave(len(choice.Unfit)), repair)
	default:
		choice.Why = fmt.Sprintf("%s are level at %s and %s, so R7's rule names none and the architect names one of them", nameList(choice.Level), count, fits)
	}
	return choice
}

// Break is the architect's choice of model, for reason, where the rule names none. It refuses a
// run the rule decides, a run whose models ranked first include one with no action fit, a model
// outside the full tie and a reason that says nothing, returning c as it was: a person names a
// model only among models the measurement cannot tell apart and each could be named, and the
// record keeps why.
func (c Choice) Break(model, reason string) (Choice, error) {
	switch {
	case c.Model != "":
		return c, fmt.Errorf("R7's rule names %s (%s), and a person names a model only where the leading candidates are level on right choices and on the display fit", c.Model, c.By)
	case len(c.Level) == 0:
		return c, errors.New("no model meets R7's floors, so there is no tie to break")
	case len(c.Unfit) > 0:
		return c, fmt.Errorf("a person names a model only on a full tie of models that each have an action fit, and here %s", c.Why)
	case !slices.Contains(c.Level, model):
		return c, fmt.Errorf("%q is not one of the level models, %s", model, strings.Join(c.Level, ", "))
	case strings.TrimSpace(reason) == "":
		return c, fmt.Errorf("naming %s gives no reason, and the record keeps why a person named a model", model)
	}
	return Choice{Model: model, By: ChosenByPerson, Reason: reason, Level: c.Level, Why: c.Why}, nil
}

// nameList is the names joined as a sentence lists them.
func nameList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func hasHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// EncodeRecords is the bytes of the record the bake-off writes: Go's indented JSON, with a final
// newline. The committed file is these bytes exactly (TestTheRecordIsWhatTheBakeOffWrites), and
// biome.json's formatter leaves the file alone, so a run's record is committed as written.
func EncodeRecords(records []Record) ([]byte, error) {
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
