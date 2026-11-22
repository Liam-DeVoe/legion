package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
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

// reachedConflictWindow is a window from 12:00 to 18:00 recorded under legion-v20.42.5, its inputs
// reaching back to 00:00, whose reach and window hold stretches at two rates of conflict. ACME-1, a
// root promoted at 00:30, is in implementing from 01:00. In the reach it goes to testing and back 30
// times, 10 minutes each way, its tester failing every round: 30 stretches, none a conflict, the
// last ending at 11:00. It goes to testing again at 11:50, and a conflict at 12:05 ends that stretch
// inside the window, though it began in the reach. In the window it goes to testing and back 10
// times from 12:20, its head conflicting in every second round and its tester failing in the
// others: 10 stretches, 5 of them conflicts. It goes to testing a last time at 15:40 and stands
// there at the window's end, a stretch with no outcome. Its base branch took no merge, so every
// stretch has M = 0.
func reachedConflictWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(12, 0), clock(18, 0))
	b.window.Parts[0].Rule = "legion-v20.42.5"
	b.reaches(12 * time.Hour)
	b.capped(2)
	b.boot(clock(0, 30))
	b.file(clock(0, 30), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(0, 30), "ACME-1")
	b.rootDesigned(clock(0, 45), "ACME-1")
	b.segmentEnds(clock(1, 0), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	round := func(at time.Time, back workflow.TriggerKind) {
		b.segmentEnds(at, "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
		b.segmentEnds(at.Add(10*time.Minute), "ACME-1", phase.Testing, phase.Implementing, back)
	}
	for i := range 30 {
		round(clock(1, 10).Add(time.Duration(i)*20*time.Minute), workflow.TriggerTesterFailed)
	}
	b.segmentEnds(clock(11, 50), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(12, 5), "ACME-1", phase.Testing, phase.Implementing, workflow.TriggerConflict)
	for i := range 10 {
		back := workflow.TriggerTesterFailed
		if i%2 == 0 {
			back = workflow.TriggerConflict
		}
		round(clock(12, 20).Add(time.Duration(i)*20*time.Minute), back)
	}
	b.segmentEnds(clock(15, 40), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	return b.build()
}

// The conflict model is fitted on the stretches inside the window alone (plan D3): a stretch that
// began in the reach ran under what the reach recorded, and a reach of a release with no conflict
// trigger records each of its conflicts as another move back. The test reads
// reachedConflictWindow, whose record holds 41 ended stretches, 6 of them conflicts: 30 in the reach
// with none, one from the reach into the window that ends in one, and 10 in the window, 5 of them
// conflicts. It fails where the model is fitted on other than the window's 10 over the window's
// span; where its base is not the window's rate, the log-odds of 5 conflicts to 5 stretches
// without one (0), or its interval does not hold that; where the interval holds the base of the 41
// pooled (the log-odds of 6 to 35, about −1.76); where the printed model does not give the span;
// and where the window's record in a directory of runs counts other stretches than the model.
func TestTheConflictModelIsFittedOnTheWindowAlone(t *testing.T) {
	window := reachedConflictWindow(t)
	trace, err := BuildTrace(window)
	if err != nil {
		t.Fatalf("build the window's trace: %v", err)
	}
	merges := newBaseMerges(window, trace)
	pooled := FitConflicts(recordedStretches(trace, merges))
	if pooled.Stretches != 41 || pooled.Conflicts != 6 {
		t.Fatalf("the record holds %d ended stretches, %d of them conflicts; want 41 and 6: 30 in the reach, one from the reach into the window and 10 in the window",
			pooled.Stretches, pooled.Conflicts)
	}
	const stretches, conflicts = 10, 5
	model := windowConflicts(trace, merges)
	if model.Stretches != stretches || model.Conflicts != conflicts || !model.From.Equal(window.From) || !model.To.Equal(window.To) {
		t.Fatalf("the model was fitted on %d stretches, %d of them conflicts, over %s to %s; want the window's %d and %d over %s to %s",
			model.Stretches, model.Conflicts, stamp(model.From), stamp(model.To), stretches, conflicts, stamp(window.From), stamp(window.To))
	}
	t.Logf("the window's conflict model: %s; the base of the window's and the reach's stretches pooled: %v [%v, %v]", model,
		pooled.Base.Estimate, pooled.Base.Low, pooled.Base.High)
	rate := math.Log(float64(conflicts) / float64(stretches-conflicts))
	if base := model.Base; math.Abs(base.Estimate-rate) > 1e-6 || base.Low > rate || rate > base.High {
		t.Errorf("the base is %v [%v, %v], want the window's rate %v within its interval", base.Estimate, base.Low, base.High, rate)
	}
	if base := model.Base; base.Low <= pooled.Base.Estimate && pooled.Base.Estimate <= base.High {
		t.Errorf("the base's interval [%v, %v] holds %v, the base of the window's and the reach's stretches pooled", base.Low, base.High,
			pooled.Base.Estimate)
	}
	if span := "began and ended inside the window from 2001-03-05T12:00:00Z to 2001-03-05T18:00:00Z"; !strings.Contains(model.String(), span) {
		t.Errorf("the printed model %q does not give the span it was fitted on, %q", model, span)
	}
	rec, err := newWindowRecord(window, "window-one", 1)
	if err != nil {
		t.Fatalf("the window's record: %v", err)
	}
	if rec.Stretches != stretches || rec.Conflicted != conflicts {
		t.Errorf("the window's record counts %d stretches, %d of them conflicts; want the model's %d and %d", rec.Stretches, rec.Conflicted,
			stretches, conflicts)
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

// phaseLoopsWindow is a window from 09:00 to 13:00 recorded under legion-v20.42.5, its inputs
// reaching back to the daemon's boot at 07:00, whose recorded conflict loops left reviewing and,
// where fromTesting, testing. ACME-1, a root promoted at 07:05, moves to testing at 08:50 and stands
// there at the window's start; its tester fails it at 09:30, and it works in implementing to the
// window's end. ACME-2, a root promoted at 09:00, works 15 minutes in each phase: its tester fails
// it at 10:00, and at 10:45 a conflict moves it back from reviewing; its loop is 15 minutes in
// implementing and 15 in testing, and it is back in reviewing at 11:15 to the window's end. With
// fromTesting, ACME-3, a root promoted at 11:00, works 15 minutes in each phase to testing at
// 11:45, and a conflict moves it back at 12:00; its loop is 25 minutes in implementing, and it is
// back in testing at 12:25 to the window's end. The base branch took a commit inside ACME-1's
// stretch, at 09:10, and one inside each stretch that ends in a conflict, at 10:40 and 11:50: the
// stretches inside the window separate, so the chance fitted at M = 1 is 1 and at M = 0 is 0, and
// ACME-1's, which began in the reach and is no part of the fit, draws a conflict at 09:30 under any
// seed.
func phaseLoopsWindow(t *testing.T, fromTesting bool) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(13, 0))
	b.window.Parts[0].Rule = "legion-v20.42.5"
	b.reaches(2 * time.Hour)
	b.capped(3)
	b.boot(clock(7, 0))
	move := func(hour, minute int, to phase.Phase, trigger workflow.TriggerKind) builtMove {
		return builtMove{clock(hour, minute), to, trigger}
	}
	rootThrough(b, "ACME-1", "a1", clock(7, 5), clock(7, 20), move(7, 35, phase.Implementing, workflow.TriggerPlannerCompleted),
		move(8, 50, phase.Testing, workflow.TriggerImplementationReady), move(9, 30, phase.Implementing, workflow.TriggerTesterFailed))
	rootThrough(b, "ACME-2", "a2", clock(9, 0), clock(9, 15), move(9, 30, phase.Implementing, workflow.TriggerPlannerCompleted),
		move(9, 45, phase.Testing, workflow.TriggerImplementationReady), move(10, 0, phase.Implementing, workflow.TriggerTesterFailed),
		move(10, 15, phase.Testing, workflow.TriggerImplementationReady), move(10, 30, phase.Reviewing, workflow.TriggerTesterPassed),
		move(10, 45, phase.Implementing, workflow.TriggerConflict), move(11, 0, phase.Testing, workflow.TriggerImplementationReady),
		move(11, 15, phase.Reviewing, workflow.TriggerTesterPassed))
	merges := []Merge{
		{SHA: "3e9a7c1b5d2f4068a1b2c3d4e5f60718293a4b5c", At: clock(9, 10), Branch: "main"},
		{SHA: "7c4d2e9f1a3b5067b1c2d3e4f5a6b7c8d9e0f1a2", At: clock(10, 40), Branch: "main"},
	}
	if fromTesting {
		rootThrough(b, "ACME-3", "a3", clock(11, 0), clock(11, 15), move(11, 30, phase.Implementing, workflow.TriggerPlannerCompleted),
			move(11, 45, phase.Testing, workflow.TriggerImplementationReady), move(12, 0, phase.Implementing, workflow.TriggerConflict),
			move(12, 25, phase.Testing, workflow.TriggerImplementationReady))
		merges = append(merges, Merge{SHA: "9f2b6d4a8c1e3057c2d3e4f5a6b7c8d9e0f1a2b3", At: clock(11, 50), Branch: "main"})
	}
	window := b.build()
	window.Merges = map[string][]Merge{builtRepository: merges}
	return window
}

// backInTesting is ACME-1's moves in a replay of phaseLoopsWindow whose drawn conflict returns it
// to the phase the conflict left: back to implementing by conflict, to testing at the end of the
// loop's implementing, and its own recorded testing outcome, its tester's failure.
var backInTesting = []fixtureMove{
	{From: phase.Testing, To: phase.Implementing, Trigger: workflow.TriggerConflict},
	{From: phase.Implementing, To: phase.Testing, Trigger: workflow.TriggerImplementationReady},
	{From: phase.Testing, To: phase.Implementing, Trigger: workflow.TriggerTesterFailed},
}

// checkACME1Moves replays window under legion-v20.42.5, the rule it ran, on the main branch at
// seed, and fails where ACME-1's moves in the replay or their moments differ from want and at, or
// where the replay refused a move.
func checkACME1Moves(t *testing.T, window Window, seed int64, want []fixtureMove, at []time.Time) {
	t.Helper()
	out := t.TempDir()
	result, err := Run(context.Background(), RunConfig{Window: window, Rule: replayedRule(t, "legion-v20.42.5"), Branch: MainBranch, Seed: seed,
		Slowdown: 1, PGDSN: replayDSN(t), Out: out})
	if err != nil {
		t.Fatalf("replay the window under legion-v20.42.5 at seed %d: %v", seed, err)
	}
	moved, err := replayedMoves(filepath.Join(out, logFile))
	if err != nil {
		t.Fatal(err)
	}
	moments := linesAt(replayLog(t, out), func(line replayLine) bool { return line.Msg == phaseChangedLine && line.Issue == "ACME-1" })
	if !slices.Equal(moved["ACME-1"], want) || !slices.EqualFunc(moments, at, time.Time.Equal) {
		t.Errorf("at seed %d ACME-1 moved %s at %s, want %s at %s", seed, describeMoves(moved["ACME-1"]), describeTimes(moments),
			describeMoves(want), describeTimes(at))
	}
	if refused := result.Divergence.Refused; len(refused) > 0 {
		t.Errorf("at seed %d the replay refused %+v", seed, refused)
	}
}

// A drawn conflict's loop is drawn among the recorded loops whose conflict left the same phase
// (spec R2): a loop recorded after a conflict in a later phase holds the work of that phase's
// return, which would carry the issue past the phase its own conflict left. The test replays
// phaseLoopsWindow with its loop from testing under legion-v20.42.5, the rule it ran, at the seeds
// 1 to 5. At each, ACME-1's stretch draws a conflict at 09:30 and is given ACME-3's loop, the one
// left from testing, not ACME-2's, left from reviewing: ACME-1 moves back to implementing by
// conflict at 09:30, to testing at 09:55 after the loop's 25 minutes of implementing, and runs its
// own recorded testing, 40 minutes, to its tester's failure at 10:35. The test fails for a seed
// whose replay moves ACME-1 otherwise or at other moments, or refuses a move.
func TestADrawnConflictIsGivenALoopLeftFromItsPhase(t *testing.T) {
	replayDSN(t)
	window := phaseLoopsWindow(t, true)
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			checkACME1Moves(t, window, seed, backInTesting, []time.Time{clock(9, 30), clock(9, 55), clock(10, 35)})
		})
	}
}

