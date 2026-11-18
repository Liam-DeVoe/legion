package workflow

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// checkTaskWakes are the four wakes a look can have, each written as its own caller writes it:
// the merge that opened the check (openCheck, the merge commit as the detail), the wake the
// checking agent named on a pending report (checkReported, the report's summary), the retry of a
// look nobody reported (endLook, retryOfLook and the cause it was given), and the check's bound
// (checkBound). The last three are the ones a reader of the task must tell apart, so each case
// names the fields the task has to carry for it.
var checkTaskWakes = []struct {
	name    string
	cause   string
	detail  string
	wokenBy string
	retryOf string
	summary string
}{
	{"the merge", wakeMerge, "merge-43", texts.WokenByMerge, "", ""},
	{"the agent's own wake", wakeNamed, "production runs yesterday's build; the release train runs at 09:00", texts.WokenByNamed, "",
		"production runs yesterday's build; the release train runs at 09:00"},
	{"the retry of a look nobody reported", wakeNamed, retryOfLook + "look bound", texts.WokenByRetry, "look bound", ""},
	{"the bound", wakeBound, boundText(14*24*time.Hour) + " since the issue closed", texts.WokenByBound, "", ""},
}

// checkTaskFor is the task of a look woken by cause with detail, last marking the bound's look.
// One issue and one check for every case, so two tasks differ only in their wake, the check's
// bound 14 days from its opening.
func checkTaskFor(cause, detail string, last bool) (texts.CheckLookRecord, string) {
	issue := record.Issue{Key: "LEGION-208", Title: "A production check"}
	check := record.ProductionCheck{Issue: issue.Key, Repo: "sjawhar/legion", Number: 43, MergeSHA: "merge-43",
		OpenedAt: checkOpened, BoundAt: checkOpened.Add(14 * 24 * time.Hour), Looks: 2, Role: claim.RoleTester}
	return CheckLook(issue, check, cause, detail, last), checkTask(issue, check, cause, detail, last)
}

// The task tells the checking agent what woke this look, and nothing else does: the session may be
// a fresh one on a woken pod, with no memory of the report it sent. A look the agent's own wake
// started carries the summary that report gave in a block by the checking role, and one the retry
// started names the retry and its cause, so a reader can tell the look it asked for from the look
// it was given after saying nothing. The first line is the one a resumed session tells the check
// from its last phase task by, and the legion tool's description quotes it.
func TestTheCheckTaskNamesWhatWokeItsOwnLook(t *testing.T) {
	for _, tc := range checkTaskWakes {
		t.Run(tc.name, func(t *testing.T) {
			look, task := checkTaskFor(tc.cause, tc.detail, false)
			read := readTaskOf(t, task)
			if want := "Production check of LEGION-208: A production check."; read.lines[0] != want {
				t.Errorf("the task of a look woken by %s opens %q, want it to open %q", tc.name, read.lines[0], want)
			}
			if look.WokenBy != tc.wokenBy || look.RetryOf != tc.retryOf || read.fields["woken_by"] != tc.wokenBy {
				t.Errorf("the look woken by %s names woken_by %q and retry_of %q, want %q and %q", tc.name, look.WokenBy, look.RetryOf, tc.wokenBy, tc.retryOf)
			}
			if tc.summary == "" {
				if len(read.blocks) != 0 {
					t.Errorf("the look woken by %s carries the blocks %+v, want none", tc.name, read.blocks)
				}
				return
			}
			if block := read.block(t, "summary"); block.text != tc.summary || block.attrs["by"] != string(claim.RoleTester) {
				t.Errorf("the summary block is %+v, want the report's summary by the tester", block)
			}
		})
	}
}

// The look names its facts: the look, whether it is the last, the pull request, the merge commit
// (none where the record holds none, which the reader then reads from GitHub), and the check's
// bound in whole days with the time of the look at that bound.
func TestTheCheckTaskNamesTheFactsOfItsLook(t *testing.T) {
	look, _ := checkTaskFor(wakeBound, boundText(14*24*time.Hour)+" since the issue closed", true)
	want := texts.CheckLookRecord{Issue: "LEGION-208", Title: "A production check", Kind: texts.CheckLook, Look: 2, WokenBy: texts.WokenByBound,
		Last: true, PullRequest: "sjawhar/legion#43", MergeCommit: "merge-43", BoundDays: 14, LastLook: checkOpened.Add(14 * 24 * time.Hour).UTC()}
	if !reflect.DeepEqual(look, want) {
		t.Fatalf("the bound's look = %+v, want %+v", look, want)
	}
	issue := record.Issue{Key: "LEGION-208", Title: "A production check"}
	unknown := checkTask(issue, record.ProductionCheck{Issue: issue.Key, Repo: "sjawhar/legion", Number: 43, Looks: 1}, wakeMerge, "", false)
	if read := readTaskOf(t, unknown); read.fields["merge_commit"] != "" || strings.Contains(unknown, "merge_commit") {
		t.Fatalf("the look of a check whose record holds no merge commit = %q, want no merge commit named", unknown)
	}
}
