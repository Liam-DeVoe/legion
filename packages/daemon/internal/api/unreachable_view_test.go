package api

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// read_state and read_record show a claim out of reach as unreachable, with the moment the 20
// minutes were counted from and the last registration the daemon read, and the claim keeps its
// state (R5); a claim not marked carries no such key.
func TestAClaimViewShowsTheUnreachableMarkAndKeepsItsState(t *testing.T) {
	since := time.Date(2026, 11, 12, 15, 51, 1, 0, time.UTC)
	marked := supervise.Claim{State: supervise.StateWorking, Reach: supervise.Reach{Session: "ses", Since: since, LastSeen: since}}
	view := ClaimViewOf(marked)
	if view.State != string(supervise.StateWorking) {
		t.Fatalf("the marked claim's view reads state %q, want working: the mark changes no state", view.State)
	}
	if view.Unreachable == nil || !view.Unreachable.Since.Equal(since) || view.Unreachable.LastSeen == nil || !view.Unreachable.LastSeen.Equal(since) {
		t.Fatalf("the marked claim's view = %+v, want unreachable since %s with its last registration", view.Unreachable, since)
	}
	if reached := ClaimViewOf(supervise.Claim{State: supervise.StateIdle, Reach: supervise.Reach{Session: "ses", Reported: true}}); reached.Unreachable != nil {
		t.Fatalf("a claim not marked shows %+v, want no unreachable key", reached.Unreachable)
	}
	encoded, err := json.Marshal(ClaimViewOf(supervise.Claim{State: supervise.StateIdle}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"unreachable"`)) {
		t.Fatalf("a claim not marked is written %s, want no unreachable key", encoded)
	}
}

// A caller from a pod below the contract that added the key is served the state without it: its
// plugin reads the claim views with a schema that knows no such key.
func TestTheStateServedBelowTheUnreachableContractCarriesNoMark(t *testing.T) {
	since := time.Date(2026, 11, 12, 15, 51, 1, 0, time.UTC)
	state := State{Issues: map[string]Issue{"LEGION-209": {Key: "LEGION-209",
		Architect: &ClaimView{State: "idle", Unreachable: &UnreachableView{Since: since}},
	}}, ControllerLocator: &ControllerLocator{SessionID: "ses_controller", Unreachable: &UnreachableView{Since: since}}}
	if !carriesUnreachable(state) {
		t.Fatal("carriesUnreachable is false for a state with a marked claim")
	}
	served := withoutUnreachable(state)
	if served.Issues["LEGION-209"].Architect.Unreachable != nil || served.ControllerLocator.Unreachable != nil {
		t.Fatalf("the state served below %d still carries the mark: %+v", unreachableContract, served)
	}
	if state.Issues["LEGION-209"].Architect.Unreachable == nil || state.ControllerLocator.Unreachable == nil {
		t.Fatal("the strip changed the state the source answered")
	}
	if unreachableContract != DaemonAPIVersion {
		t.Fatalf("unreachableContract = %d, want this change's contract %d", unreachableContract, DaemonAPIVersion)
	}
}
