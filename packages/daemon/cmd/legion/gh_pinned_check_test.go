package main

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghroute/ghroutetest"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// ghCheckReadingLikeThePinnedCheck is a stand-in shared check for the call args, which does with
// it what the pinned check does, by ghroutetest.PinnedCheckReads, decided here for the call the
// test runs: for a call that check reads as a read, nothing; for any other, what that check does
// for one whose text matched a rule other than a credential's on a repository it must look up (a
// call that begins with an option it refuses instead, and no test gives this one such a call). It
// reads the target's visibility, through gh, in the argv and under the variable that check's
// vis_of uses, and records `lookup <slug>` in its own record first. The target is
// LEAK_CHECK_STANDIN_TARGET, which stands for the slug the check's own targets would find. Either
// way it then clears the call, as that check clears such a call to a private repository, and it
// records each run, as leakguardtest.Recording does, so a test reads that it was handed args.
// `legion gh` answers its visibility read itself (answerVisibilityRead), by the guard's REST read,
// so it reaches the stand-in gh as ghGuardLookup, as the guard's own read does: the record is
// what tells the two apart.
func ghCheckReadingLikeThePinnedCheck(args []string) string {
	script := "cat >/dev/null 2>&1\n"
	if !ghroutetest.PinnedCheckReads(args) {
		script += `if [ -n "${LEAK_CHECK_STANDIN_TARGET-}" ]; then
	printf 'lookup %s\n' "$LEAK_CHECK_STANDIN_TARGET" >>"${LEAK_CHECK_RECORD:-/dev/null}"
	PUBLIC_LEAK_CHECK_VISIBILITY_READ=$LEAK_CHECK_STANDIN_TARGET GH_HOST=github.com \
		gh api --hostname github.com "repos/$LEAK_CHECK_STANDIN_TARGET" --jq .visibility >/dev/null 2>&1 </dev/null
fi
`
	}
	return leakguardtest.Recording(script + "exit 0\n")
}

// ghGuardLookup is what the stand-in gh records of a REST visibility read of acme/widgets: the
// guard's own, which asks for the response's headers, and the shared check's, which `legion gh`
// answers by that same read (answerVisibilityRead).
const ghGuardLookup = "visibility api --hostname github.com repos/acme/widgets --include --jq .visibility\n"

// A call counts as a read only where the shared check counts it as one, so no read spends a
// visibility read in the check either. That every call ghroute admits as a read is one the pinned
// check reads as a read too, over generated spellings and without a process, is ghroute's
// TestGhReadsOnlyWhatTheSharedCheckReadsAsARead.
//
// And end to end, against ghCheckReadingLikeThePinnedCheck, a call outside the read grammar is
// guarded whole. Where the check reads it as no read — a listed leaf by its alias `pr ls`, a gh
// api endpoint written as a whole URL, `-i=` with a false value, which collect reads as `-f`, `--`
// before a word collect reads as a method, an empty value before a second option collect then
// takes as that value, so that the word after it is the method or the endpoint, a method other
// than GET, a field and an input — the check looks up its target's visibility, and the guard reads
// it too where the call names acme/widgets, so it costs what it cost on main and no less. Where
// the check reads it as a read — `--` before an endpoint, an empty value before a method, which
// collect takes as that value while gh sends the method, a value after `=` or joined to its short
// option (`--method=GET`, `-XGET`), an empty header and one beginning with `-`, and a listed leaf
// given a value after `=` — it costs the guard's read alone. The shared check's lookup reaches gh
// as the guard's REST read, which `legion gh` answers it with, and the check records it.
func TestLegionGhReadsOnlyWhatTheSharedCheckReadsAsARead(t *testing.T) {
	run := ghGuardRig(t)
	for _, tc := range []struct {
		args                   []string
		checkLooks, guardLooks bool
	}{
		{[]string{"pr", "ls", "-R", "acme/widgets"}, true, true},
		{[]string{"api", "https://api.github.com/repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "-i=false", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "repos/acme/widgets/pulls", "-i=f"}, true, true},
		{[]string{"api", "-i=F", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "-i=FALSE", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "-i=False", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "--", "-XPOST"}, true, false},
		{[]string{"api", "--", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "-q", "", "-q", "-XPOST", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "--jq", "", "--jq", "graphql", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "repos/acme/widgets/pulls", "--jq", "", "-XPOST"}, false, true},
		{[]string{"api", "--jq=.x", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "-q.x", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "-H", "", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "-H", "-x", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "--method=GET", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "-XGET", "repos/acme/widgets/pulls"}, false, true},
		{[]string{"api", "-X", "POST", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "-f", "x=y", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"api", "--input", "-", "repos/acme/widgets/pulls"}, true, true},
		{[]string{"pr", "view", "1", "--repo=acme/widgets"}, false, true},
	} {
		t.Run("guarded whole: "+strings.Join(tc.args, " "), func(t *testing.T) {
			got := run(t, ghCheckAt(t, ghCheckReadingLikeThePinnedCheck(tc.args)), "", tc.args...)
			if got.code != 7 {
				t.Errorf("legion gh %q = exit %d, stderr %q; want gh's own 7", tc.args, got.code, got.stderr)
			}
			if want := "checked gh-guard " + strings.Join(tc.args, " ") + "\n"; !strings.HasPrefix(got.checked, want) {
				t.Errorf("the shared check recorded %q, want its run of the call first, %q", got.checked, want)
			}
			if looked := strings.Contains(got.checked, "lookup acme/widgets\n"); looked != tc.checkLooks {
				t.Errorf("the shared check read the visibility: %v, want %v: %q", looked, tc.checkLooks, got.checked)
			}
			wantReads := 0
			for _, looks := range []bool{tc.checkLooks, tc.guardLooks} {
				if looks {
					wantReads++
				}
			}
			if reads := strings.Count(got.gh, ghGuardLookup); reads != wantReads {
				t.Errorf("gh was asked the visibility %d times, want %d (the check's read %v, the guard's %v): %q", reads, wantReads, tc.checkLooks, tc.guardLooks, got.gh)
			}
			if strings.Contains(got.log, `visibility="checked-only"`) {
				t.Errorf("the call log reads a call the guard read whole as checked-only: %q", got.log)
			}
		})
	}
}
