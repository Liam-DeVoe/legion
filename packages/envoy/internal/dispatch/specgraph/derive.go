package specgraph

import "slices"

// State is the state of each check and each statement at one subject (R10: "A page says which
// subject it shows"): Checks as CheckStates gives them, and Statements in the order of Derive.
type State struct {
	Subject    Point            `json:"subject"`
	Checks     []CheckState     `json:"checks"`
	Statements []StatementState `json:"statements"`
}

// Derive is the state at in.Subject of each check that CheckStates judges, and of each statement
// that the approved spec holds a current text of, in preorder: the top statements by id, then the
// statements under each one by id, depth first.
//
// The checks of a statement S that count are those that read the subject, the checks that
// CheckStates judges (R10: "Only the checks that read the subject count"). They are taken in the
// search order: S's own checks by id, then each statement under S in preorder with its own checks
// by id. Then come S's supporters by id, the statements whose serves names S (R10: "the checks of
// the statement, the checks under it, and the checks of the statements that support it"), each by
// its own state. The first rule that holds decides, and Detail names the first statement, check or
// supporter in the search order that the rule reads:
//
//  1. The files do not hold the current approved text of S or of a statement under it: not_built,
//     not_built, Detail the first such statement in preorder, S first (R10: "Not built: the files
//     at that commit do not hold the approved text of the statement, or of a statement under it").
//  2. A check that counts is false, or a supporter is not met: not_met, check_false or
//     supporter_not_met (R10: "Not met: one of them is false").
//  3. A check that counts is unknown, S or a statement under it is a leaf that the approved spec
//     gives no check, or a supporter is unknown or not built: unknown, check_unknown, no_check,
//     supporter_unknown or supporter_not_built (R10: "Unknown: every other case"; R6: "Each leaf
//     statement has one or more checks"; R7: "silence is not a pass").
//  4. Else met, checks_true, also when no check counts (R10: "Met: all of them are true"), and
//     PartlyChecked when the approved spec marks S (R10: "The page then says partly checked beside
//     met"). R37 needs that reading of no check: a statement whose only checks read production is
//     met at the head of main before its issue is done and its production checks run. A leaf
//     whose checks all read production is such a statement, and not a leaf with no check.
//
// Own is the state of S over its own checks that read the subject, which a wait on S reads (R29:
// "its own checks read true there. The statements under it do not count for that wait"; R10:
// "Only the checks that read the subject count"): not_built when the files do not hold S's current
// text; unknown when S is a leaf that the approved spec gives no check, so a wait on it holds (R6,
// and R10: "Met means that its checks pass"); else not_met when one of them is false, unknown when
// one is unknown, and met when all are true, also when S has statements under it and no own check,
// or none of its checks reads the subject. So at the head of main a wait on S never waits for a
// check of production.
func Derive(in Input) State {
	checks := newCheckJudge(in)
	s := State{Subject: in.Subject, Checks: checks.states()}
	if s.Checks == nil {
		s.Checks = []CheckState{}
	}
	j := newStatementJudge(checks.current, in.Approved, checks.holds, s.Checks)
	order := j.inPreorder(checks.current)
	s.Statements = make([]StatementState, 0, len(order))
	for _, id := range order {
		s.Statements = append(s.Statements, j.state(id))
	}
	return s
}

// WaitEnds is whether a wait on the statement ends at the subject (R29: "That wait ends when main
// holds the statement's approved text and its own checks read true there"): the subject is the
// head of main and the statement's Own state is met. It is false for an id that is no current
// statement.
func (s State) WaitEnds(statement string) bool {
	if s.Subject.Branch != "main" {
		return false
	}
	i := slices.IndexFunc(s.Statements, func(st StatementState) bool { return st.ID == statement })
	return i >= 0 && s.Statements[i].Own == statementMet
}
