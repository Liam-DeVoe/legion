// packages/envoy/internal/broker/judge/private_project_test.go
// The judge's project check: a secret of a private project, or of a project Dispatch does not
// hold, is left to a person, and an untagged request is judged as it was before the check existed.
package judge

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestTheJudgeLeavesAPrivateProjectsSecretToItsPerson: a request for a secret whose
// dispatch-project tag names a private project, or a project Dispatch does not hold, stops at the
// project check. The model is never asked and Dispatch is never read, so the person the request
// names decides it. A project whose state could not be read stops the run too: the judge fails
// closed.
func TestTheJudgeLeavesAPrivateProjectsSecretToItsPerson(t *testing.T) {
	for _, c := range []struct {
		name, wantDetail string
		machine          func(*fakeRequests)
	}{
		{"a private project's secret", "belongs to a private project", func(f *fakeRequests) { f.private = true }},
		{"a project whose state could not be read", "could not be read", func(f *fakeRequests) { f.projectErr = errors.New("dispatch answered 503") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dispatch, model, machine := newDispatch(t, "doing", testSpec), newModel(t, verdictReply(Approve, "yes")), newRequests()
			c.machine(machine)
			New(t.Context(), testOptions(t, dispatch, model, machine)).consider(t.Context(), pending())

			row := theOneRow(t, machine)
			if got := failedCheck(t, row); got != checkProject {
				t.Fatalf("the row names %q as the check that failed; want %q: the judge decided a private project's secret", got, checkProject)
			}
			if detail := failedDetail(t, row); !strings.Contains(detail, testSecret) || !strings.Contains(detail, c.wantDetail) {
				t.Errorf("the project check reads %q; want it to name %s and say %q", detail, testSecret, c.wantDetail)
			}
			if row["verdict"] != Refer || row["asked"] != false || model.asked() != 0 {
				t.Errorf("the row reads verdict %v, asked %v, and the model got %d requests; want a referral the model was never asked for", row["verdict"], row["asked"], model.asked())
			}
			if len(dispatch.got()) != 0 {
				t.Errorf("Dispatch got %v; want no read: the project check stops the run before the issue", dispatch.got())
			}
			if got := machine.projectsAsked(); !slices.Equal(got, []string{testSecret}) {
				t.Errorf("the judge asked the project of %v; want %s alone", got, testSecret)
			}
			if got := machine.reasonsAsked(); !slices.Equal(got, []string{pending().Reason}) {
				t.Errorf("the judge asked the project with reasons %q; want the request's own signed reason, whose issue keys can make it a private project's", got)
			}
		})
	}
}

// TestTheJudgeOfAnUntaggedRequestAsksDispatchNothingNew: a request for a secret of no private
// project passes the project check and goes on exactly as before it existed — the seven checks
// in their order, the issue and spec reads it always made and no other Dispatch route, and the
// model's approval applied.
func TestTheJudgeOfAnUntaggedRequestAsksDispatchNothingNew(t *testing.T) {
	dispatch, model, machine := newDispatch(t, "doing", testSpec), newModel(t, verdictReply(Approve, "yes")), newRequests()
	New(t.Context(), testOptions(t, dispatch, model, machine)).consider(t.Context(), pending())

	applied, logged := machine.rows()
	if len(applied) != 1 || len(logged) != 0 {
		t.Fatalf("the run applied %d decisions and logged %d rows; want the approval applied", len(applied), len(logged))
	}
	checks, ok := applied[0]["checks"].([]check)
	if !ok {
		t.Fatalf("the row's checks are %T", applied[0]["checks"])
	}
	var names []string
	for _, c := range checks {
		names = append(names, c.Check)
		if !c.Passed {
			t.Errorf("check %s failed: %s", c.Check, c.Detail)
		}
	}
	want := []string{checkRulesJudge, checkLifetime, checkPolicy, checkAdmin, checkWithheld, checkProject, checkIssue}
	if !slices.Equal(names, want) {
		t.Errorf("the run made the checks %v; want %v, the project check after withheld and before the issue", names, want)
	}
	for _, call := range dispatch.got() {
		if !strings.HasPrefix(call.path, "/api/v1/issues/") {
			t.Errorf("Dispatch got %s %s; want only the issue and spec reads the judge always made", call.method, call.path)
		}
	}
	if len(dispatch.got()) != 2 {
		t.Errorf("Dispatch got %d requests; want the issue read and the spec read: %v", len(dispatch.got()), dispatch.got())
	}
}
