package api

import (
	"bytes"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// neverReconnectedDropped is what the daemon's log names for the narrowing of
// supervise.TaskTakeContract that rewrites the failure cause never_reconnected, named here as a
// literal so a row that drifted from it fails the narrowing tests that read it.
const neverReconnectedDropped = "the failure cause never_reconnected in the claim views' failure and pendingCause, served as never_registered"

// neverReconnectedIssue is an issue whose two claims carry the failure cause
// supervise.TaskTakeContract added: its architect failed under it, the relaunches after a daemon
// start whose shim never said hello having run out, and its implementer is relaunching after one
// such process (pendingCause).
func neverReconnectedIssue() Issue {
	failedAt := time.Date(2026, 11, 17, 9, 14, 3, 0, time.UTC)
	return Issue{Key: "LEGION-214", Phase: phase.Implementing, Status: "in_progress",
		Architect: &ClaimView{Session: "ses_architect", State: string(supervise.StateFailed), PluginContract: supervise.TaskTakeContract,
			Failure: &ClaimFailureView{Cause: supervise.CauseNeverReconnected, Detail: "launch failures ran out", At: &failedAt,
				Attempts: BudgetsView{LaunchFailures: 3}}},
		Workers: map[claim.Role]PhaseView{claim.RoleImplementer: {Claim: ClaimView{Session: "ses_implementer",
			State: string(supervise.StateQueued), PluginContract: supervise.TaskTakeContract,
			PendingCause: supervise.CauseNeverReconnected}, Rounds: 1}}}
}

// neverReconnectedCauses is the two causes a state body serves for LEGION-214: its architect's
// `failure.cause` and its implementer's `pendingCause`.
func neverReconnectedCauses(t *testing.T, body []byte) (failure, pending string) {
	t.Helper()
	var served struct {
		Issues map[string]struct {
			Architect struct {
				Failure struct {
					Cause string `json:"cause"`
				} `json:"failure"`
			} `json:"architect"`
			Workers map[string]struct {
				Claim struct {
					PendingCause string `json:"pendingCause"`
				} `json:"claim"`
			} `json:"workers"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("decode state %s: %v", body, err)
	}
	issue := served.Issues["LEGION-214"]
	return issue.Architect.Failure.Cause, issue.Workers[string(claim.RoleImplementer)].Claim.PendingCause
}

// A plugin below supervise.TaskTakeContract reads `failure.cause` and `pendingCause` through a
// strict enum of the causes it was built with, so never_reconnected, the word that contract added,
// would refuse the whole state body at it, every claim of the project included. Such a caller is
// served never_registered in its place in both keys — the nearest cause it knows, a process that
// never said hello — and the daemon logs the narrowing; a caller at the contract is served the
// word, and the state the source answered keeps it for the next caller.
//
// The negative control is the table without the row (stateNarrowings' never_reconnected row): the
// caller below the contract is then served never_reconnected.
func TestNeverReconnectedIsServedAsNeverRegisteredBelowItsContract(t *testing.T) {
	state := populatedState(t)
	issues := make(map[string]Issue, len(state.Issues)+1)
	maps.Copy(issues, state.Issues)
	issues["LEGION-214"] = neverReconnectedIssue()
	state.Issues = issues

	below, narrowings := stateNarrowed(t, state, statePodAddr+":41000", podClaims(supervise.TaskTakeContract-1))
	if failure, pending := neverReconnectedCauses(t, below); failure != string(supervise.CauseNeverRegistered) ||
		pending != string(supervise.CauseNeverRegistered) {
		t.Errorf("a caller at %d is served failure.cause %q and pendingCause %q, want %q in both: its plugin's enum names no %q",
			supervise.TaskTakeContract-1, failure, pending, supervise.CauseNeverRegistered, supervise.CauseNeverReconnected)
	}
	if bytes.Contains(below, []byte(supervise.CauseNeverReconnected)) {
		t.Errorf("the body served to a caller at %d names %q: %s", supervise.TaskTakeContract-1, supervise.CauseNeverReconnected, below)
	}
	logged := false
	for _, line := range narrowings {
		logged = logged || line.Dropped == neverReconnectedDropped
	}
	if !logged {
		t.Errorf("the daemon wrote %+v for a caller at %d, want a line saying it dropped %q", narrowings, supervise.TaskTakeContract-1, neverReconnectedDropped)
	}

	at := stateFrom(t, state, statePodAddr+":41000", podClaims(supervise.TaskTakeContract))
	if failure, pending := neverReconnectedCauses(t, at); failure != string(supervise.CauseNeverReconnected) ||
		pending != string(supervise.CauseNeverReconnected) {
		t.Errorf("a caller at %d is served failure.cause %q and pendingCause %q, want %q in both", supervise.TaskTakeContract,
			failure, pending, supervise.CauseNeverReconnected)
	}
	source := state.Issues["LEGION-214"]
	if source.Architect.Failure.Cause != supervise.CauseNeverReconnected ||
		source.Workers[claim.RoleImplementer].Claim.PendingCause != supervise.CauseNeverReconnected {
		t.Errorf("the narrowing wrote through to the state the source answered: %+v", source)
	}
}
