// Command going-bakeoff measures the decision models of the command bar (R7) and fits their two
// thresholds (R5). It puts every committed case of going/bakeoff/cases.json to every model named,
// through the same three questions and the same state the server asks (going.DecisionQuestions,
// going.DecisionState) over the catalogue the server serves (api.BuildGoingCatalogue), one
// agentloop.Decide call per case per model, one call at a time so each latency is one call's own.
// --repeats asks every case that many times: each repeat puts every case to each model in turn,
// so a drift of the endpoint over the run reaches every model alike, and the accuracies, the
// latencies, the display rates and both fits pool every answer of every repeat.
//
// It prints a markdown table and writes going/decision_models.json, which the server reads its
// thresholds from (R11). Beside the pooled table it prints each repeat's right choices, their
// spread, the cases a model chose right in some repeats and wrong in others, and the paired test
// of each two models over the same cases and repeats (going.ComparePaired), which says whether
// the difference in their right choices is real or noise; R7's rule names the model by it
// (choose), and the table says which branch of the rule did. --choose names the model to record
// as chosen in place of the rule's, for a choice a person made on more than the measurement; it
// must be a measured model that meets R7's floors, and the table prints the rule's choice beside
// it.
//
//	going-bakeoff --models jev-latest,jev-preview --endpoint https://api.typesafe.ai --key-file <file> [--repeats <n>] [--choose <model>]
//
// Run it from inside the envoy module: the case file and the record default to their committed
// paths under the module's root. It never prints the key. With no key it says so, writes no fit
// and exits non-zero; with fewer than 80 cases or fewer than 20 chart-worthy cases run, the run is
// void and writes nothing.
package main

import (
	"bufio"
	"cmp"
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
	// meetsFloors is whether the model reaches R7's floors and its action fit holds, and
	// right the count of right choices over every repeat, which ranks the models that do.
	meetsFloors bool
	right       int
}

// decision is what R7's rule names: named is the rule's model and branch the branch of the rule
// that named it (going.ChosenOnly, ChosenReal or ChosenNoise); test is the paired test of named
// against runnerUp, the two leading models that meet the floors, nil where fewer than two do.
// chosen is the model the record marks chosen: named, or the model --choose names.
type decision struct {
	named, runnerUp, chosen *measured
	branch                  string
	test                    *going.PairedTest
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
		models    = flag.String("models", "", "comma-separated model ids to measure; empty measures every model the endpoint serves")
		endpoint  = flag.String("endpoint", "https://api.typesafe.ai", "the System One endpoint's base URL")
		keyFile   = flag.String("key-file", "", "a file holding the endpoint's key")
		casesArg  = flag.String("cases", filepath.Join(root, "going", "bakeoff", "cases.json"), "the case set")
		outArg    = flag.String("out", filepath.Join(root, "going", "decision_models.json"), "the record to write")
		repeats   = flag.Int("repeats", 1, "how many times each model is asked every case; the accuracies, the fits and the choice pool every repeat")
		chooseArg = flag.String("choose", "", "the measured model to record as chosen in place of the rule's; it must meet R7's floors")
	)
	flag.Parse()
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

	ruled := choose(results)
	ruled.chosen = ruled.named
	if *chooseArg != "" && ruled.named != nil {
		ruled.chosen = nil
		for _, m := range results {
			if m.record.Model == *chooseArg {
				ruled.chosen = m
			}
		}
		if ruled.chosen == nil || !ruled.chosen.meetsFloors {
			return fmt.Errorf("--choose %q names no measured model that meets R7's floors", *chooseArg)
		}
	}
	fmt.Print(render(results, ruled, len(served), unmeasured))
	for _, m := range results {
		printMisses(os.Stderr, m)
	}
	if ruled.chosen == nil {
		return errors.New("no model meets R7's floors with an action fit that holds, so no record is written")
	}
	records := make([]going.Record, 0, len(results))
	for _, m := range results {
		m.record.Chosen = m == ruled.chosen
		switch {
		case !m.record.Chosen:
		case m == ruled.named:
			m.record.ChosenBy = ruled.branch
		default:
			m.record.ChosenBy = going.ChosenByPerson
		}
		records = append(records, m.record)
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outArg, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outArg)
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
		m.right += counted.Total
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
	m.meetsFloors = action.Holds && r.NamedAccuracy >= going.NamedFloor && r.OtherAccuracy >= going.OtherFloor && r.P95MS < going.P95CeilingMS
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

