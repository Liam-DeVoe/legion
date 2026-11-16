package specgraph

import "slices"

// StatementState is the state of one statement at the subject (R10): State "not_built", "met",
// "not_met" or "unknown", with the Reason code of the rule that decided it and Detail, the id that
// the reason names: the statement that is not built, the check that is false or unknown, the
// supporter, or the statement with no check where the tree rules ask for one (lacksCheck), and ""
// for "checks_true". HeldVersion is the approved version of the statement that the files hold, 0
// when they hold none, and EarlierText marks a version that is not the current one (R10: "Where
// the files hold the text of an earlier approval, the page says so"). PartlyChecked marks a met
// statement whose checks decide only part of it, as the reviewer of its level judged (R10). Own is
// the state over the statement's own checks that read the subject, which a wait on the statement
// reads (R29).
type StatementState struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Reason        string `json:"reason"`
	Detail        string `json:"detail"`
	Own           string `json:"own"`
	HeldVersion   int    `json:"held_version"`
	EarlierText   bool   `json:"earlier_text"`
	PartlyChecked bool   `json:"partly_checked"`
}

// The states of a statement (R10).
const (
	statementNotBuilt = "not_built"
	statementMet      = "met"
	statementNotMet   = "not_met"
	statementUnknown  = "unknown"
)

// The reason codes of a statement state, beside reasonNotBuilt and reasonRefused, each the rule of
// Derive that gives it.
const (
	reasonCheckFalse        = "check_false"
	reasonSupporterNotMet   = "supporter_not_met"
	reasonCheckUnknown      = "check_unknown"
	reasonSupporterUnknown  = "supporter_unknown"
	reasonSupporterNotBuilt = "supporter_not_built"
	reasonNoCheck           = "no_check"
	reasonStatementRefused  = "statement_refused"
	reasonChecksTrue        = "checks_true"
)

// statementJudge derives the state of each statement of the approved spec at one subject from the
// states of the checks that read the subject.
type statementJudge struct {
	approved   Approved
	holds      map[string]Holding        // which approved text of each node the files hold
	refused    map[string]string         // the rule that refuses each check and statement of the files
	checks     map[string]CheckState     // the state of each check that reads the subject, by id
	statements map[string]bool           // the id of each current statement
	children   map[string][]string       // the statements under each statement, sorted by id
	own        map[string][]string       // the checks of each statement, sorted by id
	supporters map[string][]string       // the statements that support each statement, sorted by id
	states     map[string]StatementState // each state derived so far, by id
}

// newStatementJudge is the judge of the statements of current, the current text of each node of
// approved, at a subject where the files hold holds, the tree rules refuse refused (refusals) and
// the checks read checks.
func newStatementJudge(current Tree, approved Approved, holds map[string]Holding, refused map[string]string, checks []CheckState) *statementJudge {
	j := &statementJudge{approved: approved, holds: holds, refused: refused, checks: make(map[string]CheckState, len(checks)),
		statements: make(map[string]bool, len(current.Statements)), children: map[string][]string{},
		own: map[string][]string{}, supporters: map[string][]string{}, states: map[string]StatementState{}}
	for _, c := range checks {
		j.checks[c.ID] = c
	}
	// The tree is sorted by id, so each list is too.
	for _, s := range current.Statements {
		j.statements[s.ID] = true
		if s.Parent != "" {
			j.children[s.Parent] = append(j.children[s.Parent], s.ID)
		}
		for _, supported := range s.Serves {
			j.supporters[supported] = append(j.supporters[supported], s.ID)
		}
	}
	for _, c := range current.Checks {
		j.own[c.Serves] = append(j.own[c.Serves], c.ID)
	}
	return j
}

// inPreorder is the id of each current statement in preorder: the top statements by id, then the
// statements under each one by id, depth first. A statement whose parent is no current statement is
// a top statement, and a statement in a loop of parents, which Validate refuses, comes after them,
// each statement once.
func (j *statementJudge) inPreorder(current Tree) []string {
	seen := make(map[string]bool, len(current.Statements))
	order := make([]string, 0, len(current.Statements))
	for _, s := range current.Statements {
		if !j.statements[s.Parent] {
			order = j.preorder(s.ID, seen, order)
		}
	}
	for _, s := range current.Statements {
		order = j.preorder(s.ID, seen, order)
	}
	return order
}

// preorder appends to order id and each statement under it that seen does not hold, in preorder,
// and adds each to seen.
func (j *statementJudge) preorder(id string, seen map[string]bool, order []string) []string {
	if seen[id] {
		return order
	}
	seen[id] = true
	order = append(order, id)
	for _, child := range j.children[id] {
		order = j.preorder(child, seen, order)
	}
	return order
}

