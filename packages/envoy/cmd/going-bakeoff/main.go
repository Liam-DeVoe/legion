// Command going-bakeoff measures the decision models of the command bar (R7) and fits their two
// thresholds (R5). It puts every committed case of going/bakeoff/cases.json to every model named,
// through the same three questions and the same state the server asks (going.DecisionQuestions,
// going.DecisionState) over the catalogue the server serves (api.BuildGoingCatalogue), one
// agentloop.Decide call per case per model, one call at a time so each latency is one call's own.
// --repeats asks every case that many times: each repeat puts every case to each model in turn,
// so a drift of the endpoint over the run reaches every model alike, and the accuracies, the
// latencies, the display rates and both fits pool every answer of every repeat.
//
// It prints a markdown table and writes going/decision_models.json (going.EncodeRecords), which
// the server reads its thresholds from (R11). Beside the pooled table it prints each repeat's
// right choices, their spread, the cases a model chose right in some repeats and wrong in others,
// and the paired test of each two models over the same cases and repeats (going.ComparePaired).
//
// R7's rule names the chosen model (going.Choose): of the models that meet R7's floors with an
// action fit that holds, the one with the most right choices pooled over every repeat. Where the
// most right choices are level, a display fit breaks the tie, and nothing else does. Where that
// leaves models level, the rule names none: the run writes its record with no model chosen, says
// so and exits non-zero, and the architect names one of the level models with --tie-break and
// says why with --tie-reason, which the record keeps. A tie-break on a run the rule decides is
// refused, and so is one with no reason. The paired test is printed and recorded beside the
// choice as evidence of how far the lead in right choices can be trusted; it names no model.
//
//	going-bakeoff --models jev-latest,jev-preview --endpoint https://api.typesafe.ai --key-file <file> [--repeats <n>] [--tie-break <model> --tie-reason <why>]
//	going-bakeoff --from-record going/decision_models.json [--tie-break <model> --tie-reason <why>]
//
// --from-record measures nothing: it puts R7's rule to a record a run wrote, prints the run's
// table with what the rule names, and writes the record to --out, which is how an architect's
// tie-break reaches the run that tied.
//
// Run it from inside the envoy module: the case file and the record default to their committed
// paths under the module's root. It never prints the key. With no key it says so, writes no fit
// and exits non-zero; with fewer than 80 cases or fewer than 20 chart-worthy cases run, the run is
// void and writes nothing.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/dispatch/api"
)

// callTime bounds one decision call, its one retry included. It is far past the p95 ceiling, so
// a slow call is measured as slow rather than cut short.
const callTime = 20 * time.Second

// modulePath is the module the default paths are found under.
const modulePath = "github.com/sjawhar/envoy"

// errVoid is a run that measured too little to stand: it writes nothing.
var errVoid = errors.New("void")

// testCase is one committed case.
type testCase struct {
	Words       string            `json:"words"`
	Context     going.PageContext `json:"context"`
	Action      string            `json:"action"`
	NamesAction bool              `json:"names_action"`
	ChartWorthy bool              `json:"chart_worthy"`
}

// answer is one case's outcome on one model in one repeat.
type answer struct {
	c        testCase
	answered bool
	action   agentloop.Decision
	display  agentloop.Decision
	latency  time.Duration
	fault    string
}

// right is whether the call answered and chose the case's labelled action.
func (a answer) right() bool {
	return a.answered && a.action.Choice == a.c.Action
}

// measured is one model's whole run.
type measured struct {
	model  agentloop.Model
	record going.Record
	// repeats holds each repeat's answers in the cases' order, and rights whether each chose
	// right, which the paired test pairs.
	repeats [][]answer
	rights  [][]bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "going-bakeoff:", err)
		os.Exit(1)
	}
}

