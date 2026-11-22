package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// The conflict model is fitted by maximum likelihood and printed with its interval (spec R2): on
// stretches drawn from a known base and merges term, each coefficient's 95% interval holds the
// true value in about 95 of 100 draws, and the estimates centre on it. The test draws 40 sets of
// 2000 synthetic stretches, M uniform on 0 to 15, from base −2 and term 0.8 with seeded sources,
// fits each, and fails where an interval holds its true value in fewer than 32 of the 40 (a 95%
// interval misses that often with a chance near 1 in 10,000), or where the mean estimate stands
// further than 0.1 from the base or 0.05 from the term. On the fixture window
// conflict-from-fitted-rate, whose stretches separate (the one with M = 1 conflicted, the two with
// M = 0 merged), the fit reaches a chance below 10⁻⁶ at M = 0 and above 1 − 10⁻⁶ at M = 1, the
// limits its README requires, and gives each coefficient an interval with no end.
func TestConflictRateFit(t *testing.T) {
	t.Run("synthetic stretches", func(t *testing.T) {
		const base, term, draws, size = -2.0, 0.8, 40, 2000
		covered, sums := [2]int{}, [2]float64{}
		for seed := range uint64(draws) {
			source := rand.New(rand.NewPCG(seed, 0x66697474696e67))
			stretches := make([]Stretch, size)
			for i := range stretches {
				merges := source.IntN(16)
				chance := logistic(base + term*math.Log2(1+float64(merges)))
				stretches[i] = Stretch{Issue: fmt.Sprintf("ACME-%d", i+1), Merges: merges, Conflict: source.Float64() < chance}
			}
			model := FitConflicts(stretches)
			if model.Stretches != size || model.Level != conflictLevel {
				t.Fatalf("draw %d: the model was fitted on %d stretches at level %v, want %d at %v", seed, model.Stretches, model.Level, size, conflictLevel)
			}
			for i, c := range []struct {
				name string
				got  Coefficient
				want float64
			}{{"base", model.Base, base}, {"merges term", model.Term, term}} {
				if math.IsInf(c.got.Low, 0) || math.IsInf(c.got.High, 0) || c.got.Low > c.got.Estimate || c.got.Estimate > c.got.High {
					t.Fatalf("draw %d: the %s is %v with the interval [%v, %v], want a finite interval around the estimate", seed, c.name, c.got.Estimate,
						c.got.Low, c.got.High)
				}
				if c.got.Low <= c.want && c.want <= c.got.High {
					covered[i]++
				}
				sums[i] += c.got.Estimate
			}
		}
		for i, c := range []struct {
			name      string
			want, tol float64
		}{{"base", base, 0.1}, {"merges term", term, 0.05}} {
			t.Logf("the %s: true %v, mean estimate %v over %d draws, its 95%% interval holding the true value in %d", c.name, c.want,
				sums[i]/draws, draws, covered[i])
			if covered[i] < 32 {
				t.Errorf("the %s's 95%% interval held its true value %v in %d of %d draws, want at least 32", c.name, c.want, covered[i], draws)
			}
			if mean := sums[i] / draws; math.Abs(mean-c.want) > c.tol {
				t.Errorf("the %s's mean estimate over %d draws is %v, want within %v of %v", c.name, draws, mean, c.tol, c.want)
			}
		}
	})
	t.Run("the fixture window's stretches separate", func(t *testing.T) {
		window := fixtureWindow(t, "conflict-from-fitted-rate")
		trace, err := BuildTrace(window)
		if err != nil {
			t.Fatalf("build the window's trace: %v", err)
		}
		stretches := recordedStretches(trace, newBaseMerges(window, trace))
		want := []Stretch{
			{Issue: "ACME-1", Start: clock(9, 45), End: clock(11, 0), Merges: 1, Conflict: true, OwnMergeUnknown: true},
			{Issue: "ACME-1", Start: clock(11, 30), End: clock(12, 45), Merges: 0},
			{Issue: "ACME-2", Start: clock(9, 50), End: clock(10, 45), Merges: 0},
		}
		if !slices.EqualFunc(stretches, want, sameStretch) {
			t.Fatalf("the window's stretches are %+v, want %+v", stretches, want)
		}
		model := FitConflicts(stretches)
		if model.OwnMergeUnknown != 1 {
			t.Errorf("the model counts %d stretches whose M may hold their own merge, want 1: the window's merges name no pull request",
				model.OwnMergeUnknown)
		}
		t.Logf("the fixture window's conflict model: %s; base %v [%v, %v], merges term %v [%v, %v]; chance at M = 0 %v, at M = 1 %v", model,
			model.Base.Estimate, model.Base.Low, model.Base.High, model.Term.Estimate, model.Term.Low, model.Term.High, model.Chance(0), model.Chance(1))
		if chance := model.Chance(0); chance > 1e-6 {
			t.Errorf("the fitted chance at M = 0 is %v, want below 1e-6", chance)
		}
		if chance := model.Chance(1); chance < 1-1e-6 {
			t.Errorf("the fitted chance at M = 1 is %v, want above 1 - 1e-6", chance)
		}
		for name, c := range map[string]Coefficient{"base": model.Base, "merges term": model.Term} {
			if !math.IsInf(c.Low, -1) || !math.IsInf(c.High, 1) {
				t.Errorf("the %s's interval is [%v, %v], and stretches that separate give it no end", name, c.Low, c.High)
			}
		}
		if _, err := json.Marshal(model); err != nil {
			t.Errorf("the model of stretches that separate does not write as JSON: %v", err)
		}
	})
}

