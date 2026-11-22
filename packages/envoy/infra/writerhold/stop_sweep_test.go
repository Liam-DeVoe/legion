package writerhold

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// brokerFails has the stack's broker answer the first request fail takes with its own failure,
// 503 DATABASE_UNAVAILABLE, as a database away for that one request, and serve every other. fail
// sees each request with its body. The hook is removed when the test ends.
func (h *heldStack) brokerFails(fail func(req *http.Request, body []byte) bool) {
	h.t.Helper()
	var mu sync.Mutex
	fired := false
	h.s.Broker.Intercept(func(w http.ResponseWriter, req *http.Request) bool {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return false
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		defer mu.Unlock()
		if fired || !fail(req, body) {
			return false
		}
		fired = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"DATABASE_UNAVAILABLE","error":"the database is away for this request"}`))
		return true
	})
	h.t.Cleanup(func() { h.s.Broker.Intercept(nil) })
}

// journalWrite is a request writing a hold's journal whose latest entry is command's state.
func journalWrite(command, state string) func(*http.Request, []byte) bool {
	return func(req *http.Request, body []byte) bool {
		if req.Method != http.MethodPut || req.URL.Path != "/v1/nats-hold/records" {
			return false
		}
		var write struct {
			Kind  string          `json:"kind"`
			Value holdrecord.Done `json:"value"`
		}
		if json.Unmarshal(body, &write) != nil || write.Kind != "done" || len(write.Value.Ops) == 0 {
			return false
		}
		latest := write.Value.Ops[len(write.Value.Ops)-1]
		return latest.Command == command && latest.State == state
	}
}

// journalReadAfterIntent is the first read of a hold's journal after command's intent is written.
func journalReadAfterIntent(command string) func(*http.Request, []byte) bool {
	intent, armed := journalWrite(command, holdrecord.StateIntent), false
	return func(req *http.Request, body []byte) bool {
		if intent(req, body) {
			armed = true
			return false
		}
		return armed && req.Method == http.MethodGet && req.URL.Path == "/v1/nats-hold/records" && req.URL.Query().Get("kind") == "done"
	}
}

// cohortDelete is a request deleting a hold's cohort record.
func cohortDelete(req *http.Request, _ []byte) bool {
	return req.Method == http.MethodDelete && req.URL.Path == "/v1/nats-hold/records" && req.URL.Query().Get("kind") == "cohort"
}

// Every exit of a held command after an intent writes the command's stop, its own intent or one a
// killed run of it left pending, so no exit leaves an intent that refuses `hold abort` and `hold
// release` busy (the rule of 2026-11-21 17:54Z; round 4's H-1 sweep). Each case fails one request
// of the broker after the intent (or, for a rerun, before its own admission), sees the command
// exit with its stop written, and then ends or reruns the hold. The exits after a row move are the
// exception, kept pending on purpose: `hold abort`'s and `hold release`'s own rerun is admitted on
// that intent and writes the done entry the move left, and a stop there would leave a released row
// whose journal no command ends. Control: 7c6b85127a, where each of these exits returned with the
// intent pending.
func TestHoldEveryExitAfterAnIntentWritesItsStop(t *testing.T) {
	type row struct {
		name    string
		command string
		run     func(h *heldStack) (string, int, string)
		after   func(h *heldStack, id string)
	}
	abortEnds := func(h *heldStack, id string) {
		code, stdout, stderr := h.run(actionAbort, "--hold", id, "--reason", "end the hold after the stop")
		require.Zero(h.t, code, "abort after the stop: %s%s", stdout, stderr)
		require.Equal(h.t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	}
	rows := []row{
		{"hold set, its done entry", holdrecord.CommandSet, func(h *heldStack) (string, int, string) {
			h.seedProtectedStreams()
			h.awaitStreamLeaders()
			h.brokerFails(journalWrite(holdrecord.CommandSet, holdrecord.StateDone))
			code, stdout, stderr := h.run(actionSet, "--workflow", string(holdrecord.WorkflowBucketsCreate), "--reason", "a set whose done write fails")
			return h.row().HoldID, code, stdout + stderr
		}, abortEnds},
		{"hold prove, its done entry", holdrecord.CommandProve, func(h *heldStack) (string, int, string) {
			h.seedProtectedStreams()
			id := h.hubRestoreHold()
			h.disks.Remake(0)
			h.brokerFails(journalWrite(holdrecord.CommandProve, holdrecord.StateDone))
			code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
			return id, code, stderr
		}, func(h *heldStack, id string) {
			code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
			require.Zero(h.t, code, "prove's rerun after its stop: %s", stderr)
			require.Equal(h.t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandProve))
		}},
		{"hold abort, its journal read after its intent", holdrecord.CommandAbort, func(h *heldStack) (string, int, string) {
			h.seedProtectedStreams()
			id := h.hubRestoreHold()
			h.brokerFails(journalReadAfterIntent(holdrecord.CommandAbort))
			code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "an abort whose read fails")
			return id, code, stderr
		}, abortEnds},
		{"hold release, its journal read after its intent", holdrecord.CommandRelease, func(h *heldStack) (string, int, string) {
			id := h.s.Prepare(h.t, holdrecord.WorkflowBucketsCreate)
			h.s.HoldWithoutTheSteps(h.t, id)
			h.brokerFails(journalReadAfterIntent(holdrecord.CommandRelease))
			code, _, stderr := h.run(actionRelease, "--hold", id, "--reason", "a release whose read fails")
			return id, code, stderr
		}, func(h *heldStack, id string) {
			code, _, stderr := h.run(actionRelease, "--hold", id, "--reason", "the release again")
			require.Zero(h.t, code, "release's rerun after its stop: %s", stderr)
			require.Equal(h.t, natsgrant.OperatorPhaseReleased, h.row().Phase)
		}},
		{"hold abort of a row restored released from prepare, its cohort delete", holdrecord.CommandAbort, func(h *heldStack) (string, int, string) {
			ctx := context.Background()
			id := h.s.Prepare(h.t, holdrecord.WorkflowBucketsCreate)
			_, err := h.s.Records.PutRecord(ctx, id, "cohort", "cohort", 0, []byte(`{"boundary":1}`))
			require.NoError(h.t, err)
			_, err = h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, nil)
			require.NoError(h.t, err)
			h.deleteRow()
			code, _, stderr := h.run(actionRestore, "--hold", id, "--reason", "lost in prepare")
			require.Zero(h.t, code, "setup: restore: %s", stderr)
			h.brokerFails(cohortDelete)
			code, _, stderr = h.run(actionAbort, "--hold", id, "--reason", "an abort whose cohort delete fails")
			return id, code, stderr
		}, func(h *heldStack, id string) {
			code, stdout, stderr := h.run(actionAbort, "--hold", id, "--reason", "the abort again")
			require.Zero(h.t, code, "abort's rerun after its stop: %s%s", stdout, stderr)
			require.Equal(h.t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandAbort))
		}},
		{"a held command, its done entry", CommandStreamsUpdate, func(h *heldStack) (string, int, string) {
			h.seedProtectedStreams()
			h.awaitStreamLeaders()
			id := h.s.Prepare(h.t, holdrecord.WorkflowStreamsUpdate)
			h.s.HoldWithoutTheSteps(h.t, id)
			h.brokerFails(journalWrite(CommandStreamsUpdate, holdrecord.StateDone))
			code, _, stderr := h.streamsRun(nil, "update", "--hold", id)
			return id, code, stderr
		}, abortEnds},
		{"a held command's rerun, its connect before its admission", CommandStreamsUpdate, func(h *heldStack) (string, int, string) {
			h.seedProtectedStreams()
			id := h.s.Prepare(h.t, holdrecord.WorkflowStreamsUpdate)
			h.s.HoldWithoutTheSteps(h.t, id)
			_, err := h.s.Records.Admit(context.Background(), id, holdrecord.Request{Command: CommandStreamsUpdate}, nil)
			require.NoError(h.t, err, "setup: a streams update killed after its intent")
			connect := h.op.Connect
			h.op.Connect = func(context.Context) (*nats.Conn, error) { return nil, errors.New("the hub is away") }
			code, _, stderr := h.streamsRun(nil, "update", "--hold", id)
			h.op.Connect = connect
			return id, code, stderr
		}, abortEnds},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := newHeldStack(t)
			id, code, out := r.run(h)
			require.NotZero(t, code, "the command whose request failed exited 0: %s", out)
			require.Equal(t, holdrecord.StateStop, latestState(h.journal(id), r.command), "the exit did not write %s's stop: %v\n%s", r.command, h.journalCommands(id), out)
			r.after(h, id)
		})
	}
}