func run() error {
	root, rootErr := moduleRoot()
	var (
		models     = flag.String("models", "", "comma-separated model ids to measure; empty measures every model the endpoint serves")
		endpoint   = flag.String("endpoint", "https://api.typesafe.ai", "the System One endpoint's base URL")
		keyFile    = flag.String("key-file", "", "a file holding the endpoint's key")
		casesArg   = flag.String("cases", filepath.Join(root, "going", "bakeoff", "cases.json"), "the case set")
		outArg     = flag.String("out", filepath.Join(root, "going", "decision_models.json"), "the record to write")
		repeats    = flag.Int("repeats", 1, "how many times each model is asked every case; the accuracies, the fits and the choice pool every repeat")
		fromRecord = flag.String("from-record", "", "a record a run wrote: put R7's rule to it, print its table and write it to --out, measuring nothing")
		tieBreak   = flag.String("tie-break", "", "the model the architect names where the run is level on right choices and on the display fit; refused on any other run")
		tieReason  = flag.String("tie-reason", "", "why the architect named the --tie-break model, which the record keeps")
	)
	flag.Parse()
	if (*tieBreak == "") != (strings.TrimSpace(*tieReason) == "") {
		return errors.New("--tie-break and --tie-reason go together: a person names a model only with a reason the record keeps")
	}
	if *fromRecord != "" {
		for _, name := range []string{"models", "endpoint", "key-file", "cases", "repeats"} {
			if isSet(name) {
				return fmt.Errorf("--from-record measures nothing, so it takes no --%s", name)
			}
		}
		if rootErr != nil && !isSet("out") {
			return fmt.Errorf("finding the envoy module for the default --out: %w; run from inside packages/envoy or pass --out", rootErr)
		}
		records, err := readRecords(*fromRecord)
		if err != nil {
			return err
		}
		source := fmt.Sprintf("Read from %s, which a run wrote; nothing was measured again", *fromRecord)
		if err := conclude(os.Stdout, records, source, *tieBreak, *tieReason, *outArg); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *outArg)
		return nil
	}
	if rootErr != nil && (!isSet("cases") || !isSet("out")) {
		return fmt.Errorf("finding the envoy module for the default paths: %w; run from inside packages/envoy or pass --cases and --out", rootErr)
	}
	if !strings.HasPrefix(*endpoint, "https://") {
		return fmt.Errorf("--endpoint %q is not https, and the key is never sent in the clear", *endpoint)
	}
	if *repeats < 1 {
		return fmt.Errorf("--repeats %d asks no case; it must be at least 1", *repeats)
	}
	if *keyFile == "" {
		return errors.New("no key: --key-file names no file, so no model was measured and no fit is recorded")
	}
	ctx := context.Background()
	token := going.FileTokenSource{Path: *keyFile}.Token
	if _, err := token(ctx); err != nil {
		return fmt.Errorf("no key: the key file %s cannot be read or is empty, so no model was measured and no fit is recorded", *keyFile)
	}

	catalogue, err := api.BuildGoingCatalogue()
	if err != nil {
		return fmt.Errorf("the catalogue: %w", err)
	}
	actions := api.GoingDecisionActions(catalogue.Actions())
	cases, err := readCases(*casesArg, actions)
	if err != nil {
		return err
	}
	// A void run measures nothing, names no choice and writes nothing (c12): every model is put
	// every case, so the case set alone says whether a run would stand.
	worthy := 0
	for _, c := range cases {
		if c.ChartWorthy {
			worthy++
		}
	}
	if len(cases) < going.LeastCases || worthy < going.LeastChartCases {
		return fmt.Errorf("%w: the cases hold %d cases, %d chart-worthy, under R7's %d and %d, so nothing is measured or written",
			errVoid, len(cases), worthy, going.LeastCases, going.LeastChartCases)
	}

	served, err := servedModels(ctx, *endpoint, token)
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(served))
	if *models != "" {
		names = strings.Split(*models, ",")
		for index, name := range names {
			names[index] = strings.TrimSpace(name)
		}
	}
	for _, name := range names {
		if _, ok := served[name]; !ok {
			return fmt.Errorf("the endpoint serves no model %q; it serves %s", name, strings.Join(slices.Sorted(maps.Keys(served)), ", "))
		}
	}
	var unmeasured []string
	for name := range served {
		if !slices.Contains(names, name) {
			unmeasured = append(unmeasured, name)
		}
	}
	slices.Sort(unmeasured)

	questions := going.DecisionQuestions(actions)
	results := make([]*measured, 0, len(names))
	for _, name := range names {
		results = append(results, &measured{
			model: agentloop.Model{
				Endpoint: agentloop.Endpoint{Kind: agentloop.SystemOne, BaseURL: *endpoint, Token: token},
				ID:       name,
			},
			record: going.Record{Model: name, Endpoint: *endpoint, ReleaseDate: served[name], Repeats: *repeats},
		})
	}
	// Each repeat puts every case to each model in turn, so a drift of the endpoint over the run
	// reaches every model alike, and the paired test pairs the answers of one pass.
	for repeat := range *repeats {
		for _, m := range results {
			if repeat == 0 {
				m.record.MeasuredAt = time.Now().UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(os.Stderr, "repeat %d of %d: measuring %s on %d cases\n", repeat+1, *repeats, m.record.Model, len(cases))
			answers, err := ask(ctx, m.model, questions, cases)
			if err != nil {
				return err
			}
			m.repeats = append(m.repeats, answers)
		}
	}
	for _, m := range results {
		score(m)
	}
	for _, m := range results {
		m.record.PairedTests = make([]going.PairedTest, 0, len(results)-1)
		for _, other := range results {
			if other != m {
				m.record.PairedTests = append(m.record.PairedTests, paired(m, other))
			}
		}
	}

	records := make([]going.Record, 0, len(results))
	for _, m := range results {
		records = append(records, m.record)
	}
	source := fmt.Sprintf("The endpoint serves %d model(s)", len(served))
	if len(unmeasured) > 0 {
		source += "; not measured: " + strings.Join(unmeasured, ", ")
	}
	err = conclude(os.Stdout, records, source, *tieBreak, *tieReason, *outArg)
	for _, m := range results {
		printMisses(os.Stderr, m)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outArg)
	return nil
}

