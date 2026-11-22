package cireconcile

import (
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
)

// eachRead runs check twice, once on a head the query answers whole and once on a head whose
// checks GitHub refuses the query, which the pass reads over REST: both reads reach the record
// through the one rule.
func eachRead(t *testing.T, check func(t *testing.T, h *harness)) {
	for _, read := range []struct {
		name string
		rest bool
	}{{name: "answered by the query"}, {name: "read over REST", rest: true}} {
		t.Run(read.name, func(t *testing.T) {
			h := newHarness(t, verdictAppID)
			h.seedProjectDefault(rowRepository)
			h.listHead()
			if read.rest {
				h.github.refuseChecks(gitHubRepository, headSHA)
			}
			check(t, h)
		})
	}
}

// A legion-verdict run is the Legion daemon's own output, not a CI result, so a pass leaves every
// one out of the head's record and its settlement: the verdict App's as its own, and another
// App's as an untrusted verdict, which the pass names at WARN.
func TestLegionVerdictRunsAreLeftOutOfTheReconciledRecord(t *testing.T) {
	eachRead(t, func(t *testing.T, h *harness) {
		h.github.setCheckRuns(gitHubRepository, headSHA,
			inSuite(checkRun(2, contracts.LegionVerdictCheck, "completed", "failure", verdictApp, "2026-11-20T03:00:01Z"), 901),
			checkRun(3, contracts.LegionVerdictCheck, "completed", "failure", otherApp, "2026-11-20T03:00:02Z"),
			checkRun(1, "ci-ok", "completed", "success", otherApp, "2026-11-20T03:00:00Z"),
		)
		h.github.setCheckSuites(gitHubRepository, headSHA,
			checkSuite(900, "completed", "failure", otherApp, "2026-11-20T03:00:03Z"),
			checkSuite(901, "completed", "failure", verdictApp, "2026-11-20T03:00:04Z"),
		)
		settlements := h.subscribe()

		h.pass()

		settled := h.settlement(settlements, "with legion-verdict runs at the head")
		if len(settled.CheckRuns) != 1 || settled.CheckRuns[0].Name != "ci-ok" || settled.Failed.Count != 0 {
			t.Fatalf("settlement = %+v, want ci-ok alone, passing", settled)
		}
		state, _, ok := h.record()
		if !ok {
			t.Fatal("the head has no record")
		}
		if _, held := state.Checks[contracts.LegionVerdictCheck]; held {
			t.Fatalf("the record holds a legion-verdict run: %+v", state.Checks)
		}
		if !h.logged("ci reconcile ignored a verdict check another App created") {
			t.Fatalf("another App's legion-verdict run was not warned about:\n%s", h.logs.String())
		}
	})
}

// The verdict App's own check suite is no part of a head's record either, completed or not, so the
// suite that holds the daemon's legion-verdict run is never recorded beside the head's CI. Another
// App's completed suite is recorded as before.
func TestVerdictAppSuitesAreLeftOutOfTheReconciledRecord(t *testing.T) {
	eachRead(t, func(t *testing.T, h *harness) {
		h.github.setCheckRuns(gitHubRepository, headSHA,
			checkRun(1, "ci-ok", "completed", "success", otherApp, "2026-11-20T03:00:00Z"),
		)
		h.github.setCheckSuites(gitHubRepository, headSHA,
			checkSuite(900, "completed", "success", otherApp, "2026-11-20T03:00:03Z"),
			checkSuite(901, "completed", "success", verdictApp, "2026-11-20T03:00:04Z"),
			checkSuite(902, "queued", "", verdictApp, "2026-11-20T03:00:05Z"),
		)
		settlements := h.subscribe()

		h.pass()

		settled := h.settlement(settlements, "with the verdict App's suites at the head")
		if settled.SHA != headSHA || settled.Passed.Count != 1 {
			t.Fatalf("settlement = %+v, want the head settled on its one passing check", settled)
		}
		state, _, ok := h.record()
		if !ok {
			t.Fatal("the head has no record")
		}
		for _, id := range []string{"901", "902"} {
			if _, held := state.Suites[id]; held {
				t.Fatalf("the record holds the verdict App's suite %s: %+v", id, state.Suites)
			}
		}
		if _, held := state.Suites["900"]; !held {
			t.Fatalf("another App's completed suite is not in the record: %+v", state.Suites)
		}
	})
}
