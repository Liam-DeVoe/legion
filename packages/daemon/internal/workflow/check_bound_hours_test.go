package workflow

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A look's task names the check's bound as the check has it. Main said it as "<boundText> since
// the issue closed", in whole days, in hours, or as Go prints a shorter one (boundText), and
// `production_check_hours` takes any positive number of hours, a decimal among them. The task gave
// it in whole days alone, so a bound of 36 hours read as one day and a bound of 12 hours named no
// bound at all. A bound of whole days is named in whole days (bound_days); any other is named in
// hours (bound_hours), a decimal where it is one, with no bound in days beside it.
func TestALookTaskNamesABoundThatIsNotWholeDaysInHours(t *testing.T) {
	issue := record.Issue{Key: "LEGION-208", Title: "A production check"}
	for _, tc := range []struct {
		bound       time.Duration
		days, hours string
	}{
		{bound: 14 * 24 * time.Hour, days: "14"},
		{bound: 36 * time.Hour, hours: "36"},
		{bound: 12 * time.Hour, hours: "12"},
		{bound: 90 * time.Minute, hours: "1.5"},
		{bound: 18 * time.Minute, hours: "0.3"},
	} {
		t.Run(tc.bound.String(), func(t *testing.T) {
			check := record.ProductionCheck{Issue: issue.Key, Repo: "sjawhar/legion", Number: 43, MergeSHA: "merge-43",
				OpenedAt: checkOpened, BoundAt: checkOpened.Add(tc.bound), Looks: 1, Role: claim.RoleTester}
			read := readTaskOf(t, checkTask(issue, check, wakeMerge, "", false))
			if read.fields["bound_days"] != tc.days || read.fields["bound_hours"] != tc.hours {
				t.Errorf("a bound of %s is named as bound_days %q and bound_hours %q, want %q and %q", tc.bound,
					read.fields["bound_days"], read.fields["bound_hours"], tc.days, tc.hours)
			}
			if want := checkOpened.Add(tc.bound).UTC().Format(time.RFC3339); read.fields["last_look"] != want {
				t.Errorf("the last look is named %q, want %q", read.fields["last_look"], want)
			}
		})
	}
}
