package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// claimOperationRoutes is the controller's four claim operations, each by its route and the
// words its refusal of the role `controller` gives.
var claimOperationRoutes = []struct {
	name             string
	route            string
	notTheController string
}{
	{"stop", "/legion/v1/claims/stop", stopOp.notTheController},
	{"retry", "/legion/v1/claims/retry", retryOp.notTheController},
	{"suspend", "/legion/v1/claims/suspend", suspendOp.notTheController},
	{"resume", "/legion/v1/claims/resume", resumeOp.notTheController},
}

// askClaimOperation posts one of the controller's claim operations with a fresh controller grant.
func askClaimOperation(t *testing.T, h *harness, route, issue string, role claim.Role) *httptest.ResponseRecorder {
	t.Helper()
	return h.request(http.MethodPost, route, ClaimRequest{GrantID: controllerGrant(t, h).GrantID, Issue: issue, Role: string(role)}, nil)
}

// wantClaimOperationAnswer holds a success of the controller's claim operations to its status and
// its body: the claim as the request left it.
func wantClaimOperationAnswer(t *testing.T, recorder *httptest.ResponseRecorder, status int, want ClaimOperationResponse) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body %s", recorder.Code, status, recorder.Body)
	}
	var got ClaimOperationResponse
	decodeInto(t, recorder, &got)
	if got != want {
		t.Fatalf("answer = %+v, want %+v", got, want)
	}
}

// operatorSuspendedImplementer is readyImplementer suspended through the operator's own route,
// whose suspension only the operator lifts.
func operatorSuspendedImplementer(t *testing.T, h *harness) claim.Token {
	t.Helper()
	token := readyImplementer(t, h)
	if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(token)+"/suspend", nil); recorder.Code != http.StatusOK {
		t.Fatalf("the operator's suspend = %d, want 200; body %s", recorder.Code, recorder.Body)
	}
	return token
}

// controllerSuspendedImplementer is readyImplementer suspended through the controller's own
// route, which records the suspension as the controller's.
func controllerSuspendedImplementer(t *testing.T, h *harness) claim.Token {
	t.Helper()
	token := readyImplementer(t, h)
	if recorder := askClaimOperation(t, h, "/legion/v1/claims/suspend", "LEGION-209", claim.RoleImplementer); recorder.Code != http.StatusOK {
		t.Fatalf("the controller's suspend = %d, want 200; body %s", recorder.Code, recorder.Body)
	}
	if got := h.stored(token); got.State != supervise.StateSuspended || got.SuspendedBy != supervise.SuspendedByController {
		t.Fatalf("the worker is %s suspended by %q, want it suspended by the controller", got.State, got.SuspendedBy)
	}
	return token
}

// workingImplementer is readyImplementer in a turn: the claim stored working whose process the
// fake runtime answers for as running.
func workingImplementer(t *testing.T, h *harness) claim.Token {
	t.Helper()
	token := readyImplementer(t, h)
	m, _ := h.supervisor.Machine(token)
	if err := m.Handle(h.ctx, supervise.StreamTurnStart{Claim: token}); err != nil {
		t.Fatalf("start %s's turn: %v", token, err)
	}
	if got := m.View().State; got != supervise.StateWorking {
		t.Fatalf("the worker is %s, want working", got)
	}
	return token
}