// state is the state of the statement id, in the order of Derive.
func (j *statementJudge) state(id string) StatementState {
	if st, derived := j.states[id]; derived {
		return st
	}
	st := StatementState{ID: id, Own: j.ownState(id)}
	if h := j.holds[id]; h.Present {
		st.HeldVersion, st.EarlierText = h.Version, !h.Current
	}
	under := j.preorder(id, map[string]bool{}, nil)
	if i := slices.IndexFunc(under, j.notBuilt); i >= 0 {
		st.State, st.Reason, st.Detail = statementNotBuilt, reasonNotBuilt, under[i]
	} else {
		st.State, st.Reason, st.Detail = j.byChecks(id, under)
	}
	st.PartlyChecked = st.State == statementMet && j.approved.Nodes[id].PartlyChecked
	j.states[id] = st
	return st
}

// byChecks is the state, reason and detail of the statement id, whose files hold its current text
// and the current text of each statement under it, in under: its own checks and the checks of each
// statement under it that read the subject, in the search order, then its supporters by id. A
// statement of under that lacks a check that the tree rules ask for (lacksCheck) is unknown,
// no_check, in its place of the search order (R7: "silence is not a pass"); one that the tree
// rules refuse otherwise (refusals) is unknown there too: refused with the rule for id itself, and
// statement_refused with that statement for a statement above it. A supporter that id supports in
// its turn, directly or through other supporters, is in a loop of supporters, which Validate
// refuses: it reads unknown, so that no state depends on the order of the derivation.
func (j *statementJudge) byChecks(id string, under []string) (state, reason, detail string) {
	var notMet, unknown finding
	for _, s := range under {
		rule, refused := j.refused[s]
		switch {
		case j.lacksCheck(s):
			unknown.keep(reasonNoCheck, s)
		case refused && s == id:
			unknown.keep(reasonRefused, rule)
		case refused:
			unknown.keep(reasonStatementRefused, s)
		}
		for _, c := range j.own[s] {
			switch cs, reads := j.checks[c]; {
			case !reads:
			case cs.State == checkFalse:
				notMet.keep(reasonCheckFalse, c)
			case cs.State == checkUnknown:
				unknown.keep(reasonCheckUnknown, c)
			}
		}
	}
	for _, p := range j.supporters[id] {
		supporter := statementUnknown
		if !j.supportedBy(p, id) {
			supporter = j.state(p).State
		}
		switch supporter {
		case statementNotMet:
			notMet.keep(reasonSupporterNotMet, p)
		case statementUnknown:
			unknown.keep(reasonSupporterUnknown, p)
		case statementNotBuilt:
			unknown.keep(reasonSupporterNotBuilt, p)
		}
	}
	switch {
	case notMet.reason != "":
		return statementNotMet, notMet.reason, notMet.detail
	case unknown.reason != "":
		return statementUnknown, unknown.reason, unknown.detail
	}
	return statementMet, reasonChecksTrue, ""
}

// finding is the first reason of one state that the search order meets, and the id it names.
type finding struct {
	reason, detail string
}

// keep sets the finding unless it has one.
func (f *finding) keep(reason, detail string) {
	if f.reason == "" {
		f.reason, f.detail = reason, detail
	}
}

// supportedBy is whether target supports statement, directly or through other supporters.
func (j *statementJudge) supportedBy(statement, target string) bool {
	seen := map[string]bool{statement: true}
	for next := []string{statement}; len(next) > 0; {
		s := next[len(next)-1]
		next = next[:len(next)-1]
		for _, p := range j.supporters[s] {
			if p == target {
				return true
			}
			if !seen[p] {
				seen[p] = true
				next = append(next, p)
			}
		}
	}
	return false
}

// lacksCheck is whether the approved spec gives the statement id no check where the tree rules ask
// for one, by the pair of rules of Validate: a leaf (leaf-without-check, R6: "Each leaf statement
// has one or more checks"), or a statement that other statements serve (supported-without-check,
// R5: "A statement that other statements support still has checks of its own"). A statement with
// statements under it, no supporter and no check of its own is neither: the rules take it. A
// statement that serves itself is in a loop, which Validate refuses too, and lacks a check here.
func (j *statementJudge) lacksCheck(id string) bool {
	return len(j.own[id]) == 0 && (len(j.children[id]) == 0 || len(j.supporters[id]) > 0)
}

// ownState is the state of the statement id over those of its own checks that read the subject
// (R29, with R10: "Only the checks that read the subject count"): not built when the files do not
// hold its current text; unknown when it lacks a check that the tree rules ask for (lacksCheck;
// R10: "Met means that its checks pass") or the tree rules refuse it (refusals); else not met when
// one of them is false, unknown when one is unknown, and met when all are true, also when a
// statement with statements under it and no supporter has no own check, or none of its checks
// reads the subject.
func (j *statementJudge) ownState(id string) string {
	if j.notBuilt(id) {
		return statementNotBuilt
	}
	if _, refused := j.refused[id]; refused || j.lacksCheck(id) {
		return statementUnknown
	}
	state := statementMet
	for _, c := range j.own[id] {
		switch j.checks[c].State { // a check that does not read the subject has no state here
		case checkFalse:
			return statementNotMet
		case checkUnknown:
			state = statementUnknown
		}
	}
	return state
}

// notBuilt is whether the files do not hold the current approved text of the statement id.
func (j *statementJudge) notBuilt(id string) bool {
	h := j.holds[id]
	return !h.Present || !h.Current
}