// choose is R7's rule. Only the models that meet the floors with an action fit count, and where
// one does, it is named. Where more do, they are ranked (ranking) and the paired test is put to
// the first two: where it finds their difference real, the rule names the better-answering of
// them; where it finds it noise, the ranking names the model. The ranking puts the most right
// choices first and a real difference is never a tie, so the better-answering model of a real
// difference is the first too: the branch says whether its lead was measured to be real or the
// ranking's keys named it. A display fit is a tiebreak only and never names a worse-answering
// model. The decision is empty where no model meets the floors.
func choose(results []*measured) decision {
	var candidates []*measured
	for _, m := range results {
		if m.meetsFloors {
			candidates = append(candidates, m)
		}
	}
	switch len(candidates) {
	case 0:
		return decision{}
	case 1:
		return decision{named: candidates[0], branch: going.ChosenOnly}
	}
	slices.SortStableFunc(candidates, ranking)
	first, second := candidates[0], candidates[1]
	test := paired(first, second)
	d := decision{named: first, runnerUp: second, test: &test, branch: going.ChosenNoise}
	if test.Real {
		d.branch = going.ChosenReal
	}
	return d
}

// ranking orders the models that meet the floors as R7 ranks them: the most right choices over
// every repeat first; a display fit breaks a tie and never outranks a better answer, and a lower
// p95 breaks what is left.
func ranking(a, b *measured) int {
	if a.right != b.right {
		return cmp.Compare(b.right, a.right)
	}
	if fitA, fitB := a.record.ChartThreshold != nil, b.record.ChartThreshold != nil; fitA != fitB {
		if fitA {
			return -1
		}
		return 1
	}
	return cmp.Compare(a.record.P95MS, b.record.P95MS)
}

// render is the markdown table of the run.
func render(results []*measured, ruled decision, served int, unmeasured []string) string {
	var b strings.Builder
	first := results[0].record
	fmt.Fprintf(&b, "## Decision model bake-off\n\n")
	fmt.Fprintf(&b, "%d case(s) per model, each asked %d time(s), one `systemone` call each time, run one at a time. The endpoint serves %d model(s)", first.Cases, first.Repeats, served)
	if len(unmeasured) > 0 {
		fmt.Fprintf(&b, "; not measured: %s", strings.Join(unmeasured, ", "))
	}
	fmt.Fprintf(&b, ". Floors (R7): at least %.0f%% on the cases that name their action, at least %.0f%% on the rest, p95 under %.0f ms. A threshold holds where at least 60%% of its cases are at or above it (R5). Every accuracy, latency, threshold and display rate pools every answer of every repeat.\n\n", going.NamedFloor*100, going.OtherFloor*100, going.P95CeilingMS)
	b.WriteString("| Model | Released | Named accuracy | Rest accuracy | p50 ms | p95 ms | Action threshold | Cases at or above | Chart threshold | Chart-worthy at or above | Chart-worthy cases used | Unanswered | Floors met |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, m := range results {
		r := m.record
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %.0f | %.0f | %s | %.1f%% | %s | %.1f%% | %d | %d | %s |\n",
			r.Model, r.ReleaseDate,
			share(r.NamedAccuracy, r.NamedCases*r.Repeats), share(r.OtherAccuracy, r.OtherCases*r.Repeats),
			r.P50MS, r.P95MS,
			threshold(r.ActionThreshold), r.ActionShare*100,
			threshold(r.ChartThreshold), r.ChartShare*100,
			r.ChartWorthyCases, r.Unanswered, yesNo(m.meetsFloors))
	}
	b.WriteString("\n### Display rates\n\n| Model | Label | Answered | Answers | Rate |\n| --- | --- | --- | --- | --- |\n")
	for _, m := range results {
		for _, rate := range m.record.DisplayRates {
			fmt.Fprintf(&b, "| %s | %s | %s | %d of %d | %.1f%% |\n", m.record.Model, rate.Label, rate.Answered, rate.Count, rate.Of, rate.Rate*100)
		}
	}

	b.WriteString("\n### Right choices in each repeat\n\nEach cell is the right choices on the cases that name their action, plus those on the rest, and their total, in the order the repeats ran.\n\n| Repeat |")
	for _, m := range results {
		fmt.Fprintf(&b, " %s |", m.record.Model)
	}
	b.WriteString("\n| --- |" + strings.Repeat(" --- |", len(results)) + "\n")
	for repeat := range first.Repeats {
		fmt.Fprintf(&b, "| %d |", repeat+1)
		for _, m := range results {
			counted := m.record.RepeatRights[repeat]
			fmt.Fprintf(&b, " %d + %d = %d |", counted.Named, counted.Other, counted.Total)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n### Spread of the total over the repeats\n\n| Model | Mean | Min | Max | SD | Right over every repeat | Cases right in some repeats and wrong in others |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, m := range results {
		r := m.record
		s := r.TotalSpread
		fmt.Fprintf(&b, "| %s | %.2f of %d | %d | %d | %.2f | %d of %d | %d |\n",
			r.Model, s.Mean, r.Cases, s.Min, s.Max, s.SD, m.right, r.Cases*r.Repeats, len(r.InconsistentCases))
	}
	b.WriteString("\n### Cases right in some repeats and wrong in others\n\n| Model | Case | Right in |\n| --- | --- | --- |\n")
	for _, m := range results {
		if len(m.record.InconsistentCases) == 0 {
			fmt.Fprintf(&b, "| %s | none | |\n", m.record.Model)
		}
		for _, c := range m.record.InconsistentCases {
			fmt.Fprintf(&b, "| %s | %s | %d of %d |\n", m.record.Model, strings.ReplaceAll(c.Words, "|", `\|`), c.Right, c.Of)
		}
	}
	if len(results) > 1 {
		fmt.Fprintf(&b, "\n### Paired test\n\nMcNemar's exact test, two-sided at %.2f, on the (case, repeat) pairs: each case's answer from one model in one repeat against the same case's answer from the other in the same repeat. Only the discordant pairs, one model right and the other wrong, tell the two apart. The mean difference is the first model's right choices less the second's in one repeat, averaged over the repeats, with its 95%% interval by Student's t over the repeats. The verdict is the exact test's alone: the t interval leans on a normal approximation, which a handful of repeats that differ at all does not bear out.\n\n", going.PairedAlpha)
		b.WriteString("| First | Second | Pairs | First right, second wrong | First wrong, second right | p | Mean difference per repeat | 95% interval | Verdict |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for i, m := range results {
			for _, other := range results[i+1:] {
				test := paired(m, other)
				fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %.4g | %+.3f | %s | %s |\n",
					m.record.Model, other.record.Model, test.Pairs, test.Better, test.Worse, test.PValue,
					test.MeanDifference, interval(test.Interval), verdict(test.Real))
			}
		}
	}
	b.WriteString("\n")
	switch {
	case ruled.named == nil:
		b.WriteString("**Choice:** none. No model meets R7's floors with an action threshold that holds.\n")
	case ruled.chosen != ruled.named:
		fmt.Fprintf(&b, "**Choice:** `%s`, named by --choose among the models meeting the floors. R7's rule names `%s`. %s\n", ruled.chosen.record.Model, ruled.named.record.Model, ruled.why(len(results)))
	default:
		fmt.Fprintf(&b, "**Choice:** `%s`. %s\n", ruled.named.record.Model, ruled.why(len(results)))
	}
	return b.String()
}