// Each of the four goes through the door fresh_session and compact share: a grant that is not
// the controller's is refused, the role `controller` names no claim, and a claim the daemon does
// not supervise is answered 404.
//
// The 404 is also the whole of the refusal of another project's claim. The door builds the token
// from the daemon's own project (claim.NewToken(s.project, …)), so a request can name an issue
// and a role and never a project: another project's claim reaches this daemon only as an issue it
// supervises no claim for, which is what LEGION-777's tester is here. That is the only form such
// a claim can take, so NO_CLAIM is the only answer it can get.
func TestTheControllersClaimOperationsNeedAControllerGrantAClaimRoleAndAClaimOfItsProject(t *testing.T) {
	for _, op := range claimOperationRoutes {
		t.Run(op.name, func(t *testing.T) {
			h := freshSessionHarness(t, &phaseRestarter{})
			token, _ := h.launchIn("LEGION-208", "LEGION-209", claim.RoleImplementer)
			registered := h.registered(h.bootToken(token), "ses_implementer")
			var worker GrantResponse
			minted := h.request(http.MethodPost, "/legion/v1/grants", GrantRequest{
				SessionID: "ses_implementer", Secret: registered.Secret, Tree: "LEGION-208", Issue: "LEGION-209",
			}, nil)
			if minted.Code != http.StatusOK {
				t.Fatalf("mint the worker's grant = %d; body %s", minted.Code, minted.Body)
			}
			decodeInto(t, minted, &worker)
			before := h.stored(token)

			assertFailure(t, h.request(http.MethodPost, op.route,
				ClaimRequest{GrantID: worker.GrantID, Issue: "LEGION-209", Role: string(claim.RoleImplementer)}, nil),
				http.StatusForbidden, "CONTROLLER_REQUIRED")

			wantFreshSessionRefusal(t, askClaimOperation(t, h, op.route, "LEGION-209", claim.RoleController),
				http.StatusBadRequest, "INVALID_ROLE",
				"role must be one of architect, planner, implementer, tester, reviewer, merger; "+
					"the project's controller is none of them, and "+op.notTheController)

			// Another project's claim: only an issue this daemon supervises no claim for.
			assertFailure(t, askClaimOperation(t, h, op.route, "LEGION-777", claim.RoleTester),
				http.StatusNotFound, "NO_CLAIM")

			if after := h.stored(token); after.State != before.State || after.Session != before.Session {
				t.Errorf("the refused requests left the claim %s on session %q, want it %s on %q as they found it",
					after.State, after.Session, before.State, before.Session)
			}
		})
	}
}

// The controller's suspend runs the operator's effect and records the suspension as its own:
// the claim's process stops, its session is kept, and the answer and the store name the
// controller. Its own resume lifts it, and starts the claim again in that session.
func TestTheControllerSuspendsAClaimAsItsOwnAndResumesIt(t *testing.T) {
	h := freshSessionHarness(t, &phaseRestarter{})
	token := readyImplementer(t, h)

	wantClaimOperationAnswer(t, askClaimOperation(t, h, "/legion/v1/claims/suspend", "LEGION-209", claim.RoleImplementer),
		http.StatusOK, ClaimOperationResponse{State: "suspended", SuspendedBy: "controller"})
	stored := h.stored(token)
	if stored.State != supervise.StateSuspended || stored.SuspendedBy != supervise.SuspendedByController || stored.Session != "ses_implementer" {
		t.Fatalf("the claim is stored %s suspended by %q on session %q, want it suspended by the controller on ses_implementer",
			stored.State, stored.SuspendedBy, stored.Session)
	}
	if !stored.RuleHolds() {
		t.Fatal("the controller's suspension does not hold against the daemon's rules")
	}

	wantClaimOperationAnswer(t, askClaimOperation(t, h, "/legion/v1/claims/resume", "LEGION-209", claim.RoleImplementer),
		http.StatusOK, ClaimOperationResponse{State: "launching"})
	if resume := h.runtime.CallsOf("Resume"); len(resume) != 1 {
		t.Fatalf("the resume made %d resumes of the session, want 1", len(resume))
	}
}

// A suspension that reaches a claim in a turn is held for that turn's end, as the operator's is,
// and answered 202 with the hold named as the controller's.
func TestTheControllersSuspensionOfAClaimInATurnIsHeldForTheTurn(t *testing.T) {
	h := freshSessionHarness(t, &phaseRestarter{})
	token := workingImplementer(t, h)

	wantClaimOperationAnswer(t, askClaimOperation(t, h, "/legion/v1/claims/suspend", "LEGION-209", claim.RoleImplementer),
		http.StatusAccepted, ClaimOperationResponse{State: "working", SuspensionHeld: true, SuspensionHeldBy: "controller"})
	m, _ := h.supervisor.Machine(token)
	if err := m.Handle(h.ctx, supervise.StreamTurnEnd{Claim: token}); err != nil {
		t.Fatalf("end the turn: %v", err)
	}
	if got := h.stored(token); got.State != supervise.StateSuspended || got.SuspendedBy != supervise.SuspendedByController {
		t.Fatalf("after the turn the claim is %s suspended by %q, want it suspended by the controller", got.State, got.SuspendedBy)
	}
}