// Where the window records no loop left from the phase of a drawn conflict, the loop is drawn
// among those that left a later phase and cut at its first piece in that phase or a phase after
// it, so the issue returns to the phase its conflict left. The test replays phaseLoopsWindow
// without its loop from testing under legion-v20.42.5: ACME-1's stretch draws a conflict at 09:30
// and is given ACME-2's loop, left from reviewing, up to its testing. ACME-1 moves back to
// implementing by conflict at 09:30, to testing at 09:45 after the loop's 15 minutes of
// implementing, and runs its own recorded testing to its tester's failure at 10:25, rather than
// going on to reviewing by the loop's testing.
func TestALoopFromALaterPhaseReturnsTheIssueToThePhaseItsConflictLeft(t *testing.T) {
	replayDSN(t)
	checkACME1Moves(t, phaseLoopsWindow(t, false), 1, backInTesting, []time.Time{clock(9, 30), clock(9, 45), clock(10, 25)})
}

// earlierLoopWindow is a window from 09:00 to 13:00 recorded under legion-v20.42.5, its inputs
// reaching back to the daemon's boot at 07:00, whose one recorded conflict loop left testing.
// ACME-1, a root promoted at 07:05, moves to testing at 08:40 and stands there at the window's
// start; its tester passes it at 09:10, its reviewer sends it back to implementing at 09:40, and
// it works in implementing to the window's end. ACME-2, a root promoted at 11:00, works 15 minutes
// in each phase: its tester fails it at 12:00, and at 12:30 a conflict moves it back from testing;
// its loop is 20 minutes in implementing, and it is back in testing at 12:50 to the window's end.
// The base branch took a commit inside ACME-1's stretch, at 09:20, and one inside ACME-2's stretch
// that ends in a conflict, at 12:20: the stretches inside the window separate, so the chance
// fitted at M = 1 is 1 and at M = 0 is 0, and ACME-1's, which began in the reach and is no part of
// the fit, draws a conflict in reviewing at 09:40 under any seed.
func earlierLoopWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(13, 0))
	b.window.Parts[0].Rule = "legion-v20.42.5"
	b.reaches(2 * time.Hour)
	b.capped(2)
	b.boot(clock(7, 0))
	move := func(hour, minute int, to phase.Phase, trigger workflow.TriggerKind) builtMove {
		return builtMove{clock(hour, minute), to, trigger}
	}
	rootThrough(b, "ACME-1", "a1", clock(7, 5), clock(7, 20), move(7, 35, phase.Implementing, workflow.TriggerPlannerCompleted),
		move(8, 40, phase.Testing, workflow.TriggerImplementationReady), move(9, 10, phase.Reviewing, workflow.TriggerTesterPassed),
		move(9, 40, phase.Implementing, workflow.TriggerReviewRejected))
	rootThrough(b, "ACME-2", "a2", clock(11, 0), clock(11, 15), move(11, 30, phase.Implementing, workflow.TriggerPlannerCompleted),
		move(11, 45, phase.Testing, workflow.TriggerImplementationReady), move(12, 0, phase.Implementing, workflow.TriggerTesterFailed),
		move(12, 15, phase.Testing, workflow.TriggerImplementationReady), move(12, 30, phase.Implementing, workflow.TriggerConflict),
		move(12, 50, phase.Testing, workflow.TriggerImplementationReady))
	window := b.build()
	window.Merges = map[string][]Merge{builtRepository: {
		{SHA: "5a8c3e1f7b2d4096d3e4f5a6b7c8d9e0f1a2b3c4", At: clock(9, 20), Branch: "main"},
		{SHA: "b1d7f3a9c5e2408be4f5a6b7c8d9e0f1a2b3c4d5", At: clock(12, 20), Branch: "main"},
	}}
	return window
}