// readRecords is the record a run wrote, each entry holding one count of right choices for each
// of the run's repeats.
func readRecords(path string) ([]going.Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the record: %w", err)
	}
	var records []going.Record
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("the record %s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("the record %s holds no measured model", path)
	}
	for _, r := range records {
		if r.Repeats < 1 || r.Repeats != records[0].Repeats || len(r.RepeatRights) != r.Repeats {
			return nil, fmt.Errorf("the record %s holds %s with %d repeats and %d repeat counts, against %d repeats for %s", path, r.Model, r.Repeats, len(r.RepeatRights), records[0].Repeats, records[0].Model)
		}
	}
	return records, nil
}

// conclude puts R7's rule to the run's records, with the architect's tie-break where one is
// given, prints the run's table and what the rule names to w, and writes the record to out. It
// writes nothing where a tie-break is refused or no model meets the floors, and where the run is
// a full tie that no tie-break breaks it writes the record with no model chosen; each of those is
// an error, so the tool exits non-zero.
func conclude(w io.Writer, records []going.Record, source, tieBreak, tieReason, out string) error {
	choice := going.Choose(records)
	var refused error
	if tieBreak != "" {
		choice, refused = choice.Break(tieBreak, tieReason)
	}
	fmt.Fprint(w, render(records, choice, source))
	switch {
	case refused != nil:
		return fmt.Errorf("--tie-break %s is refused: %w; no record is written", tieBreak, refused)
	case len(choice.Level) == 0:
		return errors.New("no model meets R7's floors with an action fit that holds, so no record is written")
	}
	for i := range records {
		r := &records[i]
		r.Chosen = r.Model == choice.Model
		r.ChosenBy, r.ChosenReason = "", ""
		if r.Chosen {
			r.ChosenBy, r.ChosenReason = choice.By, choice.Reason
		}
	}
	raw, err := going.EncodeRecords(records)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		return err
	}
	if choice.Model == "" {
		return fmt.Errorf("wrote %s with no model chosen: %s are level on right choices and on the display fit, so R7's rule names none, and the architect names one: going-bakeoff --from-record %s --tie-break <model> --tie-reason <why>",
			out, modelList(choice.Level), out)
	}
	return nil
}

// isSet is whether the named flag was passed.
func isSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// moduleRoot is the directory of the envoy module's go.mod, found from the working directory up.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if f, err := os.Open(filepath.Join(dir, "go.mod")); err == nil {
			first, _ := bufio.NewReader(f).ReadString('\n')
			_ = f.Close()
			if strings.TrimSpace(first) == "module "+modulePath {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod of %s above the working directory", modulePath)
		}
		dir = parent
	}
}