// The controller's stop runs the operator's: the claim is released and retired. The
// controller's plain retry then starts a stopped phase worker through its issue's phase, as the
// operator's retry does, and asks for no fresh session.
func TestTheControllerStopsAClaimAndRetriesIt(t *testing.T) {
	facts := &phaseRestarter{}
	h := freshSessionHarness(t, facts)
	token := readyImplementer(t, h)

	wantClaimOperationAnswer(t, askClaimOperation(t, h, "/legion/v1/claims/stop", "LEGION-209", claim.RoleImplementer),
		http.StatusOK, ClaimOperationResponse{State: "retired"})
	if got := h.stored(token); got.State != supervise.StateRetired || got.Session != "ses_implementer" {
		t.Fatalf("the stopped claim is %s on session %q, want it retired with its session kept", got.State, got.Session)
	}

	wantClaimOperationAnswer(t, askClaimOperation(t, h, "/legion/v1/claims/retry", "LEGION-209", claim.RoleImplementer),
		http.StatusAccepted, ClaimOperationResponse{State: "retired"})
	applied := facts.recorded()
	if len(applied) != 1 {
		t.Fatalf("the retry applied %#v, want the phase's restart alone", applied)
	}
	restart, ok := applied[0].(intake.RestartPhase)
	if !ok || restart.Issue != "LEGION-209" || restart.Role != claim.RoleImplementer || restart.FreshSession {
		t.Fatalf("the retry applied %#v, want LEGION-209's implementing restarted with no fresh session", applied[0])
	}
	if got := h.stored(token).Session; got != "ses_implementer" {
		t.Errorf("the retry left the claim recording session %q, want the session it had: a plain retry keeps it", got)
	}
}

// The retry and the resume reach no claim in a turn, and each refuses it in the code both
// recoveries give a claim stored working, the retry before anything is asked and the resume from
// the machine's own refusal, with nothing of the claim changed.
func TestTheControllersRetryAndResumeRefuseAClaimInATurn(t *testing.T) {
	for _, op := range []struct {
		route string
		op    controllerOp
	}{
		{"/legion/v1/claims/retry", retryOp},
		{"/legion/v1/claims/resume", resumeOp},
	} {
		t.Run(op.op.name, func(t *testing.T) {
			h := freshSessionHarness(t, &phaseRestarter{})
			token := workingImplementer(t, h)

			wantFreshSessionRefusal(t, askClaimOperation(t, h, op.route, "LEGION-209", claim.RoleImplementer),
				http.StatusConflict, claimInATurn,
				string(token)+" is stored working, and "+op.op.name+" reaches no claim in a turn. "+op.op.reaches+". "+wantAskAgainLater)
			m, _ := h.supervisor.Machine(token)
			if view := m.View(); view.State != supervise.StateWorking || view.SuspensionHeld {
				t.Fatalf("the refused claim is %s with a suspension held %t, want it working with none", view.State, view.SuspensionHeld)
			}
		})
	}
}

// The operator's own suspension is the operator's decision, and only the operator undoes it: the
// controller's resume would lift it, its suspend would take it over, its stop would retire the
// claim for its retry to relaunch, its retry would start it, and its fresh session would resume
// it. Each of the five is refused SUSPENDED_BY_OPERATOR with nothing of the claim changed, and the
// operator's own resume then lifts the suspension as before.
func TestTheControllerCannotGetPastTheOperatorsSuspension(t *testing.T) {
	for _, op := range []struct {
		name  string
		route string
		as    controllerOp
	}{
		{"resume", "/legion/v1/claims/resume", resumeOp},
		{"suspend", "/legion/v1/claims/suspend", suspendOp},
		{"stop", "/legion/v1/claims/stop", stopOp},
		{"retry", "/legion/v1/claims/retry", retryOp},
		{"fresh_session", "/legion/v1/claims/fresh-session", freshSessionOp},
	} {
		t.Run(op.name, func(t *testing.T) {
			h := freshSessionHarness(t, &phaseRestarter{})
			token := operatorSuspendedImplementer(t, h)
			before := h.stored(token)

			wantFreshSessionRefusal(t, askClaimOperation(t, h, op.route, "LEGION-209", claim.RoleImplementer),
				http.StatusConflict, suspendedByOperator,
				string(token)+" is held by the operator's own suspension, which only the operator lifts, so "+op.as.name+
					" by the controller is refused and nothing of the claim changed. Ask the operator: `legion claims resume --claim "+
					string(token)+"` is theirs to run")
			after := h.stored(token)
			if after.State != supervise.StateSuspended || after.SuspendedBy != supervise.SuspendedByOperator || after.Session != before.Session {
				t.Fatalf("the refused claim is %s suspended by %q on session %q, want it suspended by the operator on %q",
					after.State, after.SuspendedBy, after.Session, before.Session)
			}
			if resumes := h.runtime.CallsOf("Resume"); len(resumes) != 0 {
				t.Fatalf("the refused request resumed the session %d times, want none", len(resumes))
			}

			if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(token)+"/resume", nil); recorder.Code != http.StatusOK {
				t.Fatalf("the operator's resume = %d, want 200; body %s", recorder.Code, recorder.Body)
			}
			if got := h.stored(token).State; got != supervise.StateLaunching {
				t.Fatalf("after the operator's resume the claim is %s, want launching", got)
			}
		})
	}
}