// Where the window records only loops that left a phase before the drawn conflict's, the loop is
// drawn among them, and after it the issue goes back from the phase the loop returned it to, to
// the phase its conflict left, through copies of its own most recent work in each phase between.
// The test replays earlierLoopWindow under legion-v20.42.5: ACME-1 moves to reviewing at 09:10 as
// recorded, its stretch draws a conflict in reviewing at 09:40, and it is given ACME-2's loop, left
// from testing. ACME-1 moves back to implementing by conflict at 09:40, to testing at 10:00 after
// the loop's 20 minutes of implementing, runs a copy of its own testing, 30 minutes, to reviewing
// at 10:30, and runs its own recorded reviewing, 30 minutes, to its reviewer's send-back at 11:00.
func TestALoopFromAnEarlierPhaseTakesTheIssueBackThroughItsOwnWork(t *testing.T) {
	replayDSN(t)
	want := []fixtureMove{
		{From: phase.Testing, To: phase.Reviewing, Trigger: workflow.TriggerTesterPassed},
		{From: phase.Reviewing, To: phase.Implementing, Trigger: workflow.TriggerConflict},
		{From: phase.Implementing, To: phase.Testing, Trigger: workflow.TriggerImplementationReady},
		{From: phase.Testing, To: phase.Reviewing, Trigger: workflow.TriggerTesterPassed},
		{From: phase.Reviewing, To: phase.Implementing, Trigger: workflow.TriggerReviewRejected},
	}
	checkACME1Moves(t, earlierLoopWindow(t), 1, want, []time.Time{clock(9, 10), clock(9, 40), clock(10, 0), clock(10, 30), clock(11, 0)})
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
