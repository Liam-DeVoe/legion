package procaudit

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Every test of this module that can list the machine's /proc gives it a /proc of its own or says
// why it needs the machine's, in the build every test step runs. The e2e tag's tests are left out:
// no job runs them, and the job that runs this one is held to the files its runs read. A run that
// read no test unit, or no listing, is a failure and not a pass: this module's tests do list
// /proc, so an audit that found none read the wrong thing.
func TestEveryTestThatListsTheMachinesProcGivesItARootOrSaysWhy(t *testing.T) {
	r := runAudit("../..", "", nil)
	if r.unknown != "" {
		t.Fatalf("UNKNOWN: %s\n%s", r.unknown, r.text.String())
	}
	if len(r.units) == 0 {
		t.Fatalf("the audit listed no test unit, and this module's tests list /proc:\n%s", r.text.String())
	}
	if bad := r.notGiven(); len(bad) != 0 {
		t.Errorf("%d test units list the machine's /proc with no /proc of their own and no host-proc: line saying why:\n  %s\n\nGive each a /proc of its own (as useContainerInit does for the launcher), or put a `// host-proc: <why>` line on or above the listing, or in the doc of the test or the helper that lists. The whole audit:\n%s",
			len(bad), strings.Join(bad, "\n  "), r.text.String())
	}
	t.Logf("%d test units list the machine's /proc, none without a root or a reason", len(r.units))
}

// The audit decides each of its fixture's cases as that case's comment says: a test that lists the
// machine's /proc itself, one that says why, one that gives the scan a root of its own and one that
// does not, a test whose two subtests differ, and a gate that admits only the tests that set its
// variable. The fixture is a module of its own, read with no workspace.
func TestTheAuditDecidesEachCase(t *testing.T) {
	r := runAudit("testdata/cases", "", fixtureEnv())
	if r.unknown != "" {
		t.Fatalf("UNKNOWN: %s\n%s", r.unknown, r.text.String())
	}
	want := map[string]string{
		"example.com/cases.TestCountsTheMachinesGits":                  "NOT GIVEN",
		"example.com/cases.TestSaysWhyItNeedsTheMachine":               "ANNOTATED",
		"example.com/cases.TestGivesTheScanARootOfItsOwn":              "OWN ROOT",
		"example.com/cases.TestScansWithTheDefaultRoot":                "NOT GIVEN",
		"example.com/cases.TestSubtests/own root (TestSubtests$1)":     "OWN ROOT",
		"example.com/cases.TestSubtests/default root (TestSubtests$2)": "NOT GIVEN",
		"example.com/cases.TestStandsInWithNoRoot":                     "NOT GIVEN",
		"example.com/cases.TestStandsInWithARoot":                      "OWN ROOT",
	}
	if !reflect.DeepEqual(r.units, want) {
		t.Fatalf("the audit decided\n  %v\nwant\n  %v\nThe whole audit:\n%s", r.units, want, r.text.String())
	}
}

// An audit that read no module, or a module whose tests list nothing, says UNKNOWN and decides
// nothing: an empty answer is no pass.
func TestTheAuditSaysUnknownWhenItReadNothing(t *testing.T) {
	for name, dir := range map[string]string{"no module": t.TempDir(), "no listing": "testdata/nolisting"} {
		r := runAudit(dir, "", fixtureEnv())
		if r.unknown == "" || len(r.units) != 0 {
			t.Errorf("%s: unknown %q, %d units decided, want UNKNOWN and none:\n%s", name, r.unknown, len(r.units), r.text.String())
		}
	}
}

// fixtureEnv runs the go command outside this repository's go.work, which does not hold the
// fixtures' modules.
func fixtureEnv() []string {
	return append(os.Environ(), "GOWORK=off")
}
