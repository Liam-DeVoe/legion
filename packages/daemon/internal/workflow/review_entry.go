package workflow

import (
	"strconv"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ReviewEntryLabel labels the entry of the round a reviewer's task in reviewing names
// (reviewEntry.part), the number the reviewer's phases row holds (record.PhaseRow.Entry). The
// reviewer writes that number into the Legion footer of each review it submits for the task
// (intake.PullRequestReview.LegionEntry), and only a review that names the entry that stands moves
// the commit the reviewer read (review). The reviewer's prompt and the merge gate of
// skill://legion-worker name it to the reviewer, which a test ties to it.
const ReviewEntryLabel = "Review entry"

// reviewEntry is the entry of the round a reviewer's task in reviewing names (ReviewEntryLabel),
// 0 where the task names none. Like reviewedCommit it has a type of its own, so no other number
// reaches a task through part.
type reviewEntry int64

// part is the sentence a task names n with, " Review entry: <n>." (ReviewEntryLabel), and "" for
// no entry: the one place the sentence is written. namingTheReviewEntry is its one caller, so the
// sentence is the task's LAST one, which is the shape the reviewer reads it in.
func (n reviewEntry) part() string {
	if n <= 0 {
		return ""
	}
	return " " + ReviewEntryLabel + ": " + strconv.FormatInt(int64(n), 10) + "."
}

// reviewEntryOf is the entry a task that hands role work names (reviewEntry.part): row's entry for
// the reviewer of an issue in reviewing while row holds the task of that entry
// (record.PhaseRow.EntryTask), and 0 in every other case — any other role or phase, and a row a
// clear emptied (record.Store.ClearGeneration), whose entry no review moves the commit for until
// the next entry is raised (review). A reviewer resumed in a fresh session reads its entry from
// that task alone.
func reviewEntryOf(role claim.Role, at phase.Phase, row record.PhaseRow) reviewEntry {
	if !startsReviewRound(role, at) || row.EntryTask == "" {
		return 0
	}
	return reviewEntry(row.Entry)
}

// namingTheReviewEntry is task as role is given it, standing in phase at with row its phases row:
// task ending with the sentence that names the entry of the reviewer's round (reviewEntryOf), and
// task as it is where that names none. A task that already ends with that sentence is returned as
// it is, so the sentence is in a task once: the stored task of an entry (enter) holds it, and the
// queue (queuedRow) reads every task, that one too, when it is sent again (sendOwedEntry).
//
// Every task the reviewer is given in reviewing ends with it, the wake's task too (WakeTask): the
// reviewer writes the number of the newest sentence a task of its session named into the footer of
// its review, and only a review that names the entry that stands moves the commit it read.
func namingTheReviewEntry(task string, role claim.Role, at phase.Phase, row record.PhaseRow) string {
	sentence := reviewEntryOf(role, at, row).part()
	if strings.HasSuffix(task, sentence) {
		return task
	}
	return task + sentence
}
