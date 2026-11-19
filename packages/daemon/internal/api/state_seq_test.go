package api

import (
	"encoding/json"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The state the fixture builds (populatedState) is the body of a daemon whose snapshot holds the
// project's state events up to 43 (DaemonInfo.StateSeq), counted in the store whose id it names
// (DaemonInfo.StateID), so one state proves the two keys come off the wire below their contract
// and stay on it at that contract.

// stateSeqServed is the `daemon.stateSeq` of a state body, and whether the key is on the wire at
// all. The two are read apart because 0 is a sequence the route serves, a project with no state
// event yet, and an absent key is the body served to a caller below the contract: the one must
// never read as the other.
func stateSeqServed(t *testing.T, body []byte) (seq int64, carried bool) {
	t.Helper()
	raw, carried := daemonKeyServed(t, body, "stateSeq")
	if !carried {
		return 0, false
	}
	if err := json.Unmarshal(raw, &seq); err != nil {
		t.Fatalf("decode daemon.stateSeq %s: %v", raw, err)
	}
	return seq, true
}

// stateIDServed is the `daemon.stateId` of a state body, "" where the key is not on the wire.
func stateIDServed(t *testing.T, body []byte) string {
	t.Helper()
	raw, carried := daemonKeyServed(t, body, "stateId")
	if !carried {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		t.Fatalf("decode daemon.stateId %s: %v", raw, err)
	}
	return id
}

// daemonKeyServed is the key of a state body's daemon block as it is on the wire, and whether it
// is there at all.
func daemonKeyServed(t *testing.T, body []byte, key string) (json.RawMessage, bool) {
	t.Helper()
	var state struct {
		Daemon map[string]json.RawMessage `json:"daemon"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode state %s: %v", body, err)
	}
	raw, carried := state.Daemon[key]
	return raw, carried
}

// The route authenticates nobody, so it knows a plugin by the pod the request came from: a pod
// whose claims record a contract below stateSeqContract is served the daemon block with neither
// `stateSeq` nor `stateId`, since a plugin built before sjawhar/legion#2788 reads the state with a
// strict schema that refuses the whole body for a key it does not name. A pod at the contract, a
// pod whose claims record no contract, and a caller the daemon cannot place in a pod — the
// operator's `legion state`, and Dispatch, which reads the sequence and the store id to line the
// body up with the state events — are served both keys as the source answered them.
func TestTheStateSeqIsStrippedBelowItsContract(t *testing.T) {
	full := stateFrom(t, populatedState(t), "192.0.2.1:41000", podClaims(stateSeqContract-1))
	want, carried := stateSeqServed(t, full)
	wantID := stateIDServed(t, full)
	if !carried || want == 0 || wantID == "" {
		t.Fatalf("the fixture's state %s carries no stateSeq above 0 or no stateId; this test proves nothing", full)
	}

	for _, tc := range []struct {
		name    string
		remote  string
		claims  []supervise.Claim
		carried bool
	}{
		{name: "a pod one contract below it", remote: statePodAddr + ":41000", claims: podClaims(stateSeqContract - 1)},
		{name: "a pod below PodUpdateContract, below every one of them", remote: statePodAddr + ":41000", claims: podClaims(supervise.PodUpdateContract - 1)},
		{name: "the same pod asking over IPv6", remote: "[::ffff:" + statePodAddr + "]:41000", claims: podClaims(stateSeqContract - 1)},
		{name: "a pod at the contract", remote: statePodAddr + ":41000", claims: podClaims(stateSeqContract), carried: true},
		{name: "a pod whose claims record no contract", remote: statePodAddr + ":41000", claims: podClaims(0), carried: true},
		{name: "a caller in no pod", remote: "192.0.2.1:41000", claims: podClaims(stateSeqContract - 1), carried: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := stateFrom(t, populatedState(t), tc.remote, tc.claims)
			seq, carried := stateSeqServed(t, body)
			id := stateIDServed(t, body)
			if !tc.carried {
				if carried || id != "" {
					t.Errorf("the state %s carries daemon.stateSeq = %d (on the wire = %v) or daemon.stateId = %q; the reader before stateSeqContract refuses both keys", body, seq, carried, id)
				}
				return
			}
			if !carried || seq != want || id != wantID {
				t.Errorf("the state %s carries daemon.stateSeq %d (the key is on the wire = %v) and daemon.stateId %q, want the source's %d and %q", body, seq, carried, id, want, wantID)
			}
		})
	}
}

// A project whose daemon has committed no state event yet is served `stateSeq: 0`, never an
// absent key: the pointer keeps the sequence 0 apart from the body served down, which a reader at
// this contract takes as a daemon that serves no sequence at all.
func TestAStateSeqOfZeroIsServedAtItsContract(t *testing.T) {
	state := populatedState(t)
	state.Daemon.StateSeq = new(int64(0))
	body := stateFrom(t, state, statePodAddr+":41000", podClaims(stateSeqContract))
	if seq, carried := stateSeqServed(t, body); !carried || seq != 0 {
		t.Fatalf("the state %s carries daemon.stateSeq %d (the key is on the wire = %v), want 0 on the wire", body, seq, carried)
	}
}

// The body a pod below the contract is served is a copy: the next caller of the same daemon, in no
// pod, is served the state as the source answered it, sequence and all.
func TestTheStateServedToAPodBeforeTheStateSeqContractLeavesTheNextCallersStateWhole(t *testing.T) {
	handler := stateServer(t, populatedState(t), podClaims(stateSeqContract-1))

	behind := stateAsked(t, handler, statePodAddr+":41000")
	operator := stateAsked(t, handler, "192.0.2.1:41000")

	if _, carried := stateSeqServed(t, behind); carried {
		t.Fatalf("the pod before the contract was served %s, want no stateSeq", behind)
	}
	if seq, carried := stateSeqServed(t, operator); !carried || seq == 0 {
		t.Fatalf("the operator was served %s, want the sequence the source answered", operator)
	}
}

// stateSeqContract is the contract whose entry in version.go added the key, and this daemon's own
// contract is never below it: a caller at this contract is served the sequence.
func TestTheStateSeqContractIsTheContractThatAddedTheKey(t *testing.T) {
	const addedStateSeq = 166
	if stateSeqContract != addedStateSeq {
		t.Fatalf("stateSeqContract = %d, want %d, the contract whose entry in version.go added `daemon.stateSeq`",
			stateSeqContract, addedStateSeq)
	}
	if stateSeqContract > DaemonAPIVersion {
		t.Fatalf("stateSeqContract = %d, above DaemonAPIVersion %d: no caller could be served the key",
			stateSeqContract, DaemonAPIVersion)
	}
}
