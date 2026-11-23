// Package reviewround writes the daemon's sentences of a reviewer's review round: the two that end
// each task a reviewer is given, the commit the reviewer's newest recorded deciding review read and
// the entry of its round (Sentences), and the one before them where the reviewer's row holds no
// commit and another agent's review on the task read one (AnotherAgentsSentence). Two places write
// the two: the workflow, onto each task of the reviewer it queues (workflow's
// namingTheReviewRound), and the claim's send of a task that no such queue named, a task the
// release before this one queued or an operator wrote (supervise's Delivery.message), which names
// neither. Only the workflow writes the one before them, on a reviewer's task and on the task of a
// deployment role's review step.
package reviewround

import "strconv"

// CommitLabel labels the commit a reviewer's task names (record.PhaseRow.ReviewedHead). The
// reviewer's prompt and the merge gate of skill://legion-worker name it to the reviewer, which a
// test ties to it, and the tests of the packages that read a reviewer's task name it by this
// constant rather than type it again.
const CommitLabel = "Last reviewed commit"

// EntryLabel labels the entry of the round a reviewer's task names (record.PhaseRow.Entry). The
// reviewer writes that number into the Legion footer of each review it submits for the task
// (intake.PullRequestReview.LegionEntry), and only a review that names the entry that stands moves
// the commit the reviewer read (workflow's reviewedCommitStays).
const EntryLabel = "Review entry"

// none is what each sentence names where there is no value for it: no commit, or no entry. A
// commit is 40 hexadecimal characters and an entry a number, so neither is ever this word.
const none = "none"

// AnotherAgentLabel labels the commit another agent's review on the same task read, which a review
// step's task names where the step's own row holds no commit (record.AnotherAgentsReview): its
// agent has no deciding review of its own on the task, so its own sentence names none and it reads
// the whole change, and this sentence lets it see what changed since the other agent's review.
const AnotherAgentLabel = "Commit another agent's review read"

// AnotherAgentsSentence is " Commit another agent's review read: <commit>.", and "" where commit
// is "". On a reviewer's task it stands before the two sentences of the round (Sentences), which
// stay the task's last two, so the way back of the reviewed-head migration, which strips those two
// at a task's end (store/migrations/20261116234034_phase_reviewed_head.up.sql, its header's three
// regexp_replace updates), strips them alone.
func AnotherAgentsSentence(commit string) string {
	if commit == "" {
		return ""
	}
	return " " + AnotherAgentLabel + ": " + commit + "."
}

// Sentences is the two sentences that end a reviewer's task, in this order:
// " Last reviewed commit: <commit>." and " Review entry: <entry>.", each naming `none` where
// commit is "" or entry is not above 0. It is the one place either sentence is written.
func Sentences(commit string, entry int64) string {
	if commit == "" {
		commit = none
	}
	named := none
	if entry > 0 {
		named = strconv.FormatInt(entry, 10)
	}
	return " " + CommitLabel + ": " + commit + ". " + EntryLabel + ": " + named + "."
}