// readCases is the case set, each case held to its labels' rules: words to decide on, an action
// the catalogue holds or none, and no chart-worthy request for a change.
func readCases(path string, actions []going.Action) ([]testCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the cases: %w", err)
	}
	var cases []testCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, fmt.Errorf("the cases %s: %w", path, err)
	}
	var problems []string
	for i, c := range cases {
		held := c.Action == going.ActionNone || slices.ContainsFunc(actions, func(a going.Action) bool { return a.Name == c.Action })
		switch {
		case strings.TrimSpace(c.Words) == "":
			problems = append(problems, fmt.Sprintf("case %d has no words", i+1))
		case !held:
			problems = append(problems, fmt.Sprintf("case %d (%q) is labelled %q, which is no catalogue action", i+1, c.Words, c.Action))
		case c.ChartWorthy && c.Action != going.ActionNone:
			problems = append(problems, fmt.Sprintf("case %d (%q) asks for a change and is labelled chart-worthy", i+1, c.Words))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the cases %s:\n%s", path, strings.Join(problems, "\n"))
	}
	return cases, nil
}

// servedModels is each model the endpoint serves, with the release date it gives for it.
func servedModels(ctx context.Context, endpoint string, token func(context.Context) (string, error)) (map[string]string, error) {
	key, err := token(ctx)
	if err != nil {
		return nil, err
	}
	target, err := url.JoinPath(endpoint, "v1", "models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: callTime, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("the model list redirected, which is never followed with a key")
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing the endpoint's models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("listing the endpoint's models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing the endpoint's models: %s: %.300q", resp.Status, body)
	}
	var list struct {
		Models []struct {
			Name        string `json:"name"`
			ReleaseDate string `json:"release_date"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("the endpoint's model list: %w", err)
	}
	served := make(map[string]string, len(list.Models))
	for _, m := range list.Models {
		served[m.Name] = m.ReleaseDate
	}
	if len(served) == 0 {
		return nil, errors.New("the endpoint serves no model")
	}
	return served, nil
}

// ask puts every case to the model once, one call at a time, each answer with its own call's
// latency.
func ask(ctx context.Context, model agentloop.Model, questions map[string]agentloop.Question, cases []testCase) ([]answer, error) {
	answers := make([]answer, 0, len(cases))
	for _, c := range cases {
		started := time.Now()
		result, err := agentloop.Decide(ctx, agentloop.DecideRequest{
			Model:     model,
			State:     going.DecisionState(c.Words, c.Context),
			Questions: questions,
			Bounds:    agentloop.Bounds{Time: callTime},
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", model.ID, err)
		}
		a := answer{c: c, latency: time.Since(started)}
		action, gotAction := result.Answers[going.QuestionAction]
		display, gotDisplay := result.Answers[going.QuestionDisplay]
		switch {
		case result.Fault != "":
			a.fault = string(result.Fault) + ": " + result.Detail
		case !gotAction || !gotDisplay:
			a.fault = "the answer lacks the action or the display question"
		default:
			a.answered, a.action, a.display = true, action, display
		}
		answers = append(answers, a)
	}
	return answers, nil
}

// score fills the model's record from its answers, pooling every repeat: the accuracies, the
// latencies, the display rates and both fits count every answer, and each repeat's right choices,
// their spread and the cases answered both ways are kept beside them.
func score(m *measured) {
	var named, other, all []going.ActionOutcome
	var displays []going.DisplayOutcome
	latencies := make([]float64, 0, len(m.repeats)*len(m.repeats[0]))
	r := &m.record
	cases := m.repeats[0]
	rightIn := make([]int, len(cases))
	totals := make([]int, 0, len(m.repeats))
	for _, answers := range m.repeats {
		var counted going.RepeatRight
		rights := make([]bool, len(answers))
		for i, a := range answers {
			outcome := going.ActionOutcome{Answered: a.answered, Right: a.right(), Confidence: a.action.Confidence}
			all = append(all, outcome)
			if a.c.NamesAction {
				named = append(named, outcome)
			} else {
				other = append(other, outcome)
			}
			if outcome.Right {
				rights[i] = true
				rightIn[i]++
				if a.c.NamesAction {
					counted.Named++
				} else {
					counted.Other++
				}
			}
			if !a.answered {
				r.Unanswered++
			}
			displays = append(displays, going.DisplayOutcome{
				ChartWorthy: a.c.ChartWorthy,
				Chart:       a.answered && a.display.Choice == going.DisplayChart,
				Confidence:  a.display.Confidence,
			})
			latencies = append(latencies, float64(a.latency.Microseconds())/1000)
		}
		counted.Total = counted.Named + counted.Other
		m.rights = append(m.rights, rights)
		r.RepeatRights = append(r.RepeatRights, counted)
		totals = append(totals, counted.Total)
	}
	r.Cases = len(cases)
	for _, a := range cases {
		if a.c.NamesAction {
			r.NamedCases++
		} else {
			r.OtherCases++
		}
		if a.c.ChartWorthy {
			r.ChartWorthyCases++
		}
	}
	r.TotalSpread = going.SpreadOf(totals)
	r.InconsistentCases = []going.CaseConsistency{}
	for i, right := range rightIn {
		if right > 0 && right < len(m.repeats) {
			r.InconsistentCases = append(r.InconsistentCases, going.CaseConsistency{Words: cases[i].c.Words, Right: right, Of: len(m.repeats)})
		}
	}
	r.NamedAccuracy, r.OtherAccuracy = going.Accuracy(named), going.Accuracy(other)
	slices.Sort(latencies)
	r.P50MS, r.P95MS = percentile(latencies, 0.50), percentile(latencies, 0.95)

	action := going.FitActionThreshold(all)
	r.ActionShare = action.Share
	if action.Holds {
		r.ActionThreshold = &action.Threshold
	}
	chart := going.FitChartThreshold(displays)
	r.ChartShare = chart.Share
	if chart.Holds {
		r.ChartThreshold = &chart.Threshold
	}
	r.DisplayRates = displayRates(m.repeats)
}

// percentile is the nearest-rank percentile of sorted values, zero for none.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(rank, 0)]
}

// displayRates is, for each display label, the share of the answers to its cases, over every
// repeat, answered chart, prose, or not at all, each with the count of answers behind it.
func displayRates(repeats [][]answer) []going.DisplayRate {
	var rates []going.DisplayRate
	for _, label := range []string{going.LabelChartWorthy, going.LabelNotChartWorthy} {
		worthy := label == going.LabelChartWorthy
		counts := map[string]int{}
		of := 0
		for _, answers := range repeats {
			for _, a := range answers {
				if a.c.ChartWorthy != worthy {
					continue
				}
				of++
				switch {
				case !a.answered:
					counts[going.DisplayUnanswered]++
				default:
					counts[a.display.Choice]++
				}
			}
		}
		for _, answered := range []string{going.DisplayChart, going.DisplayProse, going.DisplayUnanswered} {
			rate := 0.0
			if of > 0 {
				rate = float64(counts[answered]) / float64(of)
			}
			rates = append(rates, going.DisplayRate{Label: label, Answered: answered, Count: counts[answered], Of: of, Rate: rate})
		}
	}
	return rates
}

// paired is a's paired test against b over the run's cases and repeats.
func paired(a, b *measured) going.PairedTest {
	test := going.ComparePaired(a.rights, b.rights)
	test.Against = b.record.Model
	return test
}

// render is the markdown table of the run, and what R7's rule names on it.
func render(records []going.Record, choice going.Choice, source string) string {
	var b strings.Builder
	first := records[0]
	fmt.Fprintf(&b, "## Decision model bake-off\n\n")
	fmt.Fprintf(&b, "%d case(s) per model, each asked %d time(s), one `systemone` call each time, run one at a time. %s. Floors (R7): at least %.0f%% on the cases that name their action, at least %.0f%% on the rest, p95 under %.0f ms. A threshold holds where at least 60%% of the answers it counts are at or above it (R5). Every accuracy, latency, threshold, share and display rate pools every answer of every repeat.\n\n",
		first.Cases, first.Repeats, source, going.NamedFloor*100, going.OtherFloor*100, going.P95CeilingMS)
	b.WriteString("| Model | Released | Named accuracy | Rest accuracy | p50 ms | p95 ms | Action threshold | Answers at or above | Chart threshold | Chart-worthy answers at or above | Chart-worthy cases used | Unanswered | Floors met |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range records {
		r := &records[i]
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %.0f | %.0f | %s | %.1f%% | %s | %.1f%% | %d | %d | %s |\n",
			r.Model, r.ReleaseDate,
			share(r.NamedAccuracy, r.NamedCases*r.Repeats), share(r.OtherAccuracy, r.OtherCases*r.Repeats),
			r.P50MS, r.P95MS,
			threshold(r.ActionThreshold), r.ActionShare*100,
			threshold(r.ChartThreshold), r.ChartShare*100,
			r.ChartWorthyCases, r.Unanswered, yesNo(r.MeetsFloors()))
	}
	b.WriteString("\n### Display rates\n\n| Model | Label | Answered | Answers | Rate |\n| --- | --- | --- | --- | --- |\n")
	for i := range records {
		for _, rate := range records[i].DisplayRates {
			fmt.Fprintf(&b, "| %s | %s | %s | %d of %d | %.1f%% |\n", records[i].Model, rate.Label, rate.Answered, rate.Count, rate.Of, rate.Rate*100)
		}
	}

	b.WriteString("\n### Right choices in each repeat\n\nEach cell is the right choices on the cases that name their action, plus those on the rest, and their total, in the order the repeats ran.\n\n| Repeat |")
	for i := range records {
		fmt.Fprintf(&b, " %s |", records[i].Model)
	}
	b.WriteString("\n| --- |" + strings.Repeat(" --- |", len(records)) + "\n")
	for repeat := range first.Repeats {
		fmt.Fprintf(&b, "| %d |", repeat+1)
		for i := range records {
			counted := records[i].RepeatRights[repeat]
			fmt.Fprintf(&b, " %d + %d = %d |", counted.Named, counted.Other, counted.Total)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n### Spread of the total over the repeats\n\n| Model | Mean | Min | Max | SD | Right over every repeat | Cases right in some repeats and wrong in others |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range records {
		r := &records[i]
		s := r.TotalSpread
		fmt.Fprintf(&b, "| %s | %.2f of %d | %d | %d | %.2f | %d of %d | %d |\n",
			r.Model, s.Mean, r.Cases, s.Min, s.Max, s.SD, r.Right(), r.Cases*r.Repeats, len(r.InconsistentCases))
	}
	b.WriteString("\n### Cases right in some repeats and wrong in others\n\n| Model | Case | Right in |\n| --- | --- | --- |\n")
	for i := range records {
		r := &records[i]
		if len(r.InconsistentCases) == 0 {
			fmt.Fprintf(&b, "| %s | none | |\n", r.Model)
		}
		for _, c := range r.InconsistentCases {
			fmt.Fprintf(&b, "| %s | %s | %d of %d |\n", r.Model, strings.ReplaceAll(c.Words, "|", `\|`), c.Right, c.Of)
		}
	}
	if len(records) > 1 {
		b.WriteString("\n### Paired test\n\nMcNemar's exact test, two-sided, on the (case, repeat) pairs: each case's answer from one model in one repeat against the same case's answer from the other in the same repeat. Only the discordant pairs, one model right and the other wrong, tell the two apart, and p is the chance that two models answering equally well split them at least as unevenly. The test names no model: it says how far a lead in right choices can be trusted, and R7's rule does not read it. The mean difference is the first model's right choices less the second's in one repeat, averaged over the repeats, with its 95% interval by Student's t over the repeats, which leans on a normal approximation that a handful of repeats that differ at all does not bear out.\n\n")
		fmt.Fprintf(&b, "| First | Second | Pairs | First right, second wrong | First wrong, second right | p | p under %.2f | Mean difference per repeat | 95%% interval |\n", going.PairedAlpha)
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for i := range records {
			for j := i + 1; j < len(records); j++ {
				one, other := records[i].Model, records[j].Model
				test, held := records[i].PairedAgainst(other)
				if !held {
					fmt.Fprintf(&b, "| %s | %s | none recorded | | | | | | |\n", one, other)
					continue
				}
				fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %.4g | %s | %+.3f | %s |\n",
					one, other, test.Pairs, test.Better, test.Worse, test.PValue,
					yesNo(test.Real), test.MeanDifference, interval(test.Interval))
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(says(records, choice))
	return b.String()
}

// says is what R7's rule names on the run, or the architect where the rule names none, with the
// numbers that decided it, and beside a named model its paired tests against the other models
// that meet the floors.
func says(records []going.Record, choice going.Choice) string {
	var candidates []*going.Record
	most := 0
	for i := range records {
		if records[i].MeetsFloors() {
			candidates = append(candidates, &records[i])
			most = max(most, records[i].Right())
		}
	}
	if len(candidates) == 0 {
		return "**Choice:** none. No model meets R7's floors with an action threshold that holds.\n"
	}
	answers := candidates[0].Cases * candidates[0].Repeats
	var b strings.Builder
	switch choice.By {
	case going.ChosenByRightChoices:
		fmt.Fprintf(&b, "**Choice:** `%s`, by the most right choices pooled over every repeat: %d of %d", choice.Model, most, answers)
		var others []string
		for _, c := range candidates {
			if c.Model != choice.Model {
				others = append(others, fmt.Sprintf("`%s`'s %d", c.Model, c.Right()))
			}
		}
		switch {
		case len(others) > 0:
			fmt.Fprintf(&b, ", against %s.", strings.Join(others, ", "))
		case len(records) == 1:
			b.WriteString(". It is the one model measured, and it meets R7's floors with an action threshold that holds.")
		default:
			b.WriteString(". It is the one model that meets R7's floors with an action threshold that holds.")
		}
	case going.ChosenByDisplayFit:
		var level []string
		for _, c := range candidates {
			if c.Right() == most {
				level = append(level, c.Model)
			}
		}
		fmt.Fprintf(&b, "**Choice:** `%s`, by R7's tiebreak: %s are level at %d of %d right choices pooled over every repeat, and `%s` alone of them has a display fit.",
			choice.Model, modelList(level), most, answers, choice.Model)
	default:
		fits := "none of them has a display fit"
		for _, c := range candidates {
			if c.Model == choice.Level[0] && c.DisplayFit() {
				fits = "each has a display fit"
			}
		}
		tie := fmt.Sprintf("%s are level at %d of %d right choices pooled over every repeat and %s, so R7's rule names none", modelList(choice.Level), most, answers, fits)
		if choice.By == going.ChosenByPerson {
			fmt.Fprintf(&b, "**Choice:** `%s`, named by the architect: %s. The reason recorded: %s", choice.Model, tie, choice.Reason)
		} else {
			fmt.Fprintf(&b, "**Choice:** none. %s; the architect names one with `--tie-break <model> --tie-reason <why>`.", tie)
		}
	}
	b.WriteString("\n")
	var chosen *going.Record
	for _, c := range candidates {
		if c.Model == choice.Model {
			chosen = c
		}
	}
	if chosen == nil || len(candidates) == 1 {
		return b.String()
	}
	fmt.Fprintf(&b, "\nBeside the choice, the paired test of `%s` against each other model that meets the floors. It says how far a lead in right choices can be trusted, and it names no model.\n\n", chosen.Model)
	for _, c := range candidates {
		if c == chosen {
			continue
		}
		if test, held := chosen.PairedAgainst(c.Model); held {
			fmt.Fprintf(&b, "- against `%s`: %d pairs it chose right and the other wrong, %d the other way, p = %.4g\n", c.Model, test.Better, test.Worse, test.PValue)
		} else {
			fmt.Fprintf(&b, "- against `%s`: none recorded\n", c.Model)
		}
	}
	return b.String()
}

// modelList is the model names, each as code, joined as a sentence lists them.
func modelList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "`" + name + "`"
	}
	if len(quoted) < 2 {
		return strings.Join(quoted, "")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

func share(rate float64, n int) string {
	return fmt.Sprintf("%.1f%% (%d of %d)", rate*100, int(math.Round(rate*float64(n))), n)
}

func threshold(v *float64) string {
	if v == nil {
		return "none holds"
	}
	return fmt.Sprintf("%.3f", *v)
}

func interval(v *[2]float64) string {
	if v == nil {
		return "none: one repeat"
	}
	return fmt.Sprintf("%+.3f to %+.3f", v[0], v[1])
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

// printMisses names each case a model chose wrong or gave no answer on, in each repeat, with its
// confidence, so a description R5 must correct can be found.
func printMisses(w io.Writer, m *measured) {
	fmt.Fprintf(w, "\n%s: misses\n", m.record.Model)
	for repeat, answers := range m.repeats {
		for _, a := range answers {
			switch {
			case !a.answered:
				fmt.Fprintf(w, "  repeat %d  no answer  %q: %s\n", repeat+1, a.c.Words, a.fault)
			case !a.right():
				fmt.Fprintf(w, "  repeat %d  %.3f  %q: chose %s, labelled %s\n", repeat+1, a.action.Confidence, a.c.Words, a.action.Choice, a.c.Action)
			}
		}
	}
	fmt.Fprintf(w, "%s: display\n", m.record.Model)
	for repeat, answers := range m.repeats {
		for _, a := range answers {
			if a.answered && (a.display.Choice == going.DisplayChart) != a.c.ChartWorthy {
				fmt.Fprintf(w, "  repeat %d  %.3f  %q: answered %s, chart-worthy %t\n", repeat+1, a.display.Confidence, a.c.Words, a.display.Choice, a.c.ChartWorthy)
			}
		}
	}
}