// The controller's stop of a claim whose operator's suspension waits for the turn's end is
// refused as well: the hold is the operator's decision already, and the claim is left working
// under it.
func TestTheControllersStopOfAClaimTheOperatorsHeldSuspensionWaitsOnIsRefused(t *testing.T) {
	h := freshSessionHarness(t, &phaseRestarter{})
	token := workingImplementer(t, h)
	if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(token)+"/suspend", nil); recorder.Code != http.StatusAccepted {
		t.Fatalf("the operator's suspend of a claim in a turn = %d, want 202; body %s", recorder.Code, recorder.Body)
	}

	assertFailure(t, askClaimOperation(t, h, "/legion/v1/claims/stop", "LEGION-209", claim.RoleImplementer),
		http.StatusConflict, suspendedByOperator)
	m, _ := h.supervisor.Machine(token)
	if view := m.View(); view.State != supervise.StateWorking || !view.SuspensionHeld || view.SuspensionHeldBy != supervise.SuspendedByOperator {
		t.Fatalf("the claim is %s holding a suspension %t by %q, want it working under the operator's", view.State, view.SuspensionHeld, view.SuspensionHeldBy)
	}
}

// The operator's door is the operator's, and lifts a suspension of either: the operator's
// retry --fresh-session of a claim the operator suspended starts it in a new session, and the
// operator's resume lifts the controller's suspension.
func TestTheOperatorLiftsItsOwnSuspensionAndTheControllers(t *testing.T) {
	t.Run("the operator's fresh session of its own suspension", func(t *testing.T) {
		h := freshSessionHarness(t, &phaseRestarter{})
		token := operatorSuspendedImplementer(t, h)
		recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(token)+"/retry", RetryRequest{FreshSession: true})
		if recorder.Code != http.StatusOK && recorder.Code != http.StatusAccepted {
			t.Fatalf("the operator's fresh session = %d, want it run; body %s", recorder.Code, recorder.Body)
		}
		if got := h.stored(token); got.State != supervise.StateLaunching || got.Session != "" {
			t.Fatalf("the claim is %s on session %q, want it launching in a new session", got.State, got.Session)
		}
	})

	t.Run("the operator's resume of the controller's suspension", func(t *testing.T) {
		h := freshSessionHarness(t, &phaseRestarter{})
		token := controllerSuspendedImplementer(t, h)
		if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(token)+"/resume", nil); recorder.Code != http.StatusOK {
			t.Fatalf("the operator's resume = %d, want 200; body %s", recorder.Code, recorder.Body)
		}
		if got := h.stored(token).State; got != supervise.StateLaunching {
			t.Fatalf("after the operator's resume the claim is %s, want launching", got)
		}
	})
}

// The controller's fresh session of a claim it suspended itself runs: the suspension is its own,
// and so is the resume that starts the claim in a new session.
func TestTheControllersFreshSessionLiftsItsOwnSuspension(t *testing.T) {
	h := freshSessionHarness(t, &phaseRestarter{})
	token := controllerSuspendedImplementer(t, h)

	recorder := askClaimOperation(t, h, "/legion/v1/claims/fresh-session", "LEGION-209", claim.RoleImplementer)

	if recorder.Code != http.StatusOK && recorder.Code != http.StatusAccepted {
		t.Fatalf("the controller's fresh session = %d, want it run; body %s", recorder.Code, recorder.Body)
	}
	if got := h.stored(token); got.State != supervise.StateLaunching || got.Session != "" {
		t.Fatalf("the claim is %s on session %q, want it launching in a new session", got.State, got.Session)
	}
}
