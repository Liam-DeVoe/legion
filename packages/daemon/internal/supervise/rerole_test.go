package supervise

import (
	"context"
	"errors"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// stepStands is the reading of the record a re-role is asked under (Machine.Rerole) for a step its
// issue still asks for.
func stepStands(context.Context) (bool, error) { return true, nil }

// A claim whose agent's next step is of another role takes that role while it runs no process: the
// store holds the new role, and the next generation launches with it on the same claim and the same
// session, so the agent does that step with its role's prompt and App. A claim that kept its role
// would do the tester's step as the implementer.
func TestARerolePersistsTheRoleAndRelaunchesOnTheSameSession(t *testing.T) {
	h := newHarness(t)
	h.reach(StateIdle)
	h.must(RequestSuspend{Claim: testToken, By: SuspendedByOperator})

	if err := h.m.Rerole(context.Background(), claim.RoleTester, stepStands); err != nil {
		t.Fatalf("re-role the suspended claim: %v", err)
	}
	if stored := h.store.load(h.token); stored.Role != claim.RoleTester {
		t.Fatalf("the stored claim's role = %s, want tester", stored.Role)
	}

	h.must(RequestResume{Claim: testToken})
	resume := h.wantCalls("Resume", 1)[0]
	if resume.Spec.Role != claim.RoleTester || resume.Spec.Claim != testToken || resume.Spec.ResumeSessionFile != sessionFile {
		t.Fatalf("relaunched %+v, want the tester's role on %s resuming %s", resume.Spec, testToken, sessionFile)
	}
}

// A claim that still runs a process keeps its role: its process was started with the role's prompt
// and App, so the re-role is refused and the store is not written.
func TestAReroleOfAClaimThatRunsAProcessIsRefused(t *testing.T) {
	h := newHarness(t)
	h.reach(StateIdle)

	err := h.m.Rerole(context.Background(), claim.RoleTester, stepStands)
	if !errors.Is(err, ErrRunsAProcess) {
		t.Fatalf("the re-role of an idle claim = %v, want ErrRunsAProcess", err)
	}
	if got := h.claim().Role; got != claim.RoleImplementer {
		t.Fatalf("the claim's role = %s, want implementer", got)
	}
	if stored := h.store.load(h.token); stored.Role != claim.RoleImplementer {
		t.Fatalf("the stored claim's role = %s, want implementer", stored.Role)
	}
}

// The step a re-role is for is judged again under the machine's lock, on the record as it stands
// then: a step the issue has left since the start read it is ErrStepLeft, and a reading that fails
// is that failure. Either way the claim keeps its role and the store is not written. The control is
// the same re-role of a step that stands.
func TestAReroleForAStepTheIssueLeftIsRefused(t *testing.T) {
	failed := errors.New("the record did not answer")
	for _, tc := range []struct {
		name   string
		stands func(context.Context) (bool, error)
		want   error
	}{
		{name: "a step the issue still asks for", stands: stepStands},
		{name: "a step the issue has left", stands: func(context.Context) (bool, error) { return false, nil }, want: ErrStepLeft},
		{name: "a reading of the step that fails", stands: func(context.Context) (bool, error) { return false, failed }, want: failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.reach(StateIdle)
			h.must(RequestSuspend{Claim: testToken, By: SuspendedByDaemon})

			err := h.m.Rerole(context.Background(), claim.RoleTester, tc.stands)
			if tc.want == nil {
				if err != nil || h.store.load(h.token).Role != claim.RoleTester {
					t.Fatalf("the re-role of a step that stands = %v, stored role %s; want the tester's", err, h.store.load(h.token).Role)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("the re-role = %v, want %v", err, tc.want)
			}
			if got, stored := h.claim().Role, h.store.load(h.token).Role; got != claim.RoleImplementer || stored != claim.RoleImplementer {
				t.Fatalf("the claim holds %s and the store %s, want the implementer in both", got, stored)
			}
		})
	}
}
