package replay

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// Stretch is one stretch of an issue's work in which its head can conflict with its base (spec
// R2; the fixture windows' README, "Conflicts"): one visit of the issue to the phases from testing
// to merging, from its move into testing to the move that ends the visit — its merge, a conflict or
// another move back. Merges is M, the first-parent commits of the issue's base branch (the window's
// merges) strictly after Start and strictly before End but the issue's own merge — the commit whose
// pull request (Merge.PR) is the one the issue's Dispatch events link
// (IssueTrace.LinkedPullRequests) — and Conflict says a recorded conflict move ended it.
// OwnMergeUnknown says M holds a commit that may be the issue's own merge: the issue links no pull
// request of its repository, or a commit inside the stretch names none. It is one observation of
// the conflict model (FitConflicts).
type Stretch struct {
	Issue           string    `json:"issue"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Merges          int       `json:"merges"`
	Conflict        bool      `json:"conflict"`
	OwnMergeUnknown bool      `json:"ownMergeUnknown"`
}

// stretchPhase says whether p is a phase of a stretch: a phase in which the issue's pull request
// stands on a head that can conflict with its base, testing through merging.
func stretchPhase(p phase.Phase) bool {
	return p == phase.Testing || p == phase.Reviewing || p == phase.Accepting || p == phase.Retro || p == phase.Merging
}

// endsStretch says whether move, the move out of a visit to a stretch phase, ends the stretch: a
// move to done (the merge), the conflict move, or any other move back. A move on to the next phase
// of the stretch, or into a hold, goes on with it.
func endsStretch(move *Move) bool {
	if move == nil {
		return false
	}
	to := cmp.Or(move.Source.To, move.To)
	return to == phase.Done || move.Trigger == workflow.TriggerConflict || move.Source.Way == MoveDraw || phase.MovesBack(move.From, to)
}

// startsStretch says whether a move from from to to begins a stretch: a move into a stretch phase
// from a phase before testing (a hold, which a stretch goes on through, is not one), or a move back
// that lands inside the stretch's phases, which ended the stretch before it.
func startsStretch(from, to phase.Phase) bool {
	if !stretchPhase(to) {
		return false
	}
	return phase.MovesBack(from, to) || !stretchPhase(from) && from != phase.Held
}

// baseMerges is the first-parent commits of each repository's base branch a window's merges hold,
// in time order, which a stretch's M is counted from by binary search: their moments, how many of
// them up to each name no pull request, and the moment of each pull request's merge.
type baseMerges struct {
	repos map[string]*repoMerges
}

// repoMerges is one repository's commits on its base branch: at is their moments in order, unnamed
// the count of the commits before each place that name no pull request (len(at)+1 entries), and
// merged the moment of the commit of each pull request a commit names.
type repoMerges struct {
	at      []time.Time
	unnamed []int
	merged  map[int]time.Time
}

// newBaseMerges indexes w's merges on the base branch t reads for each repository.
func newBaseMerges(w Window, t Trace) baseMerges {
	m := baseMerges{repos: make(map[string]*repoMerges, len(w.Merges))}
	for repo, commits := range w.Merges {
		base := cmp.Or(t.Branches[repo], defaultBranch)
		var onBase []Merge
		for _, commit := range commits {
			if commit.Branch == "" || commit.Branch == base {
				onBase = append(onBase, commit)
			}
		}
		slices.SortStableFunc(onBase, func(x, y Merge) int { return x.At.Compare(y.At) })
		r := &repoMerges{at: make([]time.Time, len(onBase)), unnamed: make([]int, len(onBase)+1), merged: map[int]time.Time{}}
		for i, commit := range onBase {
			r.at[i] = commit.At
			r.unnamed[i+1] = r.unnamed[i]
			if commit.PR == 0 {
				r.unnamed[i+1]++
			} else {
				r.merged[commit.PR] = commit.At
			}
		}
		m.repos[repo] = r
	}
	return m
}

// between is M of a stretch from start to end of an issue of repo whose own pull request is own (0
// where its Dispatch events link none): the commits of its base branch strictly after start and
// strictly before end but the merge of own, and whether M may hold the issue's own merge all the
// same — its own pull request unknown, or a commit inside naming none.
func (m baseMerges) between(repo string, own int, start, end time.Time) (int, bool) {
	r, ok := m.repos[repo]
	if !ok {
		return 0, false
	}
	after := sort.Search(len(r.at), func(i int) bool { return r.at[i].After(start) })
	before := sort.Search(len(r.at), func(i int) bool { return !r.at[i].Before(end) })
	if before <= after {
		return 0, false
	}
	merges := before - after
	if at, merged := r.merged[own]; own > 0 && merged && at.After(start) && at.Before(end) {
		merges--
	}
	return merges, merges > 0 && (own == 0 || r.unnamed[before]-r.unnamed[after] > 0)
}

// ownPullRequest is the number of iss's own pull request on its repository, as its Dispatch events
// link it, 0 where they link none.
func ownPullRequest(iss *IssueTrace) int {
	return iss.LinkedPullRequests[strings.ToLower(iss.Repo)]
}

// recordedStretches is every ended stretch of the issues of t's project as the record shows it,
// by issue and then time, with its M counted from merges. A stretch the record shows open at the
// window's end, or cut by its issue's leave, has no outcome, and one whose start lies before the
// inputs reach (Visit.Before) has no known M; neither is an observation of the model. A merge whose
// close waited for the issues beneath it ends its stretch at the merge (Visit.Merge), not at the
// close's later move to done, nor open where that move falls after the window's end.
func recordedStretches(t Trace, merges baseMerges) []Stretch {
	var stretches []Stretch
	for _, key := range sortedKeys(t.Issues) {
		iss := t.Issues[key]
		if iss.Project != t.Project {
			continue
		}
		own := ownPullRequest(iss)
		open, known := time.Time{}, false
		for i := range iss.Visits {
			visit := &iss.Visits[i]
			if open.IsZero() {
				if !stretchPhase(visit.Phase) {
					continue
				}
				open, known = visit.Enter, !visit.Before
			}
			out := visit.Out
			if visit.Merge != nil {
				out = visit.Merge
			}
			switch {
			case !stretchPhase(visit.Phase) && visit.Phase != phase.Held, out == nil:
				open = time.Time{}
			case endsStretch(out):
				if known {
					count, unknown := merges.between(iss.Repo, own, open, out.At)
					stretches = append(stretches, Stretch{Issue: key, Start: open, End: out.At, Merges: count,
						Conflict: out.Trigger == workflow.TriggerConflict, OwnMergeUnknown: unknown})
				}
				open = time.Time{}
			}
		}
	}
	return stretches
}

// stretchStartAt is the record's start of the stretch iss stands in at at, and false where it
// stands in none: the entry into the first visit of the stretch, which is the inputs' reach for a
// stretch the record shows from before it.
func stretchStartAt(iss *IssueTrace, at time.Time) (time.Time, bool) {
	var start time.Time
	var standing *Visit
	for i := range iss.Visits {
		visit := &iss.Visits[i]
		if visit.Enter.After(at) {
			break
		}
		standing = visit
		switch {
		case visit.In != nil && startsStretch(visit.In.From, visit.Phase):
			start = visit.Enter
		case start.IsZero() && stretchPhase(visit.Phase):
			start = visit.Enter
		case !stretchPhase(visit.Phase) && visit.Phase != phase.Held:
			start = time.Time{}
		}
	}
	if standing == nil || !standing.Exit.IsZero() && !standing.Exit.After(at) {
		return time.Time{}, false
	}
	return start, !start.IsZero()
}

// conflictLevel is the level of the conflict model's intervals, and conflictZ the standard normal
// quantile a two-sided interval at that level is read with.
const (
	conflictLevel = 0.95
	conflictZ     = 1.959963984540054
)

// fitRidge is added to the diagonal of the information the fit inverts, so a window whose
// stretches separate, or whose M never varies, still gives a step and an interval: it is far below
// the information of any coefficient the stretches identify, and a coefficient whose precision is
// at least half the ridge's (unidentified) gets an interval with no end.
const fitRidge = 1e-9

// Coefficient is one coefficient of the conflict model: its maximum-likelihood estimate and its
// Wald interval at the model's level. An end the stretches leave without one (they separate, or M
// never varies) is infinite, and its JSON value is null: a low end of null is minus infinity and a
// high end of null plus infinity.
type Coefficient struct {
	Estimate float64
	Low      float64
	High     float64
}

// MarshalJSON writes c with null for an infinite end, which JSON has no number for.
func (c Coefficient) MarshalJSON() ([]byte, error) {
	end := func(v float64) *float64 {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil
		}
		return &v
	}
	return json.Marshal(struct {
		Estimate *float64 `json:"estimate"`
		Low      *float64 `json:"low"`
		High     *float64 `json:"high"`
	}{end(c.Estimate), end(c.Low), end(c.High)})
}

// ConflictModel is the chance that a stretch of an issue's work ends in a conflict, fitted on a
// window's own recorded stretches (spec R2; plan D3: only window one records conflict moves, so
// only it fits a chance above zero): logistic(Base + Term × log2(1 + M)), M the merges into the
// stretch's base branch during it, the coefficients fitted by maximum likelihood with their
// intervals. A window that records no conflict fits a chance of 0 for every stretch, and one whose
// every stretch conflicted a chance of 1, without a fit.
type ConflictModel struct {
	// Stretches and Conflicts are the stretches the model was fitted on and how many ended in a
	// conflict.
	Stretches int `json:"stretches"`
	Conflicts int `json:"conflicts"`
	// Base and Term are the coefficients, zero where no fit was made.
	Base Coefficient `json:"base"`
	Term Coefficient `json:"term"`
	// Level is the level of the coefficients' intervals.
	Level float64 `json:"level"`
	// TermZero says the merges term is set to zero (the branch's Merges zero), the base kept.
	TermZero bool `json:"termZero"`
	// OwnMergeUnknown is how many of the stretches the model was fitted on, and DrawnOwnMergeUnknown
	// how many of the stretches a replay drew by it, counted a commit in M that may be the issue's
	// own merge (Stretch.OwnMergeUnknown): the issue's own pull request or the commit's unknown.
	OwnMergeUnknown      int `json:"ownMergeUnknown"`
	DrawnOwnMergeUnknown int `json:"drawnOwnMergeUnknown"`
}

// fitted says whether the model's coefficients were fitted: some stretch conflicted and some did
// not.
func (m ConflictModel) fitted() bool { return m.Conflicts > 0 && m.Conflicts < m.Stretches }

// Chance is the chance that a stretch with merges merges into its base branch ends in a conflict.
func (m ConflictModel) Chance(merges int) float64 {
	switch {
	case m.Conflicts == 0:
		return 0
	case !m.fitted():
		return 1
	}
	eta := m.Base.Estimate
	if !m.TermZero {
		eta += m.Term.Estimate * math.Log2(1+float64(merges))
	}
	return logistic(eta)
}

// WithoutTerm is m with its merges term set to zero (Branch.Merges zero, assumption 3): every
// stretch draws at the base chance, however many merges its base took.
func (m ConflictModel) WithoutTerm() ConflictModel {
	m.TermZero = true
	return m
}

// String is the model as a run prints it: the stretches and conflicts it was fitted on, each
// coefficient with its interval, the odds ratio per doubling of 1 + M, and the stretches whose M
// may hold their own merge.
func (m ConflictModel) String() string {
	unknown := fmt.Sprintf("; %d of the window's stretches and %d of the replay's counted a merge that may be their own", m.OwnMergeUnknown,
		m.DrawnOwnMergeUnknown)
	switch {
	case m.Stretches == 0:
		return "no stretch of the window ended, so no conflict is drawn" + unknown
	case m.Conflicts == 0:
		return fmt.Sprintf("none of the window's %d stretches conflicted, so every stretch draws a chance of 0", m.Stretches) + unknown
	case !m.fitted():
		return fmt.Sprintf("all of the window's %d stretches conflicted, so every stretch draws a chance of 1", m.Stretches) + unknown
	}
	interval := func(c Coefficient, scale func(float64) float64) string {
		return fmt.Sprintf("%.4g [%s, %s]", scale(c.Estimate), endText(scale(c.Low)), endText(scale(c.High)))
	}
	same := func(v float64) float64 { return v }
	text := fmt.Sprintf("fitted on %d stretches, %d of them conflicts: base %s, merges term %s (%g intervals), odds ratio per doubling of 1 + M %s",
		m.Stretches, m.Conflicts, interval(m.Base, same), interval(m.Term, same), m.Level, interval(m.Term, math.Exp))
	if m.TermZero {
		text += "; the branch sets the merges term to 0"
	}
	return text + unknown
}

// endText is one end of an interval as String prints it.
func endText(v float64) string {
	switch {
	case math.IsInf(v, -1):
		return "-inf"
	case math.IsInf(v, 1):
		return "+inf"
	}
	return fmt.Sprintf("%.4g", v)
}

// logistic is the logistic function, 1 / (1 + e^-x).
func logistic(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

// log1pExp is log(1 + e^x), without overflow.
func log1pExp(x float64) float64 {
	if x > 0 {
		return x + math.Log1p(math.Exp(-x))
	}
	return math.Log1p(math.Exp(x))
}

// FitConflicts fits the conflict model to stretches by maximum likelihood: Newton-Raphson on the
// two coefficients with step halving, from zero, until a step gains less than 1e-12 of
// log-likelihood. Where the stretches separate (every conflict at a larger M than every stretch
// without one) the likelihood rises to its supremum only at infinity, and the fit stops where the
// fitted chances stand within 1e-9 of 0 and 1. Each coefficient's interval is its Wald interval at
// 95% from the inverse of the observed information at the estimate.
func FitConflicts(stretches []Stretch) ConflictModel {
	m := ConflictModel{Stretches: len(stretches), Level: conflictLevel}
	x := make([]float64, len(stretches))
	y := make([]bool, len(stretches))
	for i, stretch := range stretches {
		x[i], y[i] = math.Log2(1+float64(stretch.Merges)), stretch.Conflict
		if stretch.Conflict {
			m.Conflicts++
		}
		if stretch.OwnMergeUnknown {
			m.OwnMergeUnknown++
		}
	}
	if !m.fitted() {
		return m
	}
	loglik := func(b [2]float64) float64 {
		sum := 0.0
		for i := range x {
			eta := b[0] + b[1]*x[i]
			if y[i] {
				sum += eta
			}
			sum -= log1pExp(eta)
		}
		return sum
	}
	information := func(b [2]float64) (g [2]float64, h [3]float64) {
		for i := range x {
			p := logistic(b[0] + b[1]*x[i])
			residual := -p
			if y[i] {
				residual = 1 - p
			}
			w := p * (1 - p)
			g[0] += residual
			g[1] += residual * x[i]
			h[0] += w
			h[1] += w * x[i]
			h[2] += w * x[i] * x[i]
		}
		h[0] += fitRidge
		h[2] += fitRidge
		return g, h
	}
	b := [2]float64{}
	ll := loglik(b)
	for range 1000 {
		g, h := information(b)
		det := h[0]*h[2] - h[1]*h[1]
		step := [2]float64{(h[2]*g[0] - h[1]*g[1]) / det, (h[0]*g[1] - h[1]*g[0]) / det}
		next, nextLL, scale := b, ll, 1.0
		for range 60 {
			next = [2]float64{b[0] + scale*step[0], b[1] + scale*step[1]}
			if nextLL = loglik(next); nextLL >= ll {
				break
			}
			scale /= 2
		}
		if nextLL < ll {
			break
		}
		gain := nextLL - ll
		b, ll = next, nextLL
		if gain < 1e-12 {
			break
		}
	}
	_, h := information(b)
	det := h[0]*h[2] - h[1]*h[1]
	variance := [2]float64{h[2] / det, h[0] / det}
	coefficient := func(estimate, variance float64) Coefficient {
		if variance*fitRidge > 0.5 {
			return Coefficient{Estimate: estimate, Low: math.Inf(-1), High: math.Inf(1)}
		}
		half := conflictZ * math.Sqrt(variance)
		return Coefficient{Estimate: estimate, Low: estimate - half, High: estimate + half}
	}
	m.Base, m.Term = coefficient(b[0], variance[0]), coefficient(b[1], variance[1])
	return m
}

// conflictLoop is one recorded conflict loop of a window's script: the pieces of its issue's work
// from the conflict move to the issue's return to the phase the conflict left (the return's own
// piece not among them), and the implementing round among them, the first piece of the loop in
// implementing, nil where the loop holds none.
type conflictLoop struct {
	issue  string
	pieces []*piece
	round  *piece
}

// loopAfter is the pieces of key's work from p, whose visit a move back ended, to the issue's
// return to the phase p's visit left: the pieces of the loop, and the index in pieces of the first
// piece after it, the return's (len(pieces) where the record shows no return).
func loopAfter(pieces []*piece, p *piece) ([]*piece, int) {
	for i := p.index + 1; i < len(pieces); i++ {
		next := pieces[i]
		if next.phase == p.phase && next.visit != nil && next.visit.In != nil && next.visit.In.To == p.phase {
			return pieces[p.index+1 : i], i
		}
	}
	return pieces[p.index+1:], len(pieces)
}

// recordedLoops is every recorded conflict loop of s, by issue and then time: one for each piece
// a recorded conflict move ends.
func recordedLoops(s script) []conflictLoop {
	var loops []conflictLoop
	for _, key := range slices.Sorted(maps.Keys(s.pieces)) {
		pieces := s.pieces[key]
		for _, p := range pieces {
			if move := p.move(); p.ends != endMove || move == nil || move.Trigger != workflow.TriggerConflict {
				continue
			}
			loop := conflictLoop{issue: key}
			own, _ := loopAfter(pieces, p)
			loop.pieces = slices.Clone(own)
			for _, q := range loop.pieces {
				if q.phase == phase.Implementing {
					loop.round = q
					break
				}
			}
			loops = append(loops, loop)
		}
	}
	return loops
}

// drawsAtEnd says whether p's end is the end of a stretch that draws whether its head conflicts:
// a piece in a stretch phase whose recorded move (piece.move) ends the stretch (endsStretch), the
// stretch not having drawn already (piece.stretchDrawn).
func (d *driver) drawsAtEnd(p *piece) bool {
	return !p.stretchDrawn && p.visit != nil && stretchPhase(p.phase) && endsStretch(p.move())
}

// drawConflict draws, at the replay's end of the stretch p ends, whether its head conflicts with
// its base (spec R2, assumption 3): with M the merges into the issue's base branch strictly
// inside the replay's stretch, from its start (stretchFrom) to now, but the issue's own merge, a
// draw u from the run's seeded source makes a conflict where u is below the model's chance at M; a
// stretch whose M may hold its own merge is counted (ConflictModel.DrawnOwnMergeUnknown). It
// answers whether the stretch conflicted and the daemon acted on the conflict (conflictFor), where
// the recorded move that ends the stretch is not entered.
func (d *driver) drawConflict(p *piece) (bool, error) {
	now := d.clock.Instant()
	start, standing := d.stretchFrom[p.issue]
	if !standing {
		start = p.start
	}
	repo, own := "", 0
	if iss, known := d.trace.Issues[p.issue]; known {
		repo, own = iss.Repo, ownPullRequest(iss)
	}
	merges, unknown := d.merges.between(repo, own, start, now)
	if unknown {
		d.conflicts.DrawnOwnMergeUnknown++
	}
	if d.draws.Float64() >= d.conflicts.Chance(merges) {
		return false, nil
	}
	return d.conflictFor(p)
}

// conflictFor makes the drawn conflict of p's stretch through the daemon's own path for a
// conflicting head: the replay's own pull request record of the issue (assumption 29) is read by
// GitHub as conflicting at its head, confirmed, and the engine is handed that read
// (intake.PullRequestMergeability, whose handler acts on a confirmed conflict by the rule's
// conflict path). Under the daemon's own path (record.ConflictMergeForward) the issue keeps its
// phase and the engine asks the merge forward, which the replay runs as a merge-forward segment
// (openMergeForward, assumption 23); under a rule whose conflicting head moves back, the engine
// moves the issue back to implementing by the rule's trigger, and the loop's work is drawn from the
// window's recorded conflict loops (drawLoop). A conflict the daemon does not act on is counted
// as a refused move, and the stretch ends as recorded.
func (d *driver) conflictFor(p *piece) (bool, error) {
	key := p.issue
	if err := d.ensurePullRequest(key); err != nil {
		return false, err
	}
	pr, err := d.pullRequestOf(key)
	if err != nil || pr == nil {
		return false, err
	}
	if pr.HeadSHA == "" {
		pr.HeadSHA = d.newHead(key)
	}
	pr.ConflictingHead = pr.HeadSHA
	if err := d.st.Tx(d.ctx, func(tx pgx.Tx) error { return d.records.PutPullRequest(d.ctx, tx, *pr) }); err != nil {
		return false, err
	}
	d.used[assumeNoGitHub] = true
	if _, err := d.apply("github", d.factID("conflict", key), intake.PullRequestMergeability{Repo: pr.Repo, Number: pr.Number, Base: pr.Base,
		HeadSHA: pr.HeadSHA, Mergeable: record.MergeabilityConflicting, ReadAt: d.now()}); err != nil {
		return false, err
	}
	trigger, to := workflow.TriggerConflict, phase.Implementing
	path := cmp.Or(d.rule.Conflict, record.ConflictMergeForward)
	switch path {
	case record.ConflictMergeForward:
		to = p.phase
	case record.ConflictChecksRed:
		trigger = workflow.TriggerChecksRed
	}
	refuse := func(reason string) (bool, error) {
		d.counters.Refused = append(d.counters.Refused, RefusedMove{Issue: key, At: d.clock.Instant(), Trigger: trigger, From: p.phase, To: to,
			Reason: reason})
		return false, nil
	}
	if path == record.ConflictMergeForward {
		asked, err := d.pullRequestOf(key)
		if err != nil {
			return false, err
		}
		if asked == nil || asked.MergeForward == nil || asked.MergeForward.Head != pr.HeadSHA || asked.MergeForward.Landed != "" {
			return refuse(fmt.Sprintf("the engine asked no merge forward of %s's conflicting head drawn in %s", key, p.phase))
		}
		d.used[3], d.used[23] = true, true
		d.openMergeForward(p)
		return true, nil
	}
	issue, err := d.recorded(key)
	if err != nil {
		return false, err
	}
	if issue == nil || issue.Phase != phase.Implementing {
		return refuse(fmt.Sprintf("the engine did not move %s back to implementing for its conflicting head drawn in %s", key, p.phase))
	}
	// The implementer works the conflict on a head of its own: the record's head no longer
	// conflicts, so no later move asks a merge forward of it.
	moved, err := d.pullRequestOf(key)
	if err != nil || moved == nil {
		return false, err
	}
	moved.HeadSHA, moved.ConflictingHead = d.newHead(key), ""
	if err := d.st.Tx(d.ctx, func(tx pgx.Tx) error { return d.records.PutPullRequest(d.ctx, tx, *moved) }); err != nil {
		return false, err
	}
	d.drawLoop(p)
	return true, nil
}

// newHead is a head the replay's own pull request record of key moves to, unique in the run.
func (d *driver) newHead(key string) string {
	d.heads++
	return fmt.Sprintf("replay-head-%s-%d", key, d.heads)
}

// drawnLoop is a recorded conflict loop of the window drawn with the run's seeded source, nil where
// the window records none.
func (d *driver) drawnLoop() *conflictLoop {
	if len(d.loops) == 0 {
		return nil
	}
	return &d.loops[d.draws.IntN(len(d.loops))]
}

// openMergeForward runs the merge-forward segment of p's drawn conflict under the daemon's own
// conflict path (record.ConflictMergeForward): inside the phase p worked, its implementer in a turn
// from its start to its end, as long as the implementing round of a recorded conflict loop drawn
// with the run's seed (assumption 23), and unended where that round is; a window that records no
// round gives one with no end. It follows p in the issue's work, and its end is mergedForward.
func (d *driver) openMergeForward(p *piece) {
	now := d.clock.Instant()
	forward := &piece{issue: p.issue, visit: p.visit, phase: p.phase, role: claim.RoleImplementer, opens: opensOnStart, ends: endMergeForward,
		drew: p, stretchDrawn: true, start: now, m: d.script.inFlightAt(now)}
	if loop := d.drawnLoop(); loop != nil && loop.round != nil {
		forward.start, forward.end, forward.m = loop.round.start, loop.round.end, loop.round.m
	}
	forward.acts = []act{{kind: actTurnStart, rec: forward.start, role: claim.RoleImplementer}}
	if !forward.end.IsZero() {
		forward.acts = append(forward.acts, act{kind: actTurnEnd, offset: forward.recorded(), rec: forward.end, role: claim.RoleImplementer})
	}
	d.replacePieces(p.issue, p.index+1, p.index+1, []*piece{forward})
	d.openPiece(forward, now, false)
}

// mergedForward ends a merge-forward segment: the implementer's merge of the base lands on the
// replay's own pull request record as a new head (intake.PullRequestSynchronized, whose handler
// records the merge forward landed), and the stretch that drew the conflict goes on, not drawn a
// second time: past its recorded conflict loop where the record ended it in a conflict
// (stretchEnds), and otherwise by its recorded move.
func (d *driver) mergedForward(forward *piece) error {
	pr, err := d.pullRequestOf(forward.issue)
	if err != nil {
		return err
	}
	if pr != nil {
		if _, err := d.apply("github", d.factID("merge-forward", forward.issue), intake.PullRequestSynchronized{Repo: pr.Repo, Number: pr.Number,
			Branch: pr.Branch, Before: pr.HeadSHA, HeadSHA: d.newHead(forward.issue), UpdatedAt: d.now()}); err != nil {
			return err
		}
	}
	drew := forward.drew
	if drew.move().Source.Way == MoveDraw {
		return d.stretchEnds(drew)
	}
	return d.enterRecorded(drew)
}

// drawLoop gives the issue of p, moved back to implementing by a conflict drawn at the end of p's
// stretch, the work of a conflict loop drawn with the run's seed from the window's recorded loops,
// in place of its own recorded loop where the record ended the stretch in a conflict too: the
// loop's pieces, each the drawn issue's work given to this one, up to the return to the phase the
// conflict left; where the record ended the stretch otherwise, p's own work follows the loop
// again, ending as the record ended it. A drawn loop that is the issue's own runs as recorded.
func (d *driver) drawLoop(p *piece) {
	pieces := d.script.pieces[p.issue]
	own, back := []*piece(nil), p.index+1
	recordedConflict := p.move().Source.Way == MoveDraw
	if recordedConflict {
		own, back = loopAfter(pieces, p)
	}
	loop := d.drawnLoop()
	if loop == nil || len(loop.pieces) == 0 || len(own) > 0 && own[0] == loop.pieces[0] {
		return
	}
	work := clonePieces(loop.pieces, p.issue)
	if !recordedConflict {
		work = append(work, clonePieces([]*piece{p}, p.issue)...)
	}
	for _, dropped := range pieces[p.index+1 : back] {
		dropped.state = pieceSkipped
	}
	d.replacePieces(p.issue, p.index+1, back, work)
}

// replacePieces replaces the pieces from index from up to index to of key's work with work, in a
// list of its own, each piece numbered by its new place.
func (d *driver) replacePieces(key string, from, to int, work []*piece) {
	pieces := d.script.pieces[key]
	replaced := slices.Concat(pieces[:from], work, pieces[to:])
	for i, p := range replaced {
		p.index = i
	}
	d.script.pieces[key] = replaced
}
