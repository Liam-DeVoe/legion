package api

import (
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// results_pass_stands.go answers the gate's question for each check of a results read: whether a
// pass of it stands (passStandsOf), the state of each head a pass must stand at (eachSubjectStands,
// subjectState), and the commits at which CI's results of a check disagree (ciConflicts). It is
// a calculation over what comparisonReader.answer has already placed, and reads no database and
// no GitHub.

// standingCommit is where a result's commit stands while the check's words have not moved
// and no retraction stands against it. It answers no standing without both comparison fields, and
// none for a result that counts for nothing (model.CheckResult.NotCounted).
func standingCommit(result model.CheckResult) string {
	if result.Retracted != nil || result.NotCounted != "" || result.CheckWordsMoved == nil || *result.CheckWordsMoved || result.CommitStands == nil {
		return ""
	}
	return result.CommitStands.Answer
}

// passStandsOf is the one line that answers the gate's question for a check: whether a pass of
// it stands at what it compared with, against the check's words now, and from which roles.
//
// A pass stands when its commit is at one of the bases, the check's words have not moved since
// it was recorded, and no retraction stands against it. The roles are those of the passes that
// stand, in the order the results come, which is newest first. A `before_merge` check compared
// with heads asks that of each head, its subjects (headBases), and a pass stands only where one
// stands at each (eachSubjectStands): one head's fail is never hidden by a pass at another.
//
// R7: a commit cannot change, so it holds one answer. Where a check CI proves holds a pass and a
// fail of CI at one commit, in either order, no pass stands there, and the state is unknown for
// model.PassStandsReasonPassAndFail (ciConflicts): a result its CI job recorded and the result
// derived from its check run are both CI's evidence of the check at that commit.
//
// A check whose repository Dispatch could not read answers unknown, since it could compare
// nothing at all. So does a check CI proves whose check runs GitHub could not be read for: it
// holds no result, and the read that would have made one failed. So does a check whose standing
// pass is held up only by a commit Dispatch could not place. A check that compared with nothing
// because there is no pull request and no branch answers no: there is nothing for a pass to
// stand at, and that is a fact about the work rather than a failed read.
func passStandsOf(check model.CheckBlock, compared comparison) model.CheckPassStands {
	if compared.reason == model.ReasonRepositoryUnreadable ||
		(check.CI != nil && check.CI.State == model.CIStateUnread) {
		return model.CheckPassStands{Answer: model.PassStandsUnknown}
	}
	conflicted := ciConflicts(check)
	if check.When == docs.CheckWhenBeforeMerge && len(compared.subjects) > 0 {
		return eachSubjectStands(check, compared, conflicted)
	}
	var roles []string
	unknown, conflict := false, false
	for _, result := range check.Results {
		if result.Verdict != "pass" {
			continue
		}
		switch standingCommit(result) {
		case model.CommitAtBasis:
			if conflicted[result.CommitSHA] {
				conflict = true
				continue
			}
			if !slices.Contains(roles, result.Role) {
				roles = append(roles, result.Role)
			}
		case model.CommitUnknown:
			unknown = true
		}
	}
	switch {
	case len(roles) > 0:
		return model.CheckPassStands{Answer: model.PassStandsYes, Roles: roles}
	case conflict:
		return model.CheckPassStands{Answer: model.PassStandsUnknown, Reason: model.PassStandsReasonPassAndFail}
	case unknown:
		return model.CheckPassStands{Answer: model.PassStandsUnknown}
	}
	return model.CheckPassStands{Answer: model.PassStandsNo}
}

// eachSubjectStands is passStandsOf for a `before_merge` check compared with heads: yes only
// where a pass stands at each subject; no where one subject holds only a fail or nothing; unknown
// where none is no and one is unknown (R7), or where a branch head could not be read, or where a
// pass is held up only by a commit Dispatch could not place. Every answer names each subject's
// state. A yes names a role only where each subject names it (R6), in the order of the first
// subject: two roles that passed at two heads are no role that passed at each.
func eachSubjectStands(check model.CheckBlock, compared comparison, conflicted map[string]bool) model.CheckPassStands {
	stands := model.CheckPassStands{Answer: model.PassStandsYes, Subjects: make([]model.PassStandsSubject, 0, len(compared.subjects))}
	for _, basis := range compared.subjects {
		subject := subjectState(check, basis, conflicted)
		stands.Subjects = append(stands.Subjects, subject)
		switch {
		case subject.Answer == model.PassStandsNo:
			stands.Answer = model.PassStandsNo
		case subject.Answer == model.PassStandsUnknown && stands.Answer == model.PassStandsYes:
			stands.Answer, stands.Reason = model.PassStandsUnknown, subject.Reason
		}
	}
	placedNowhere := slices.ContainsFunc(check.Results, func(result model.CheckResult) bool {
		return result.Verdict == "pass" && standingCommit(result) == model.CommitUnknown
	})
	// A head Dispatch could not read is a subject whose state is unknown, so every subject it
	// read passing is not yet a yes; and a pass held up only by a commit Dispatch could not place
	// may stand at that head, so a no for want of a pass is not yet a no.
	switch {
	case stands.Answer == model.PassStandsYes && compared.unread != "",
		stands.Answer == model.PassStandsNo && placedNowhere:
		stands.Answer, stands.Reason = model.PassStandsUnknown, ""
	}
	if stands.Answer != model.PassStandsYes {
		return stands
	}
	for _, role := range stands.Subjects[0].Roles {
		if !slices.ContainsFunc(stands.Subjects, func(subject model.PassStandsSubject) bool {
			return !slices.Contains(subject.Roles, role)
		}) {
			stands.Roles = append(stands.Roles, role)
		}
	}
	return stands
}

// subjectState is the state of one subject of a check: yes where a pass stands at its commit,
// no where only a fail stands there (model.PassSubjectReasonFail) or nothing does
// (model.PassSubjectReasonNoResult), and unknown where a check CI proves holds a pass and a fail
// of CI at that commit (R7, ciConflicts). It walks every result that counts at the commit, newest
// first, so a yes names each role whose pass counts there and the newest pass of each, and a no
// for a fail names the newest fail (model.PassStandsSubject).
func subjectState(check model.CheckBlock, basis model.CheckBasis, conflicted map[string]bool) model.PassStandsSubject {
	state := model.PassStandsSubject{Basis: basis, Answer: model.PassStandsNo, Reason: model.PassSubjectReasonNoResult}
	if conflicted[basis.Commit] {
		state.Answer, state.Reason = model.PassStandsUnknown, model.PassStandsReasonPassAndFail
		return state
	}
	newestFail := ""
	for _, result := range check.Results {
		if result.CommitSHA != basis.Commit || standingCommit(result) != model.CommitAtBasis {
			continue
		}
		switch {
		case result.Verdict != "pass":
			if newestFail == "" {
				newestFail = result.ID
			}
		case !slices.Contains(state.Roles, result.Role):
			state.Roles = append(state.Roles, result.Role)
			state.Results = append(state.Results, result.ID)
		}
	}
	switch {
	case len(state.Roles) > 0:
		state.Answer, state.Reason = model.PassStandsYes, ""
	case newestFail != "":
		state.Reason, state.Results = model.PassSubjectReasonFail, []string{newestFail}
	}
	return state
}

// ciConflicts is each commit at which check, a check CI proves (validCICheck), holds both a pass
// and a fail that count, retracted by nobody and against the check's words now (R7): the results
// its CI job recorded and the result Dispatch derives from its check run alike, since each is
// CI's evidence of the one check at that commit, and a pass of one never hides a fail of the
// other. A role's result of such a check counts for nothing (not_counted), so it joins nothing.
// An ordinary check holds none here.
func ciConflicts(check model.CheckBlock) map[string]bool {
	conflicted := map[string]bool{}
	if !validCICheck(check) {
		return conflicted
	}
	verdicts := map[string][2]bool{}
	for _, result := range check.Results {
		if standingCommit(result) == "" {
			continue
		}
		seen := verdicts[result.CommitSHA]
		if result.Verdict == "pass" {
			seen[0] = true
		} else {
			seen[1] = true
		}
		verdicts[result.CommitSHA] = seen
	}
	for commit, seen := range verdicts {
		if seen[0] && seen[1] {
			conflicted[commit] = true
		}
	}
	return conflicted
}