// sameStretch says whether a and b are one stretch, its moments compared as instants.
func sameStretch(a, b Stretch) bool {
	return a.Issue == b.Issue && a.Start.Equal(b.Start) && a.End.Equal(b.End) && a.Merges == b.Merges && a.Conflict == b.Conflict &&
		a.OwnMergeUnknown == b.OwnMergeUnknown
}

// linkedPullRequest is window with key's first Dispatch event linking the pull request url among
// its external links, as Dispatch serves a link: an object with its url.
func linkedPullRequest(t *testing.T, window Window, key, url string) Window {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(window.Events[key][0], &event); err != nil {
		t.Fatal(err)
	}
	event["payload"].(map[string]any)["external_links"] = []map[string]string{{"url": url, "kind": "url"}}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string][]json.RawMessage{}
	for issue, list := range window.Events {
		events[issue] = slices.Clone(list)
	}
	events[key][0] = raw
	window.Events = events
	return window
}

// A stretch's M leaves out the issue's own merge: the commit of the pull request its Dispatch
// events link, which GitHub lands a little before the daemon sees the merge, inside the stretch
// the merge ends. Where the issue links no pull request, or a commit inside the stretch names none,
// M counts every commit and the stretch is counted as one whose own merge could not be told apart.
// The test reads conflictWindow, ACME-1's stretches 09:45 to 11:00 and 11:30 to 12:45, over merges
// at 10:30 (pull request 7, another issue's) and 12:44 (pull request 41). With ACME-1's events
// linking pull request 41 of acme/widgets, its second stretch counts M = 0, its own merge left out,
// and its first M = 1, none counted unknown. With no link, the second counts M = 1 and both are
// counted. With the link and a merge at 12:00 that names no pull request, the second counts M = 1
// (the 12:00 merge) and is counted. The squash suffix the merges are read from is ` (#<n>)` at the
// end of a commit's first line.
func TestAStretchCountsNoMergeOfItsOwn(t *testing.T) {
	merges := []Merge{
		{SHA: "7a", At: clock(10, 30), Branch: "main", PR: 7},
		{SHA: "41b", At: clock(12, 44), Branch: "main", PR: 41},
	}
	unnamed := Merge{SHA: "c0", At: clock(12, 0), Branch: "main"}
	link := "https://github.com/acme/widgets/pull/41"
	for _, c := range []struct {
		name    string
		linked  bool
		merges  []Merge
		want    []Stretch
		unknown int
	}{
		{name: "its own pull request linked", linked: true, merges: merges, want: []Stretch{
			{Issue: "ACME-1", Start: clock(9, 45), End: clock(11, 0), Merges: 1, Conflict: true},
			{Issue: "ACME-1", Start: clock(11, 30), End: clock(12, 45), Merges: 0},
		}},
		{name: "no pull request linked", merges: merges, unknown: 2, want: []Stretch{
			{Issue: "ACME-1", Start: clock(9, 45), End: clock(11, 0), Merges: 1, Conflict: true, OwnMergeUnknown: true},
			{Issue: "ACME-1", Start: clock(11, 30), End: clock(12, 45), Merges: 1, OwnMergeUnknown: true},
		}},
		{name: "a merge naming no pull request", linked: true, merges: append(slices.Clone(merges), unnamed), unknown: 1, want: []Stretch{
			{Issue: "ACME-1", Start: clock(9, 45), End: clock(11, 0), Merges: 1, Conflict: true},
			{Issue: "ACME-1", Start: clock(11, 30), End: clock(12, 45), Merges: 1, OwnMergeUnknown: true},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			window := conflictWindow(t)
			window.Merges = map[string][]Merge{builtRepository: c.merges}
			if c.linked {
				window = linkedPullRequest(t, window, "ACME-1", link)
			}
			trace, err := BuildTrace(window)
			if err != nil {
				t.Fatalf("build the window's trace: %v", err)
			}
			if got, want := ownPullRequest(trace.Issues["ACME-1"]), map[bool]int{true: 41}[c.linked]; got != want {
				t.Errorf("the trace reads ACME-1's own pull request as %d, want %d", got, want)
			}
			stretches := recordedStretches(trace, newBaseMerges(window, trace))
			if !slices.EqualFunc(stretches, c.want, sameStretch) {
				t.Errorf("the window's stretches are %+v, want %+v", stretches, c.want)
			}
			if got := FitConflicts(stretches).OwnMergeUnknown; got != c.unknown {
				t.Errorf("the model counts %d stretches whose M may hold their own merge, want %d", got, c.unknown)
			}
		})
	}
	for message, want := range map[string]int{
		"fix(daemon): a replay counts no merge of its own (#41)":              41,
		"fix(daemon): a replay counts no merge of its own (#41)\n\nbody (#9)": 41,
		"Merge pull request #41 from acme/widgets":                            0,
		"fix(daemon): see (#41) for why":                                      0,
	} {
		if got := squashedPullRequest(message); got != want {
			t.Errorf("the commit message %q names pull request %d, want %d", message, got, want)
		}
	}
}

