package api

import (
	"encoding/json"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The state the fixture builds (populatedState) is the body of a daemon whose snapshot holds the
// project's state events up to 43 (DaemonInfo.StateSeq), so one state proves the key comes off
// the wire below its contract and stays on it at that contract.

// stateSeqServed is the `daemon.stateSeq` of a state body, and whether the key is on the wire at
// all. The two are read apart because 0 is a sequence the route serves, a project with no state
// event yet, and an absent key is the body served to a caller below the contract: the one must
// never read as the other.
func stateSeqServed(t *testing.T, body []byte) (seq int64, carried bool) {
	t.Helper()
	var state struct {
		Daemon map[string]json.RawMessage `json:"daemon"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode state %s: %v", body, err)
	}
	raw, carried := state.Daemon["stateSeq"]
	if !carried {
		return 0, false
	}
	if err := json.Unmarshal(raw, &seq); err != nil {
		t.Fatalf("decode daemon.stateSeq %s: %v", raw, err)
	}
	return seq, true
}

// The route authenticates nobody, so it knows a plugin by the pod the request came from: a pod
// whose claims record a contract below stateSeqContract is served the daemon block with no
// `stateSeq` at all, since a plugin built before sjawhar/legion#2788 reads the state with a
// strict schema that refuses the whole body for a key it does not name. A pod at the contract, a
// pod whose claims record no contract, and a caller the daemon cannot place in a pod — the
// operator's `legion state`, and Dispatch, which reads the sequence to line the body up with the
// state events — are served the key with the sequence the source answered.
func TestTheStateSeqIsStrippedBelowItsContract(t *testing.T) {
	full := stateFrom(t, populatedState(t), "192.0.2.1:41000", podClaims(stateSeqContract-1))
	want, carried := stateSeqServed(t, full)
	if !carried || want == 0 {
		t.Fatalf("the fixture's state %s carries no stateSeq above 0; this test proves nothing", full)
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
			if !tc.carried {
				if carried {
					t.Errorf("the state %s carries daemon.stateSeq = %d; the reader before stateSeqContract refuses the key", body, seq)
				}
				return
			}
			if !carried || seq != want {
				t.Errorf("the state %s carries daemon.stateSeq %d (the key is on the wire = %v), want the source's %d", body, seq, carried, want)
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