// why is the sentence saying which branch of R7's rule named the model, with the numbers that
// decided it.
func (d decision) why(measuredModels int) string {
	switch d.branch {
	case going.ChosenOnly:
		s := "**Branch: only.** It is the one model that meets R7's floors with an action threshold that holds"
		if measuredModels == 1 {
			s += "; it is the one model measured"
		}
		return s + ", so no paired test decides between models."
	case going.ChosenReal:
		return fmt.Sprintf("**Branch: real.** McNemar's exact test finds its difference from `%s` real (%d pairs against %d, p = %.4g, under %.2f), so the rule names the better-answering model: %d right choices over the repeats against %d.",
			d.runnerUp.record.Model, d.test.Better, d.test.Worse, d.test.PValue, going.PairedAlpha, d.named.right, d.runnerUp.right)
	default:
		return fmt.Sprintf("**Branch: noise.** McNemar's exact test finds its difference from `%s` noise (%d pairs against %d, p = %.4g, not under %.2f), so the rule's ranking names the model, by %s.",
			d.runnerUp.record.Model, d.test.Better, d.test.Worse, d.test.PValue, going.PairedAlpha, decidedBy(d.named, d.runnerUp))
	}
}

// decidedBy names the key of the ranking that puts a before b, with its values.
func decidedBy(a, b *measured) string {
	switch fitA, fitB := a.record.ChartThreshold != nil, b.record.ChartThreshold != nil; {
	case a.right != b.right:
		return fmt.Sprintf("the most right choices over the repeats, %d against %d", a.right, b.right)
	case fitA != fitB:
		return fmt.Sprintf("a display fit, which `%s` lacks, the right choices being equal at %d", b.record.Model, a.right)
	case a.record.P95MS != b.record.P95MS:
		return fmt.Sprintf("the lower p95, %.0f ms against %.0f ms, the right choices (%d) and the display fits being alike", a.record.P95MS, b.record.P95MS, a.right)
	default:
		return "the order the models were named in, every key of the ranking being equal"
	}
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

func verdict(isReal bool) string {
	if isReal {
		return going.ChosenReal
	}
	return going.ChosenNoise
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