// Under a rule whose conflicting head moves back, a drawn conflict moves the issue back to
// implementing by the rule's trigger through the daemon's own path, and the loop's work is drawn
// from the window's recorded loops (spec R2; the fixture windows' README, "Conflicts"). The test
// replays the fixture window conflict-from-fitted-rate under that rule and fails for each measure
// that differs from the scorecard its expect.json works out by hand for legion-v20.42.5: ACME-1's
// first stretch draws a conflict at 11:00, moves back to implementing, runs the window's one
// recorded loop, its own, and merges at 12:45, its second stretch drawing none.
func TestADrawnConflictMovesTheIssueBackUnderAMovesBackRule(t *testing.T) {
	dsn := replayDSN(t)
	expect, err := readFixtureExpect(filepath.Join(fixtureWindowsDir, "conflict-from-fitted-rate"))
	if err != nil {
		t.Fatal(err)
	}
	want, ok := expect.Rules["legion-v20.42.5"]
	if !ok {
		t.Fatal("the window's expect.json holds no scorecard under legion-v20.42.5")
	}
	result, err := Run(context.Background(), RunConfig{Window: fixtureWindow(t, "conflict-from-fitted-rate"), Rule: replayedRule(t, "legion-v20.42.5"),
		Branch: MainBranch, Seed: want.Seed, Slowdown: 1, PGDSN: dsn, Out: t.TempDir()})
	if err != nil {
		t.Fatalf("replay the window under legion-v20.42.5: %v", err)
	}
	for _, diff := range scorecardDiffs(result.Scorecard, want) {
		t.Errorf("under legion-v20.42.5: %s", diff)
	}
	if refused := result.Divergence.Refused; len(refused) > 0 {
		t.Errorf("the replay refused %+v", refused)
	}
}

// The branch Merges zero sets the conflict model's merges term to zero and keeps its base (spec
// R2, assumption 3): every stretch draws at the base chance whatever M its base took. The test
// reads a model with a base and a term, and fails where the zero branch's chance at any M from 0
// to 20 is not the logistic of the base, or where the fitted model's chance at an M above 0 is.
// It then replays the fixture window conflict-from-fitted-rate under main on the branch
// main+merges=zero: ACME-1's first stretch, which conflicts at the fitted rate (its M is 1), draws
// at the base chance, below 10⁻⁶, and does not conflict, so no merge forward is asked, no loop of
// conflict is counted, and ACME-1 goes on with its last merging segment from 11:00 and is done at
// 11:15.
func TestTheZeroBranchDrawsWithoutTheMergesTerm(t *testing.T) {
	fitted := ConflictModel{Stretches: 10, Conflicts: 4, Base: Coefficient{Estimate: -1.5}, Term: Coefficient{Estimate: 0.9}, Level: conflictLevel}
	zero := fitted.WithoutTerm()
	for merges := range 21 {
		if got, want := zero.Chance(merges), logistic(-1.5); got != want {
			t.Errorf("under the zero branch the chance at M = %d is %v, want the base's %v", merges, got, want)
		}
		if merges > 0 && fitted.Chance(merges) == zero.Chance(merges) {
			t.Errorf("the fitted model's chance at M = %d is the base's %v, and its merges term is 0.9", merges, fitted.Chance(merges))
		}
	}

	dsn := replayDSN(t)
	branch, err := BranchNamed("main+merges=zero")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	result, err := Run(context.Background(), RunConfig{Window: fixtureWindow(t, "conflict-from-fitted-rate"), Rule: mainRule(t), Branch: branch,
		Seed: 1, Slowdown: 1, PGDSN: dsn, Out: out})
	if err != nil {
		t.Fatalf("replay the window on %s: %v", branch.Name, err)
	}
	if !result.Conflicts.TermZero {
		t.Errorf("the run's conflict model %s keeps its merges term on the branch %s", result.Conflicts, branch.Name)
	}
	if result.Conflicts.DrawnOwnMergeUnknown != 1 {
		t.Errorf("the run counts %d drawn stretches whose M may hold their own merge, want 1: ACME-1's first, over the 10:45 merge, "+
			"which names no pull request", result.Conflicts.DrawnOwnMergeUnknown)
	}
	if loop, counted := result.Scorecard.Loops[string(CauseConflict)]; counted {
		t.Errorf("the replay counts %+v of conflict, and on %s no stretch conflicts", loop, branch.Name)
	}
	if done := result.Scorecard.DoneAt["ACME-1"]; !done.Equal(clock(11, 15)) {
		t.Errorf("ACME-1 is done at %s, want 11:15, its last merging segment run from its stretch's end at 11:00", stamp(done))
	}
	for _, line := range replayLog(t, out) {
		if line.Msg == mergeForwardAskedLine {
			t.Errorf("the replay asked a merge forward of %s at %s, and on %s no stretch conflicts", line.Issue, stamp(line.Time), branch.Name)
		}
	}
}

