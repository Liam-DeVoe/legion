// ci_job_retract_test.go holds POST /api/v1/results/{id}/retract to R7 and R12 of the spec for a
// result a CI job recorded: a result is appended and never edited, and nobody writes a result for
// work that it did not do, so no caller retracts the job's result, and what a pass of its check
// reads does not move.
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestNobodyRetractsACIJobsResult: the job failed c1 and then passed it at one commit, so c1 reads
// unknown (pass_and_fail). A session that holds no claim, the session holding the issue's claim
// and a person each ask to retract the job's fail: each is refused 403 RETRACTION_FORBIDDEN with
// the reason, no retraction is stored, and c1 still reads unknown. The control is a person's
// retraction of a role's result of the ordinary check k1, which is taken as before.
func TestNobodyRetractsACIJobsResult(t *testing.T) {
	for _, caller := range []string{"a session holding no claim", "the claim holder", "a person"} {
		t.Run(caller, func(t *testing.T) {
			rig := newCIJobRig(t)
			rig.github.setBranch(ciJobBranch, ciJobBranchHead{sha: ciJobCommit})
			failed := jobRecorded(t, "the first attempt's fail", rig.post(t, rig.issue, rig.jobToken(nil), jobResultOf("c1", "fail")))
			jobRecorded(t, "the second attempt's pass", rig.post(t, rig.issue, jobTokenOn(rig, ciJobCommit, ciJobRef, 98), jobResultOf("c1", "pass")))
			if before := passStandsOnWire(t, rig.handler, rig.issue, "c1"); before.Answer != model.PassStandsUnknown || before.Reason != model.PassStandsReasonPassAndFail {
				t.Fatalf("before the retraction c1 reads %+v, want unknown for pass_and_fail", before)
			}

			var retracted *httptest.ResponseRecorder
			switch caller {
			case "a person":
				retracted = retractAsPerson(t, rig.handler, failed.ID, "flaky")
			default:
				if caller == "the claim holder" {
					claimIssueFor(t, rig.database, rig.issue, brokerTestSession)
				}
				retracted = rig.serve(rig.caller.presenting(t, http.MethodPost, "/api/v1/results/"+failed.ID+"/retract",
					rig.sessionToken(), map[string]any{"reason": "flaky"}))
			}
			code, text := refusalOf(t, retracted)
			if retracted.Code != http.StatusForbidden || code != "RETRACTION_FORBIDDEN" {
				t.Fatalf("%s retracts the job's fail: status=%d body=%s, want 403 RETRACTION_FORBIDDEN", caller, retracted.Code, retracted.Body.String())
			}
			for _, says := range []string{"CI job", "appended and never edited", "work that it did not do"} {
				if !strings.Contains(text, says) {
					t.Fatalf("the refusal %q does not say %q", text, says)
				}
			}
			if _, held := storedRetraction(t, rig.database, failed.ID); held {
				t.Fatalf("a retraction of the job's fail is stored after the refusal of %s", caller)
			}
			if after := passStandsOnWire(t, rig.handler, rig.issue, "c1"); after.Answer != model.PassStandsUnknown || after.Reason != model.PassStandsReasonPassAndFail {
				t.Fatalf("after %s's refused retraction c1 reads %+v, want unknown for pass_and_fail", caller, after)
			}
		})
	}
	t.Run("the control: a person retracts a role's result", func(t *testing.T) {
		rig := newCIJobRig(t)
		recorded := rig.post(t, rig.issue, rig.sessionToken(), resultBody("k1", "pass", ciJobCommit, map[string]any{"role": "tester"}))
		if recorded.Code != http.StatusCreated {
			t.Fatalf("the tester's pass of k1: status=%d body=%s", recorded.Code, recorded.Body.String())
		}
		tester := decodeBody[recordedResult](t, recorded).Result
		if retracted := retractAsPerson(t, rig.handler, tester.ID, "wrong commit"); retracted.Code != http.StatusCreated {
			t.Fatalf("a person retracts the tester's pass of k1: status=%d body=%s, want 201", retracted.Code, retracted.Body.String())
		}
		if _, held := storedRetraction(t, rig.database, tester.ID); !held {
			t.Fatal("no retraction of the tester's pass of k1 is stored")
		}
	})
}
