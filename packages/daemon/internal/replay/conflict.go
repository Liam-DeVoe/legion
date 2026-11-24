package replay

import (
	"cmp"
	"context"
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
	"github.com/sjawhar/legion/daemon/internal/classify"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// Stretch is one stretch of an issue's work in which its head can conflict with its base (spec
// R2; the fixture windows' README, "Conflicts"): one visit of the issue to the phases from testing
// to merging, from its move into testing to the move that ends the visit — its merge, a conflict or
// another move back. Phase is the phase of a stretch it ended in, the phase of the last visit of it
// that a hold did not stand in. Merges is M, the first-parent commits of the issue's base branch
// (the window's merges) strictly after Start and strictly before End but the issue's own merge —
// the commit whose pull request (Merge.PR) is the one the issue's Dispatch events link
// (IssueTrace.LinkedPullRequests) — and Conflict says a recorded conflict move ended it.
// OwnMergeUnknown says M holds a commit that may be the issue's own merge: the issue links no pull
// request of its repository, or a commit inside the stretch names none. It is one observation of
// the conflict model (FitConflicts).
type Stretch struct {
	Issue           string      `json:"issue"`
	Phase           phase.Phase `json:"phase"`
	Start           time.Time   `json:"start"`
	End             time.Time   `json:"end"`
	Merges          int         `json:"merges"`
	Conflict        bool        `json:"conflict"`
	OwnMergeUnknown bool        `json:"ownMergeUnknown"`
}

// conflictBasePhase is the phase whose stretches the conflict model's base stands for, every other
// phase a stretch can end in taking a term beside it (conflictPhases): merging, where every stretch
// that ends in a merge ends, so a window that merges anything gives the base stretches of its own.
const conflictBasePhase = phase.Merging

// conflictPhases is each phase a stretch can end in (stretchPhase) but the base's
// (conflictBasePhase), in phase.Order: the phases the conflict model takes a term for beside its
// base.
var conflictPhases = slices.DeleteFunc(slices.Clone(phase.Order), func(p phase.Phase) bool {
	return !stretchPhase(p) || p == conflictBasePhase
})

// stretchPhase says whether p is a phase of a stretch: a phase in which the issue's pull request
// stands on a head that can conflict with its base, testing through merging, each after
// implementing (pastImplementing).
func stretchPhase(p phase.Phase) bool {
	return pastImplementing(p)
}

// breaksStretch says whether p, a phase an issue moves into or stands in, ends the stretch it
// stood in: a phase outside the stretch's that is not a hold, which a stretch goes on through.
func breaksStretch(p phase.Phase) bool {
	return !stretchPhase(p) && p != phase.Held
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
	return phase.MovesBack(from, to) || breaksStretch(from)
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
	after := upTo(r.at, start)
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
// by issue and then time, with the phase it ended in and its M counted from merges. A stretch the
// record shows open at the window's end, or cut by its issue's leave, has no outcome, and one whose
// start lies before the inputs reach (Visit.Before) has no known M; neither is an observation of
// the model, which is fitted on the stretches inside the window alone (windowConflicts). A merge
// whose close waited for the issues beneath it ends its stretch at the merge (Visit.Merge), not at
// the close's later move to done, nor open where that move falls after the window's end.
func recordedStretches(t Trace, merges baseMerges) []Stretch {
	var stretches []Stretch
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		iss := t.Issues[key]
		if iss.Project != t.Project {
			continue
		}
		own := ownPullRequest(iss)
		open, known, in := time.Time{}, false, phase.Phase("")
		for i := range iss.Visits {
			visit := &iss.Visits[i]
			if open.IsZero() {
				if !stretchPhase(visit.Phase) {
					continue
				}
				open, known = visit.Enter, !visit.Before
			}
			if stretchPhase(visit.Phase) {
				in = visit.Phase
			}
			out := visit.Out
			if visit.Merge != nil {
				out = visit.Merge
			}
			switch {
			case breaksStretch(visit.Phase), out == nil:
				open = time.Time{}
			case endsStretch(out):
				if known {
					count, unknown := merges.between(iss.Repo, own, open, out.At)
					stretches = append(stretches, Stretch{Issue: key, Phase: in, Start: open, End: out.At, Merges: count,
						Conflict: out.Trigger == workflow.TriggerConflict, OwnMergeUnknown: unknown})
				}
				open = time.Time{}
			}
		}
	}
	return stretches
}

// windowConflicts is the conflict model of t's window (spec R2; plan D3, which fits the rate on
// one window alone): fitted on the recorded stretches (recordedStretches) that start at or after
// the window's start and end before its end, with M counted from merges, and that span. A
// stretch that starts in the reach before the window is left out: the reach may have run another
// release, and one with no conflict trigger records a conflict as another move back, so each of
// its stretches would enter the fit as one without a conflict.
func windowConflicts(t Trace, merges baseMerges) ConflictModel {
	inside := slices.DeleteFunc(recordedStretches(t, merges), func(s Stretch) bool {
		return s.Start.Before(t.From) || !s.End.Before(t.To)
	})
	m := FitConflicts(inside)
	m.From, m.To = t.From, t.To
	return m
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
		case breaksStretch(visit.Phase):
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
// never varies, or no stretch ended in a phase) is infinite, and its JSON value is null: a low end
// of null is minus infinity and a high end of null plus infinity.
type Coefficient struct {
	Estimate float64
	Low      float64
	High     float64
}

// coefficientJSON is a Coefficient as JSON holds it, null for an infinite end.
type coefficientJSON struct {
	Estimate *float64 `json:"estimate"`
	Low      *float64 `json:"low"`
	High     *float64 `json:"high"`
}

// MarshalJSON writes c with null for an infinite end, which JSON has no number for.
func (c Coefficient) MarshalJSON() ([]byte, error) {
	end := func(v float64) *float64 {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil
		}
		return &v
	}
	return json.Marshal(coefficientJSON{end(c.Estimate), end(c.Low), end(c.High)})
}

// UnmarshalJSON reads c as MarshalJSON writes it: a low end of null is minus infinity, a high end
// of null plus infinity, and an estimate of null is not a number.
func (c *Coefficient) UnmarshalJSON(data []byte) error {
	var read coefficientJSON
	if err := json.Unmarshal(data, &read); err != nil {
		return err
	}
	end := func(v *float64, none float64) float64 {
		if v == nil {
			return none
		}
		return *v
	}
	*c = Coefficient{Estimate: end(read.Estimate, math.NaN()), Low: end(read.Low, math.Inf(-1)), High: end(read.High, math.Inf(1))}
	return nil
}

// PhaseFit is the conflict model's reading of one phase a stretch can end in: how many of the
// stretches the model was fitted on ended in it, how many of those in a conflict, and the phase's
// term beside the base, nil for the base's own phase (conflictBasePhase), for a phase that takes no
// term (termedPhases) and where no fit was made.
type PhaseFit struct {
	Stretches int          `json:"stretches"`
	Conflicts int          `json:"conflicts"`
	Term      *Coefficient `json:"term,omitempty"`
}

// ConflictModel is the chance that a stretch of an issue's work ends in a conflict, fitted on the
// recorded stretches inside a window (windowConflicts; spec R2; plan D3: only window one records
// conflict moves, so only it fits a chance above zero; plan D7): logistic(Base + P + Term × log2(1 +
// M)), P the term of the phase the stretch ends in, and M the merges into the stretch's base branch
// during it, the coefficients fitted together by maximum likelihood with their intervals. The base
// stands for the stretches that end in merging (conflictBasePhase) and in each phase that takes no
// term (termedPhases), for which P is zero. A rule moves the chance through the merges term alone,
// since every stretch ends in the phase the record shows it ending in. A window that records no
// conflict fits a chance of 0 for every stretch, and one whose every stretch conflicted a chance of
// 1, without a fit.
type ConflictModel struct {
	// From and To are the span of the window the model was fitted on: each of its stretches began
	// at or after From and ended before To. Both are zero for a fit of stretches no window gave.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Stretches and Conflicts are the stretches the model was fitted on and how many ended in a
	// conflict.
	Stretches int `json:"stretches"`
	Conflicts int `json:"conflicts"`
	// Base and Term are the base and the merges term, zero where no fit was made, and Phases each
	// phase a stretch can end in (stretchPhase), its stretches, conflicts and term.
	Base   Coefficient              `json:"base"`
	Term   Coefficient              `json:"term"`
	Phases map[phase.Phase]PhaseFit `json:"phases"`
	// Level is the level of the coefficients' intervals.
	Level float64 `json:"level"`
	// TermZero says the merges term is set to zero (the branch's Merges zero), the base and the
	// phase terms kept.
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

// Chance is the chance that a stretch that ends in in, with merges merges into its base branch,
// ends in a conflict. A phase the model holds no term of draws at the base.
func (m ConflictModel) Chance(in phase.Phase, merges int) float64 {
	switch {
	case m.Conflicts == 0:
		return 0
	case !m.fitted():
		return 1
	}
	eta := m.Base.Estimate
	if term := m.Phases[in].Term; term != nil {
		eta += term.Estimate
	}
	if !m.TermZero {
		eta += m.Term.Estimate * math.Log2(1+float64(merges))
	}
	return logistic(eta)
}

// WithoutTerm is m with its merges term set to zero (Branch.Merges zero, assumption 3): every
// stretch draws at the chance its phase gives, however many merges its base took.
func (m ConflictModel) WithoutTerm() ConflictModel {
	m.TermZero = true
	return m
}

// String is the model as a run prints it: the span and the stretches and conflicts it was fitted
// on, each coefficient with its interval and the stretches of each phase (terms), and the stretches
// whose M may hold their own merge.
func (m ConflictModel) String() string {
	span := fmt.Sprintf("began and ended inside the window from %s to %s", stamp(m.From), stamp(m.To))
	unknown := fmt.Sprintf("; %d of the window's stretches and %d of the replay's counted a merge that may be their own", m.OwnMergeUnknown,
		m.DrawnOwnMergeUnknown)
	switch {
	case m.Stretches == 0:
		return fmt.Sprintf("no stretch %s, so no conflict is drawn", span) + unknown
	case m.Conflicts == 0:
		return fmt.Sprintf("none of the %d stretches that %s conflicted, so every stretch draws a chance of 0", m.Stretches, span) + unknown
	case !m.fitted():
		return fmt.Sprintf("all of the %d stretches that %s conflicted, so every stretch draws a chance of 1", m.Stretches, span) + unknown
	}
	text := fmt.Sprintf("fitted on the %d stretches that %s, %d of them conflicts: %s", m.Stretches, span, m.Conflicts, m.terms())
	if m.TermZero {
		text += "; the branch sets the merges term to 0 and keeps the phase terms"
	}
	return text + unknown
}

// terms is the fitted coefficients as a run and the comparison print them, each with its interval
// at the model's level: the base, with its phase and the stretches and conflicts of that phase;
// each other phase's term, with the stretches and conflicts of that phase; and the merges term,
// with the odds ratio per doubling of 1 + M.
func (m ConflictModel) terms() string {
	interval := func(c Coefficient, scale func(float64) float64) string {
		return fmt.Sprintf("%.4g [%s, %s]", scale(c.Estimate), endText(scale(c.Low)), endText(scale(c.High)))
	}
	same := func(v float64) float64 { return v }
	var text strings.Builder
	base := m.Phases[conflictBasePhase]
	fmt.Fprintf(&text, "base %s (%s: stretches %d, conflicts %d)", interval(m.Base, same), conflictBasePhase, base.Stretches, base.Conflicts)
	for _, in := range conflictPhases {
		fit, term := m.Phases[in], "none"
		if fit.Term != nil {
			term = interval(*fit.Term, same)
		}
		fmt.Fprintf(&text, ", %s term %s (stretches %d, conflicts %d)", in, term, fit.Stretches, fit.Conflicts)
	}
	fmt.Fprintf(&text, ", merges term %s, odds ratio per doubling of 1 + M %s (%g intervals)", interval(m.Term, same),
		interval(m.Term, math.Exp), m.Level)
	return text.String()
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
// base, the merges term and the term of each phase that takes one (termedPhases), together, with
// step halving, from zero, until a step gains less than 1e-12 of log-likelihood. Where the
// stretches separate (every conflict at a larger M, or in another phase, than every stretch without
// one) the likelihood rises to its supremum only at infinity, and the fit stops where the fitted
// chances stand within 1e-9 of 0 and 1. Each coefficient's interval is its Wald interval at 95% from
// the inverse of the observed information at the estimate, with no end for a coefficient the
// stretches do not identify.
func FitConflicts(stretches []Stretch) ConflictModel {
	m := ConflictModel{Stretches: len(stretches), Level: conflictLevel, Phases: make(map[phase.Phase]PhaseFit, len(conflictPhases)+1)}
	m.Phases[conflictBasePhase] = PhaseFit{}
	for _, in := range conflictPhases {
		m.Phases[in] = PhaseFit{}
	}
	for _, stretch := range stretches {
		if fit, read := m.Phases[stretch.Phase]; read {
			fit.Stretches++
			if stretch.Conflict {
				fit.Conflicts++
			}
			m.Phases[stretch.Phase] = fit
		}
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
	// The coefficients are the base (0), the merges term (1) and the term of each phase that takes
	// one (2 on), and column is the coefficient of each such phase's term.
	termed := m.termedPhases()
	column := make(map[phase.Phase]int, len(termed))
	for j, in := range termed {
		column[in] = 2 + j
	}
	k := 2 + len(termed)
	x := make([]float64, len(stretches))
	col := make([]int, len(stretches))
	y := make([]bool, len(stretches))
	for i, stretch := range stretches {
		x[i], col[i], y[i] = math.Log2(1+float64(stretch.Merges)), column[stretch.Phase], stretch.Conflict
	}
	eta := func(b []float64, i int) float64 {
		v := b[0] + b[1]*x[i]
		if col[i] > 0 {
			v += b[col[i]]
		}
		return v
	}
	loglik := func(b []float64) float64 {
		sum := 0.0
		for i := range x {
			v := eta(b, i)
			if y[i] {
				sum += v
			}
			sum -= log1pExp(v)
		}
		return sum
	}
	// information writes the gradient of the log-likelihood at b into g and its observed
	// information into h, a symmetric matrix of order k by rows, the ridge on its diagonal.
	information := func(b, g, h []float64) {
		clear(g)
		clear(h)
		for i := range x {
			p := logistic(eta(b, i))
			residual := -p
			if y[i] {
				residual = 1 - p
			}
			w := p * (1 - p)
			g[0] += residual
			g[1] += residual * x[i]
			h[0] += w
			h[1] += w * x[i]
			h[k+1] += w * x[i] * x[i]
			if c := col[i]; c > 0 {
				g[c] += residual
				h[c] += w
				h[k+c] += w * x[i]
				h[c*k+c] += w
			}
		}
		for i := range k {
			for j := range i {
				h[i*k+j] = h[j*k+i]
			}
			h[i*k+i] += fitRidge
		}
	}
	b, next, step := make([]float64, k), make([]float64, k), make([]float64, k)
	g, h, l := make([]float64, k), make([]float64, k*k), make([]float64, k*k)
	ll := loglik(b)
	for range 1000 {
		information(b, g, h)
		cholesky(h, l, k)
		choleskySolve(l, k, g, step)
		nextLL, scale := ll, 1.0
		for range 60 {
			for j := range b {
				next[j] = b[j] + scale*step[j]
			}
			if nextLL = loglik(next); nextLL >= ll {
				break
			}
			scale /= 2
		}
		if nextLL < ll {
			break
		}
		gain := nextLL - ll
		copy(b, next)
		ll = nextLL
		if gain < 1e-12 {
			break
		}
	}
	information(b, g, h)
	cholesky(h, l, k)
	coefficient := func(j int) Coefficient {
		clear(g)
		g[j] = 1
		choleskySolve(l, k, g, step)
		if variance := step[j]; variance*fitRidge <= 0.5 {
			half := conflictZ * math.Sqrt(variance)
			return Coefficient{Estimate: b[j], Low: b[j] - half, High: b[j] + half}
		}
		return Coefficient{Estimate: b[j], Low: math.Inf(-1), High: math.Inf(1)}
	}
	m.Base, m.Term = coefficient(0), coefficient(1)
	for j, in := range termed {
		fit, term := m.Phases[in], coefficient(2+j)
		fit.Term = &term
		m.Phases[in] = fit
	}
	return m
}

// termedPhases is the phases of conflictPhases that take a term of their own in the fit, in their
// order: each whose stretches hold a conflict and a stretch without one, so its own rate is known
// apart from the merges, where the stretches no such term reads, which the base stands for, hold
// both as well. Where they do not, no phase takes a term, since the base would have no stretches of
// its own to stand for. The stretches of every phase that takes no term draw at the base, as a
// stretch of no phase a stretch ends in (one built with none) does.
func (m ConflictModel) termedPhases() []phase.Phase {
	var termed []phase.Phase
	stretches, conflicts := m.Stretches, m.Conflicts
	for _, in := range conflictPhases {
		if fit := m.Phases[in]; fit.Conflicts > 0 && fit.Conflicts < fit.Stretches {
			termed = append(termed, in)
			stretches, conflicts = stretches-fit.Stretches, conflicts-fit.Conflicts
		}
	}
	if conflicts == 0 || conflicts == stretches {
		return nil
	}
	return termed
}

// cholesky writes into l, by rows, the lower Cholesky factor of h, a symmetric matrix of order k
// that the fit's ridge keeps positive definite. A pivot at or below zero, which rounding alone can
// leave on such a matrix, is taken as the ridge.
func cholesky(h, l []float64, k int) {
	clear(l)
	for i := range k {
		for j := 0; j <= i; j++ {
			sum := h[i*k+j]
			for p := range j {
				sum -= l[i*k+p] * l[j*k+p]
			}
			switch {
			case i != j:
				l[i*k+j] = sum / l[j*k+j]
			case sum > 0:
				l[i*k+i] = math.Sqrt(sum)
			default:
				l[i*k+i] = math.Sqrt(fitRidge)
			}
		}
	}
}

// choleskySolve writes into z the solution of l lᵀ z = g, l a lower Cholesky factor of order k by
// rows (cholesky).
func choleskySolve(l []float64, k int, g, z []float64) {
	for i := range k {
		sum := g[i]
		for p := range i {
			sum -= l[i*k+p] * z[p]
		}
		z[i] = sum / l[i*k+i]
	}
	for i := k - 1; i >= 0; i-- {
		sum := z[i]
		for p := i + 1; p < k; p++ {
			sum -= l[p*k+i] * z[p]
		}
		z[i] = sum / l[i*k+i]
	}
}

// conflictLoop is one recorded conflict loop of a window's script: the phase its conflict move
// left, the pieces of its issue's work from the conflict move to the issue's return to that phase
// (the return's own piece not among them), whether the record shows that return (returned), and
// the implementing round among them, the first piece of the loop in implementing, nil where the
// loop holds none.
type conflictLoop struct {
	issue    string
	left     phase.Phase
	pieces   []*piece
	returned bool
	round    *piece
}

// endsIn says whether the work a conflict drawn in left takes from loop ends as the record shows
// it: the loop's pieces up to the issue's return to the phase its conflict left, or, for a loop
// that left a phase after left, up to its first piece in left or a phase after it (returnTo), the
// record showing that return, each piece's end and the end of the loop's implementing round, which
// a merge forward takes whole. A loop the record shows no return from, its issue still in it at
// the window's end or its last piece cut by the issue's leave, has work of no known length: an
// issue given it would stand in its last piece to the window's end, and the issue's own work after
// the loop would never run.
func (loop *conflictLoop) endsIn(left phase.Phase) bool {
	work, returns := loop.pieces, loop.returned
	if loopSide(loop, left) == leftLater {
		if cut := returnTo(work, left); len(cut) < len(work) {
			work, returns = cut, true
		}
	}
	unended := func(p *piece) bool { return p.end.IsZero() }
	return returns && !slices.ContainsFunc(work, unended) && (loop.round == nil || !unended(loop.round))
}

// loopAfter is the pieces of key's work from p, whose visit a move back ended, to the issue's
// return to the phase p's visit left: the pieces of the loop, and the index in pieces of the first
// piece after it, the return's (len(pieces) where the record shows no return).
func loopAfter(pieces []*piece, p *piece) ([]*piece, int) {
	for i := p.index + 1; i < len(pieces); i++ {
		if pieces[i].returnsTo(p.phase) {
			return pieces[p.index+1 : i], i
		}
	}
	return pieces[p.index+1:], len(pieces)
}

// returnsTo says whether q is the work of an issue's return to left, the phase a loop's move back
// left: a piece in left whose recorded visit a move into left began.
func (q *piece) returnsTo(left phase.Phase) bool {
	return q.phase == left && q.visit != nil && q.visit.In != nil && q.visit.In.To == left
}

// implementingRound is the first of pieces in implementing, the implementing round of a conflict
// loop's work; nil where none is.
func implementingRound(pieces []*piece) *piece {
	if i := slices.IndexFunc(pieces, func(q *piece) bool { return q.phase == phase.Implementing }); i >= 0 {
		return pieces[i]
	}
	return nil
}

// recordedLoops is every recorded conflict loop of s, by issue and then time: one for each piece
// a recorded conflict move ends, with whether the record shows its issue's return.
func recordedLoops(s script) []conflictLoop {
	var loops []conflictLoop
	for _, key := range slices.Sorted(maps.Keys(s.pieces)) {
		pieces := s.pieces[key]
		for _, p := range pieces {
			if move := p.move(); p.ends != endMove || move == nil || move.Trigger != workflow.TriggerConflict {
				continue
			}
			loop := conflictLoop{issue: key, left: p.phase}
			own, back := loopAfter(pieces, p)
			loop.pieces, loop.returned = slices.Clone(own), back < len(pieces)
			loop.round = implementingRound(loop.pieces)
			loops = append(loops, loop)
		}
	}
	return loops
}

// drawsAtEnd says whether p's end is the end of a stretch that draws whether its head conflicts:
// a piece in a stretch phase whose recorded move (piece.move) ends the stretch (endsStretch), the
// stretch not having drawn already (piece.stretchDrawn).
func (d *driver) drawsAtEnd(p *piece) bool {
	return !p.stretchDrawn && stretchPhase(p.phase) && endsStretch(p.move())
}

// drawConflict draws, at the replay's end of the stretch p ends, whether its head conflicts with
// its base (spec R2, assumption 3): with M the merges into the issue's base branch strictly
// inside the replay's stretch, from its start (stretchFrom) to now, but the issue's own merge, a
// draw u from the run's seeded source makes a conflict where u is below the model's chance in p's
// phase, the phase the record shows the stretch ending in, at M; a stretch whose M may hold its own
// merge is counted (ConflictModel.DrawnOwnMergeUnknown). It answers whether the stretch conflicted
// and the daemon acted on the conflict (conflictFor), which then plays what the daemon made of it,
// the recorded move that ends the stretch not entered.
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
	if d.draws.Float64() >= d.conflicts.Chance(p.phase, merges) {
		return false, nil
	}
	return d.conflictFor(p, false)
}

// keepsOpenLoop says whether p ends its stretch in a recorded conflict whose loop the record shows
// running to the window's end (assumption 35): its issue's work after p holds no return to the
// phase p left (loopAfter), so the record holds no work of the issue after the stretch but that
// loop. A drawn conflict would give the issue another loop in its place, and a stretch that drew
// none would go on past the loop: either way the issue would be left with no recorded work, idle
// to the window's end. Such a stretch keeps its conflict, and is not drawn (keepConflict).
func (d *driver) keepsOpenLoop(p *piece) bool {
	if move := p.move(); move == nil || move.Source.Way != MoveDraw || !stretchPhase(p.phase) {
		return false
	}
	pieces := d.script.pieces[p.issue]
	_, back := loopAfter(pieces, p)
	return back == len(pieces)
}

// keepConflict makes the recorded conflict that ends p's stretch, whose loop the record shows
// running to the window's end (keepsOpenLoop, assumption 35), through the daemon's own path for a
// conflicting head (conflictFor), the stretch not drawn: under a rule whose conflicting head moves
// back, from testing, reviewing, accepting, retro or merging, the issue runs its own loop as
// recorded; under one whose head keeps its phase the merge-forward segment of the role asked
// (record.SlotRule.MergeForwardAsker) lasts as that loop's implementing round, unended where the
// round is; and under main's path in accepting or retro, where the conflict waits for merging
// (assumption 37), the issue stands in its phase with no recorded work to the window's end, the
// record holding none after the stretch but the loop. Each conflict kept is counted
// (Divergence.KeptOpenLoops). A conflict the daemon does not act on is counted as a refused move,
// and the stretch ends as recorded.
func (d *driver) keepConflict(p *piece) error {
	conflicted, err := d.conflictFor(p, true)
	if err != nil {
		return err
	}
	if !conflicted {
		return d.enterRecorded(p)
	}
	d.counters.KeptOpenLoops++
	d.used[assumeKeptOpenLoop] = true
	return nil
}

// conflictFor makes the conflict of p's stretch, drawn or, where kept, its recorded one
// (keepConflict), through the daemon's own path for a conflicting head, and plays what the daemon
// made of it: the replay's own pull request record of the issue (assumption 29) is read by GitHub
// as conflicting at its head, confirmed, and the engine is handed that read
// (intake.PullRequestMergeability, whose handler decides by classify.ConflictNeedsMergeUnder and
// acts by the rule's conflict path, record.SlotRule.Conflict). Past the review the path is the
// rule's own too: main's and the candidates built on it answer as cybermonklabs/legion#4979 has
// it, which a daemon before it, the one that recorded a window of legion-v20.42.5 or
// legion-v19.57.3, did not (assumption 37):
//
//   - Under a rule whose conflicting head moves back, in testing, reviewing, accepting, retro and
//     merging, the engine moves the issue back to implementing by the rule's trigger (workflow's
//     conflictMovesBack, whose rows leave each of those phases), as the tables of 958173da and
//     9542c6fb did: the implementer works on a head of its own (headOfItsOwn), and the loop's work
//     is drawn from the window's recorded conflict loops (drawLoop), or, where kept, is the issue's
//     own loop as recorded.
//   - Under a rule whose conflicting head keeps its phase, the engine asks a role to merge the base
//     forward (record.SlotRule.MergeForwardAsker): the issue keeps its phase, and the role asked
//     runs a merge-forward segment (openMergeForward, assumption 23). Under main's path
//     (record.ConflictMergeForward) that is the implementer in testing and reviewing and the merger
//     in merging; under legion-v22.14.1's (record.ConflictImplementerMergesForward) it is the
//     implementer in every phase, accepting and retro too, as at bafd3618.
//   - Under main's path, in accepting and retro, the engine records the head conflicting and asks
//     nobody (record.SlotRule.ConflictWaitsForMerging): neither the architect's acceptance nor retro
//     needs a head that merges, so the conflict waits for merging, where the issue's move asks the
//     merger to merge the base forward (workflow's mergeForwardOnEntry). The issue keeps its phase
//     and its work goes on (waitForMerging).
//
// A conflict the replay so leaves to the merger, in merging or waiting for it, is counted
// (Divergence.ConflictsToMerger). A conflict the daemon does not act on is counted as a refused
// move, and the stretch ends as recorded. Any conflict that waited for merging before this one is
// over: this one's path decides what follows.
func (d *driver) conflictFor(p *piece, kept bool) (bool, error) {
	key := p.issue
	delete(d.waiting, key)
	pr, err := d.ensurePullRequest(key)
	if err != nil || pr == nil {
		return false, err
	}
	if pr.HeadSHA == "" {
		pr.HeadSHA = d.newHead(key)
	}
	pr.ConflictingHead = pr.HeadSHA
	if err := store.Transact(d.ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error { return d.records.PutPullRequest(ctx, tx, *pr) }); err != nil {
		return false, err
	}
	d.used[assumeNoGitHub] = true
	if _, err := d.apply("github", d.factID("conflict", key), intake.PullRequestMergeability{Repo: pr.Repo, Number: pr.Number, Base: pr.Base,
		HeadSHA: pr.HeadSHA, Mergeable: record.MergeabilityConflicting, ReadAt: d.now()}); err != nil {
		return false, err
	}
	var issue *record.Issue
	var after *record.PullRequest
	if err := store.TransactWith(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if issue, err = d.records.Issue(ctx, tx, key); err != nil {
			return err
		}
		after, err = d.records.PullRequest(ctx, tx, key)
		return err
	}); err != nil {
		return false, err
	}
	switch {
	case issue == nil || after == nil:
	case issue.Phase == phase.Implementing:
		if err := d.moveToOwnHead(key, after); err != nil {
			return false, err
		}
		if !kept {
			d.drawLoop(p)
		}
		return true, nil
	case after.MergeForward != nil && after.MergeForward.Head == pr.HeadSHA && after.MergeForward.Landed == "":
		asked := after.MergeForward.Asked()
		d.used[3], d.used[23] = true, true
		if asked == claim.RoleMerger {
			d.counters.ConflictsToMerger++
			d.used[assumeConflictToMerger] = true
		}
		d.openMergeForward(p, kept, asked)
		return true, nil
	case record.StandsIn(*issue, phase.Accepting, phase.Retro) && classify.ConflictRound(*after) && d.rule.ConflictWaitsForMerging():
		d.used[3] = true
		d.counters.ConflictsToMerger++
		d.used[assumeConflictToMerger] = true
		return true, d.waitForMerging(p, kept)
	}
	trigger, to := workflow.TriggerConflict, p.phase
	switch d.rule.Conflict {
	case record.ConflictMovesBack:
		to = phase.Implementing
	case record.ConflictChecksRed:
		trigger, to = workflow.TriggerChecksRed, phase.Implementing
	}
	made := "drawn"
	if kept {
		made = "kept"
	}
	reason := fmt.Sprintf("the engine did not move %s back to implementing for its conflicting head %s in %s", key, made, p.phase)
	if to == p.phase {
		reason = fmt.Sprintf("the engine asked no merge forward of %s's conflicting head %s in %s", key, made, p.phase)
	}
	d.counters.Refused = append(d.counters.Refused, RefusedMove{Issue: key, At: d.clock.Instant(), Trigger: trigger, From: p.phase, To: to,
		Reason: reason})
	return false, nil
}

// headOfItsOwn moves the replay's own pull request record of key to a head of its own, which no
// confirmed conflict holds (record.PullRequest.ConflictingHead): the implementer works on from it,
// so no later move asks a merge forward of the head that conflicted.
func (d *driver) headOfItsOwn(key string) error {
	moved, err := d.pullRequestOf(key)
	if err != nil {
		return err
	}
	return d.moveToOwnHead(key, moved)
}

// moveToOwnHead is headOfItsOwn of moved, key's record as just read, nil for none, which moves
// nothing.
func (d *driver) moveToOwnHead(key string, moved *record.PullRequest) error {
	if moved == nil {
		return nil
	}
	moved.HeadSHA, moved.ConflictingHead = d.newHead(key), ""
	return store.Transact(d.ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error { return d.records.PutPullRequest(ctx, tx, *moved) })
}

// newHead is a head the replay's own pull request record of key moves to, unique in the run.
func (d *driver) newHead(key string) string {
	d.heads++
	return fmt.Sprintf("replay-head-%s-%d", key, d.heads)
}

// The sides of the phase a recorded loop's conflict left from the phase of a drawn conflict
// (loopSide), in the order the drawn conflict draws its loop from them: the same phase, a later
// one in phase.Order, an earlier one.
const (
	leftSame = iota
	leftLater
	leftEarlier
	loopSides
)

// loopSide is the side of the phase loop's conflict left from left.
func loopSide(loop *conflictLoop, left phase.Phase) int {
	switch cmp.Compare(slices.Index(phase.Order, loop.left), slices.Index(phase.Order, left)) {
	case 0:
		return leftSame
	case 1:
		return leftLater
	}
	return leftEarlier
}

// drawnLoop is a recorded conflict loop of the window drawn with the run's seeded source for a
// conflict drawn in left, among the loops whose work the draw takes ends as the record shows it
// (conflictLoop.endsIn): drawn among the loops whose conflict left the same phase, the record's
// work of a conflict there; where the window records none from it, among those that left a later
// phase; and where it records none of those either, among those that left an earlier one. Nil
// where it records none at all.
func (d *driver) drawnLoop(left phase.Phase) *conflictLoop {
	var sides [loopSides][]int
	for i := range d.loops {
		if d.loops[i].endsIn(left) {
			side := loopSide(&d.loops[i], left)
			sides[side] = append(sides[side], i)
		}
	}
	for _, candidates := range sides {
		if len(candidates) > 0 {
			return &d.loops[candidates[d.draws.IntN(len(candidates))]]
		}
	}
	return nil
}

// mergeForwardRound is the recorded implementing round the merge forward of the conflict that ends
// p's stretch lasts as (assumption 23): the implementing round of a recorded conflict loop drawn
// with the run's seed among the loops that left p's phase whose work the record shows ending
// (drawnLoop), or, for a conflict kept (keepConflict, assumption 35), the round of the issue's own
// loop, unended where that round is; nil where the window records no such round, which gives a
// merge forward with no end. The phase chooses the round as it chooses a moved-back conflict's loop,
// so a conflict in a phase costs either conflict path what the record's conflicts in that phase
// cost, and the two paths replayed on one window draw their conflicts' work from the same loops. A
// loop drawn from another phase, where the window records none from that phase, gives its round
// whole: a merge forward reads the round alone, which comes before every phase a stretch draws in.
func (d *driver) mergeForwardRound(p *piece, kept bool) *piece {
	if kept {
		own, _ := loopAfter(d.script.pieces[p.issue], p)
		return implementingRound(own)
	}
	if loop := d.drawnLoop(p.phase); loop != nil {
		return loop.round
	}
	return nil
}

// mergeForwardPiece is a merge-forward segment of the work of drew's issue in at, of role, the role
// the engine asked to merge the base forward (record.MergeForward.Asked), for the conflict that
// ends drew's stretch: role's worker in a turn from its start to its end, as long as round
// (mergeForwardRound), with no end where round is nil. visit is the recorded visit it stands in,
// nil for none. Its end is mergedForward.
func (d *driver) mergeForwardPiece(drew *piece, visit *Visit, at phase.Phase, role claim.Role, round *piece) *piece {
	now := d.clock.Instant()
	forward := &piece{issue: drew.issue, visit: visit, phase: at, role: role, opens: opensOnStart, ends: endMergeForward,
		drew: drew, stretchDrawn: true, start: now, m: d.script.inFlightAt(now)}
	if round != nil {
		forward.start, forward.end, forward.m = round.start, round.end, round.m
	}
	forward.acts = []act{{kind: actTurnStart, rec: forward.start, role: role}}
	if !forward.end.IsZero() {
		forward.acts = append(forward.acts, act{kind: actTurnEnd, offset: forward.recorded(), rec: forward.end, role: role})
	}
	return forward
}

// openMergeForward runs the merge-forward segment of p's conflict where the engine asked role to
// merge the base forward at once (conflictFor): inside the phase p worked, the role the rule asks
// there (record.SlotRule.MergeForwardAsker: under main the implementer's in testing and reviewing
// and the merger's in merging, and under legion-v22.14.1 the implementer's in every phase), as
// long as the conflict's round (mergeForwardRound). It follows p in the issue's work, and opens
// now.
func (d *driver) openMergeForward(p *piece, kept bool, role claim.Role) {
	forward := d.mergeForwardPiece(p, p.visit, p.phase, role, d.mergeForwardRound(p, kept))
	d.replacePieces(p.issue, p.index+1, p.index+1, []*piece{forward})
	d.openPiece(forward, d.clock.Instant(), false)
}

// waitingConflict is a conflict past the review the daemon leaves to wait for merging under main's
// path (assumption 37): one made in accepting or retro, where the engine records the head
// conflicting and asks nobody (classify.ConflictNeedsMerge, record.SlotRule.ConflictWaitsForMerging),
// until the issue's move into merging asks its merger to merge the base forward (workflow's
// mergeForwardOnEntry). drew is the piece whose stretch made it, and round the recorded
// implementing round the merger's merge forward lasts as, drawn when the conflict came
// (mergeForwardRound).
type waitingConflict struct {
	drew  *piece
	round *piece
}

// waitForMerging plays a conflict made at the end of p's stretch in accepting or retro, which the
// daemon leaves to wait for merging (conflictFor): the issue keeps its phase, and its work goes on
// as though the stretch had drawn no conflict, past the recorded loop where the record ended the
// stretch in a conflict (stretchEnds), and otherwise by its recorded move (enterRecorded). The
// conflict waits for the issue's move into merging (mergerMergesForward), unless a move back to a
// phase before accepting ends the stretch first (waitMoves); the round of the merger's merge
// forward is drawn now, when the conflict comes, as an immediate merge forward's is.
func (d *driver) waitForMerging(p *piece, kept bool) error {
	d.waiting[p.issue] = waitingConflict{drew: p, round: d.mergeForwardRound(p, kept)}
	return d.enterRecorded(p)
}

// waitMoves follows a conflict of key that waits for merging (waitingConflict) through the issue's
// move into to: it waits on through accepting, retro, merging and a hold, and is over at the move
// to done. A move into an earlier phase ends the stretch that made it before any merger was asked,
// and the work goes on from a head of its own (headOfItsOwn), as after a conflict's move back: the
// stretch that follows draws its own conflict.
func (d *driver) waitMoves(key string, to phase.Phase) error {
	if _, waits := d.waiting[key]; !waits {
		return nil
	}
	switch to {
	case phase.Accepting, phase.Retro, phase.Merging, phase.Held:
		return nil
	case phase.Done:
		delete(d.waiting, key)
		return nil
	}
	delete(d.waiting, key)
	return d.headOfItsOwn(key)
}

// mergerMergesForward plays the merger's merge forward of key's conflict that waited for merging
// (waitingConflict), where the engine has just asked the merger for it on the issue's move into
// merging (workflow's mergeForwardOnEntry): a merge-forward segment of the merger's in merging, as
// long as the conflict's round, put before the issue's next work in merging, so the merger's start
// the move wrote opens it (startPiece). It stands in no recorded visit. Its end lands the merge
// forward, and that next work opens then (mergedForward).
func (d *driver) mergerMergesForward(key string) {
	waiting, waits := d.waiting[key]
	if !waits {
		return
	}
	delete(d.waiting, key)
	d.used[23] = true
	pieces := d.script.pieces[key]
	at := slices.IndexFunc(pieces, func(q *piece) bool { return q.waitsForStart(claim.RoleMerger, phase.Merging) })
	if at < 0 {
		at = len(pieces)
	}
	d.replacePieces(key, at, at, []*piece{d.mergeForwardPiece(waiting.drew, nil, phase.Merging, claim.RoleMerger, waiting.round)})
}

// mergedForward ends a merge-forward segment: the merge of the base by the role asked lands on the
// replay's own pull request record as a new head (intake.PullRequestSynchronized, whose handler
// records the merge forward landed). A merge forward asked at once, in the phase of the conflict,
// goes on with the stretch that drew it, not drawn a second time: past its recorded conflict loop
// where the record ended it in a conflict (stretchEnds), and otherwise by its recorded move. The
// merger's merge forward of a conflict that waited for merging (mergerMergesForward), in another
// phase than the conflict's, comes after that stretch already went on: the issue's next work in
// merging, which the merger's start opened this segment in its place, opens now, and is not drawn
// a second time either.
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
	if forward.phase != drew.phase {
		pieces := d.script.pieces[forward.issue]
		if i := forward.index + 1; i < len(pieces) {
			next := pieces[i]
			if next.waitsForStart(forward.role, forward.phase) {
				next.stretchDrawn = true
				d.openPiece(next, d.clock.Instant(), false)
			}
		}
		return nil
	}
	if drew.move().Source.Way == MoveDraw {
		return d.stretchEnds(drew)
	}
	return d.enterRecorded(drew)
}

// drawLoop gives the issue of p, moved back to implementing by a conflict drawn at the end of p's
// stretch, the work of a conflict loop drawn with the run's seed from the window's recorded loops
// (drawnLoop), in place of its own recorded loop where the record ended the stretch in a conflict
// too: the loop's pieces, each the drawn issue's work given to this one, up to the return to the
// phase the conflict left; where the record ended the stretch otherwise, p's own work follows the
// loop again, ending as the record ended it. A loop that left p's phase returns the issue to it. One
// that left a later phase is cut where its work reaches p's phase (returnTo), rather than going on
// with the work of its own return. After one that left an earlier phase the issue goes back from
// that phase to p's through its own most recent work in each phase between (wayBack); where its
// work before p holds none in that phase, it is given no drawn loop and goes on with its own work.
// Each start of the issue's workers so opens the next piece of its work, passing over none. A
// drawn loop that is the issue's own runs as recorded. The recorded events of the issue's own loop
// go with the work that takes its place (giveEvents).
func (d *driver) drawLoop(p *piece) {
	pieces := d.script.pieces[p.issue]
	own, back := []*piece(nil), p.index+1
	recordedConflict := p.move().Source.Way == MoveDraw
	if recordedConflict {
		own, back = loopAfter(pieces, p)
	}
	loop := d.drawnLoop(p.phase)
	if loop == nil {
		return
	}
	drawn, way := loop.pieces, []*piece(nil)
	switch loopSide(loop, p.phase) {
	case leftLater:
		drawn = returnTo(drawn, p.phase)
	case leftEarlier:
		if way = wayBack(pieces, p, loop.left); way == nil {
			return
		}
	}
	if len(drawn) == 0 || len(own) > 0 && own[0] == drawn[0] {
		return
	}
	work := append(clonePieces(drawn, p.issue), clonePieces(way, p.issue)...)
	if !recordedConflict {
		work = append(work, clonePieces([]*piece{p}, p.issue)...)
	}
	for _, dropped := range pieces[p.index+1 : back] {
		dropped.state = pieceSkipped
	}
	d.giveEvents(pieces[p.index+1:back], work)
	d.replacePieces(p.issue, p.index+1, back, work)
}

// giveEvents gives each recorded event of own, an issue's recorded pieces of work that work, the
// replay's own work for that issue, takes the place of, to the piece of work that stands in for
// its piece: the one of the same role and phase in the same place among those of that role and
// phase, or the last of them. The event is an act of that work as it was of its own piece, so it
// keeps its offset, but no later than the end of a shorter piece: an act the record shows after a
// piece's end comes at the replay's end of it (sizePiece), and its moment in the record orders it
// there, before that end. An event of a piece whose role and phase no piece of work holds is placed
// by its anchor (placeUnrun).
func (d *driver) giveEvents(own, work []*piece) {
	type kind struct {
		role  claim.Role
		phase phase.Phase
	}
	given := map[kind]int{}
	for _, q := range own {
		k := kind{q.role, q.phase}
		place := given[k]
		given[k]++
		var to *piece
		for _, c := range work {
			if c.role != q.role || c.phase != q.phase {
				continue
			}
			to = c
			if place == 0 {
				break
			}
			place--
		}
		if to == nil {
			d.placeUnrun(q)
			continue
		}
		for _, a := range q.acts {
			if a.kind != actEvent || d.script.events[a.event].piece != q {
				continue
			}
			offset := a.offset
			if !to.end.IsZero() {
				offset = min(offset, to.recorded())
			}
			to.acts = append(to.acts, act{kind: actEvent, offset: offset, rec: to.start.Add(offset), event: a.event})
			d.script.events[a.event].piece = to
		}
	}
}

// returnTo is pieces up to the first in left or a phase after it in phase.Order: the part of a run
// of work that takes an issue to left and no further.
func returnTo(pieces []*piece, left phase.Phase) []*piece {
	from := slices.Index(phase.Order, left)
	for i, q := range pieces {
		if slices.Index(phase.Order, q.phase) >= from {
			return pieces[:i]
		}
	}
	return pieces
}

// wayBack is the work that takes p's issue from to, a phase before p's that a drawn conflict loop
// returned it to, back to p's phase: its own most recent work before p in each phase between, the
// pieces of its work from the first of its last visit to to before p up to its first piece in p's
// phase or a later one (returnTo). Nil where its work before p holds no piece in to.
func wayBack(pieces []*piece, p *piece, to phase.Phase) []*piece {
	last := -1
	for i := p.index - 1; i >= 0; i-- {
		if pieces[i].phase == to {
			last = i
			break
		}
	}
	if last < 0 {
		return nil
	}
	first := last
	for first > 0 && pieces[last].visit != nil && pieces[first-1].visit == pieces[last].visit {
		first--
	}
	return returnTo(pieces[first:p.index], p.phase)
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

// waitsForStart says whether q is the next work a start of role's worker in ph opens: a piece of
// role's in ph that waits for that start (opensOnStart).
func (q *piece) waitsForStart(role claim.Role, ph phase.Phase) bool {
	return q.state == pieceWaiting && q.role == role && q.phase == ph && q.opens == opensOnStart
}
