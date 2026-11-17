package workflow

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ReviewedCommitLabel labels the commit a later review round's task names (reviewedCommit.part),
// the commit the daemon last recorded for the reviewer (record.PhaseRow.ReviewedHead). The
// reviewer's prompt and the merge gate of skill://legion-worker name it to the reviewer, which a
// test ties to it, and the tests of the packages that read a reviewer's task name it by this
// constant rather than type it again.
const ReviewedCommitLabel = "Last reviewed commit"

// reviewedCommit is the commit the daemon last recorded for the reviewer, as a start's task names
// it (reviewedPart), and "" where the task names none. It holds the commit and not the sentence
// that names it, and part writes that sentence, so no value of the type reaches a task as anything
// but that sentence or nothing. It has a type of its own so task takes it beside the reason, and a
// swap of the two does not compile, nor does a string variable passed for it. No package outside
// this one can name the type, so a caller there can pass ResumePhaseTask only an untyped
// constant, such as the "" of a start that names no commit.
type reviewedCommit string

// part is the part of a start's task that names c, " Last reviewed commit: <sha>."
// (ReviewedCommitLabel), and "" for no commit: the one place the sentence is written.
func (c reviewedCommit) part() string {
	if c == "" {
		return ""
	}
	return " " + ReviewedCommitLabel + ": " + string(c) + "."
}

// reviewedPart is the commit a start's task names as the one the daemon last recorded for the
// reviewer's own deciding review of an entry that stood (record.PhaseRow.ReviewedHead, which
// review writes), so the reviewer can read the difference from that commit to the head. It is
// read from reviewer, the reviewer's phases row:
// the row's commit for the reviewer started in reviewing, and "" in every other case — any other
// role or phase, and the first review of a generation, whose row holds none. It is the one source
// of the part: every start that hands the reviewer a review round builds its task through task or
// ResumePhaseTask, and each takes the commit from here.
func reviewedPart(role claim.Role, at phase.Phase, reviewer record.PhaseRow) reviewedCommit {
	if !startsReviewRound(role, at) {
		return ""
	}
	return reviewedCommit(reviewer.ReviewedHead)
}

// startsReviewRound says whether a start of role in phase at hands the reviewer a review round,
// the one start whose task can name a reviewed commit (reviewedPart).
func startsReviewRound(role claim.Role, at phase.Phase) bool {
	return role == claim.RoleReviewer && at == phase.Reviewing
}

// reviewedPartFor is reviewedPart for a start of role on issue, in the phase the issue stands in,
// whose caller holds no phases row of that role: the transition's start and the promotion's. It
// reads the reviewer's row only for a review round, so no other start reads the record for it.
func (e *Engine) reviewedPartFor(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role) (reviewedCommit, error) {
	if !startsReviewRound(role, issue.Phase) {
		return "", nil
	}
	reviewer, err := e.phaseRow(ctx, tx, issue.Key, role)
	if err != nil {
		return "", err
	}
	return reviewedPart(role, issue.Phase, reviewer), nil
}