// A stretch's chance of a conflict depends on how long its head waited only through the merges
// its base took meanwhile (spec R2: a rule that changes how long a branch waits changes its
// conflicts only as far as the merges term does). The test counts M over stretches of the window's
// base branch, main of acme/widgets, which took commits at 10:00 and 11:00 (and a commit of
// another branch at 10:30, which no stretch of it counts): a stretch of 20 minutes and one of
// three hours that each hold the 10:00 commit alone have M = 1 and the same chance; a stretch
// that also holds the 11:00 commit has M = 2 and another chance where the term is not zero, and
// the same under the zero branch; and a commit at a stretch's own start or end is not inside it.
func TestAConflictDependsOnTheWaitOnlyThroughMerges(t *testing.T) {
	window := Window{Merges: map[string][]Merge{builtRepository: {
		{SHA: "1a", At: clock(10, 0), Branch: "main"}, {SHA: "2b", At: clock(10, 30), Branch: "release"}, {SHA: "3c", At: clock(11, 0), Branch: "main"},
	}}}
	merges := newBaseMerges(window, Trace{Branches: map[string]string{builtRepository: "main"}})
	model := ConflictModel{Stretches: 20, Conflicts: 5, Base: Coefficient{Estimate: -2}, Term: Coefficient{Estimate: 1.1}, Level: conflictLevel}
	for _, c := range []struct {
		name       string
		start, end time.Time
		merges     int
	}{
		{"a stretch of 20 minutes", clock(9, 50), clock(10, 10), 1},
		{"a stretch of three hours", clock(8, 0), clock(10, 59), 1},
		{"a stretch over both commits", clock(8, 0), clock(11, 30), 2},
		{"a stretch from one commit to the next", clock(10, 0), clock(11, 0), 0},
	} {
		if got, _ := merges.between(builtRepository, 0, c.start, c.end); got != c.merges {
			t.Errorf("%s from %s to %s counts M = %d, want %d", c.name, stamp(c.start), stamp(c.end), got, c.merges)
		}
	}
	merged := func(start, end time.Time) int {
		count, _ := merges.between(builtRepository, 0, start, end)
		return count
	}
	short := model.Chance(merged(clock(9, 50), clock(10, 10)))
	long := model.Chance(merged(clock(8, 0), clock(10, 59)))
	if short != long {
		t.Errorf("a stretch of 20 minutes draws at %v and one of three hours at %v, and both took one merge", short, long)
	}
	both := merged(clock(8, 0), clock(11, 30))
	if model.Chance(both) == long {
		t.Errorf("a stretch that took two merges draws at %v, the chance of one that took one, under a merges term of 1.1", long)
	}
	if zero := model.WithoutTerm(); zero.Chance(both) != zero.Chance(1) {
		t.Errorf("under the zero branch a stretch that took two merges draws at %v and one that took one at %v", zero.Chance(both), zero.Chance(1))
	}
	if got := (ConflictModel{Stretches: 3}).Chance(5); got != 0 {
		t.Errorf("a window that records no conflict draws a chance of %v, want 0", got)
	}
	if rule := record.MainRule.OrMain(); rule.Conflict != record.ConflictMergeForward {
		t.Errorf("the rule main's conflicting head %s, want it to keep its phase while the base is merged forward", rule.Conflict)
	}
}
