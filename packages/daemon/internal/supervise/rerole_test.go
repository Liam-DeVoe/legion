package supervise

import (
	"context"
	"errors"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A claim whose agent's next step is of another role takes that role while it runs no process: the
// store holds the new role, and the next generation launches with it on the same claim and the same
// session, so the agent does that step with its role's prompt and App. A claim that kept its role
// would do the tester's step as the implementer.
func TestARerolePersistsTheRoleAndRelaunchesOnTheSameSession(t *testing.T) {
	h := newHarness(t)
	h.reach(StateIdle)
	h.must(RequestSuspend{Claim: testToken, By: SuspendedByOperator})

	if err := h.m.Rerole(context.Background(), claim.RoleTester); err != nil {
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

	err := h.m.Rerole(context.Background(), claim.RoleTester)
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