// The exception to that rule: `hold abort` and `hold release` cut after their row move (here their
// done write fails, as a database away for that request) exit with their intent pending, the row
// released, and no stop. Their own rerun is admitted on that intent, reads the row released, writes
// the done entry the move left and ends the hold. Control: a go test -overlay copy of hold.go whose
// two exits after the move write the stop instead; the rerun then cannot end the hold (abort reads
// the released row as already released and writes nothing; release refuses a released row whose
// journal has not ended), and the row fails naming it.
func TestHoldAbortAndReleaseCutAfterTheirMoveLeaveTheirIntentForTheirRerun(t *testing.T) {
	for _, row := range []struct {
		name, command, action string
		hold                  func(h *heldStack) string
	}{
		{"hold abort", holdrecord.CommandAbort, actionAbort, func(h *heldStack) string {
			h.seedProtectedStreams()
			return h.hubRestoreHold()
		}},
		{"hold release", holdrecord.CommandRelease, actionRelease, func(h *heldStack) string {
			id := h.s.Prepare(h.t, holdrecord.WorkflowBucketsCreate)
			h.s.HoldWithoutTheSteps(h.t, id)
			return id
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			id := row.hold(h)
			h.brokerFails(journalWrite(row.command, holdrecord.StateDone))
			code, stdout, stderr := h.run(row.action, "--hold", id, "--reason", "cut after its move")
			require.NotZero(t, code, "%s whose done write failed exited 0: %s%s", row.name, stdout, stderr)
			require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase, "%s did not move the row before its done write", row.name)
			require.Equal(t, holdrecord.StateIntent, latestState(h.journal(id), row.command), "%s's exit after its move did not leave its intent pending: %v", row.name, h.journalCommands(id))

			code, stdout, stderr = h.run(row.action, "--hold", id, "--reason", "the rerun")
			require.Zero(t, code, "%s's rerun after its cut: %s%s", row.name, stdout, stderr)
			require.Contains(t, stdout, "done entry is now written")
			require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), row.command), "%s's rerun did not end the hold", row.name)
		})
	}
}
