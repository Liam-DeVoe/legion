package writerhold

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// otherProbeToken names the stack probe's account and audience, and the stack's auth service
// refuses it for its own reason: it lists its own probe token alone, as a real one refuses a token
// another issuer minted or one with a bad signature. A check of the token's claims takes it for the
// admitted token; a check of the whole token does not.
var otherProbeToken = holdtest.PlatformToken(holdtest.ProbeAccount, []string{"envoy-nats"}, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

// `hold prove` counts each restarted server's refusal of the probe as the hold's only through
// Operator.Probe's check against this hold's admitted-probe record, and checks the same before it
// writes anything: a probe token other than the one `hold set` admitted under prepare (one the hub
// refuses for its own reason, or one with the recorded account and audience) stops the prove
// UNKNOWN, naming (b), with nothing written and the row held. With the recorded token the prove is
// done, as before. The reviewer's case: under an auth service whose held refusal is removed
// (decide.go's overlay), the prove with a token the hub refuses was done and a listener was admitted
// under the held row. Control: 870ce0c500, where `hold prove` ran no check and both refused tokens
// gave exit 0 and a done entry.
func TestHoldAProveCountsTheProbesRefusalOnlyForTheRecordedToken(t *testing.T) {
	for _, row := range []struct{ name, token, why string }{
		{"the recorded token", "", ""},
		{"a credential the hub refuses for its own reason", "not-a-listed-token", "(b)"},
		{"the recorded account and audience, another token", otherProbeToken, "(b)"},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			h.seedProtectedStreams()
			id := h.hubRestoreHold()
			h.disks.Remake(0)
			if row.token != "" {
				h.probeWith(row.token)
			}
			before := h.journalCommands(id)
			code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
			if row.why == "" {
				require.Zero(t, code, "the prove with the recorded token: %s", stderr)
				require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandProve))
				return
			}
			require.Equal(t, 2, code, "the prove with %s: %s", row.name, stderr)
			require.Contains(t, stderr, row.why)
			require.Equal(t, before, h.journalCommands(id), "the refused prove wrote an entry")
			require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
		})
	}
}

// RestartProveAndBarrier, which t-7's `restore --finish` runs, reads the hold's admitted-probe
// record from the hold's signed journal and checks the probe's token against it before it restarts
// anything: with no record (a) or another token (b) it is UNKNOWN; with the recorded token it
// restarts and proves every server as before. The reviewer's case: a token the hub refuses, under
// decide.go's held refusal removed, returned nil with a listener admitted under the held row.
// Control: 870ce0c500, whose RestartProveAndBarrier took no journal and returned nil for that token
// (r4m1-red.out, through a scratch row at its signature).
func TestHoldRestartProveAndBarrierCountsTheProbesRefusalOnlyForTheRecordedToken(t *testing.T) {
	for _, row := range []struct {
		name, token, why string
		record           bool
	}{
		{"the recorded token", "", "", true},
		{"a credential the hub refuses for its own reason", "not-a-listed-token", "(b)", true},
		{"the recorded account and audience, another token", otherProbeToken, "(b)", true},
		{"a hold whose journal holds no admitted-probe record", "", "(a)", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			h.seedProtectedStreams()
			h.awaitStreamLeaders()
			ctx := context.Background()
			id := h.s.Prepare(t, holdrecord.WorkflowRollback)
			var record map[string]string
			if row.record {
				record = h.stackProbeRecord(time.Time{})
			}
			_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, record)
			require.NoError(t, err)
			_, err = h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseHeld, id, "a rollback hold")
			require.NoError(t, err)
			require.NoError(t, h.s.Records.AppendDone(ctx, id, holdrecord.CommandSet, nil))
			if row.token != "" {
				h.probeWith(row.token)
			}
			restarted, err := RestartProveAndBarrier(ctx, h.s.Records, h.op, id)
			if row.why == "" {
				require.NoError(t, err, "RestartProveAndBarrier with the recorded token")
				require.Len(t, restarted, len(h.s.Cluster.Servers))
				return
			}
			var unknown *Unknown
			require.ErrorAs(t, err, &unknown, "RestartProveAndBarrier with %s", row.name)
			require.ErrorContains(t, err, row.why)
			require.Nil(t, restarted)
		})
	}
}

// Operator.Probe reads the probe's credential file at each dial and counts a refusal as the hold's
// only for the token this hold's signed journal recorded, within its life: the same op, the record
// read once, answers the refusal for the recorded token, UNKNOWN naming (b) after the file holds
// another token (a credential rotated mid-hold), and UNKNOWN naming (a) with no record. An admitted
// dial is answered as it is. Each dial presents the token it read. Control: a go test -overlay copy
// of hold.go whose Probe skips the check answers the refusal for every token.
func TestTheProbeCountsARefusalOnlyForTheRecordedTokenAtEachDial(t *testing.T) {
	recorded := holdtest.PlatformToken(holdtest.ProbeAccount, []string{"envoy-nats"}, time.Now().Add(time.Hour))
	credential, err := ProbeCredentialOf(recorded)
	require.NoError(t, err)
	journal := holdrecord.Done{Ops: []holdrecord.Operation{
		{Command: holdrecord.CommandSet, State: holdrecord.StateIntent, Inputs: probeRecordInputs(credential)},
	}}
	current := recorded
	admit := false
	var presented []string
	op := &Operator{
		ProbeToken: func() (string, error) { return current, nil },
		ProbeDial: func(_ context.Context, _, token string) error {
			presented = append(presented, token)
			if admit {
				return nil
			}
			return fmt.Errorf("nats: %w", nats.ErrAuthorization)
		},
	}
	ctx := context.Background()
	var unknown *Unknown

	err = op.Probe(ctx, "nats://hub")
	require.ErrorAs(t, err, &unknown, "a refusal with no record read")
	require.ErrorContains(t, err, "(a)")

	require.NoError(t, op.useProbeRecord(journal))
	require.ErrorIs(t, op.Probe(ctx, "nats://hub"), nats.ErrAuthorization, "the recorded token's refusal")

	current = otherProbeToken
	err = op.Probe(ctx, "nats://hub")
	require.False(t, errors.Is(err, nats.ErrAuthorization), "a refusal of another token was answered as the hold's: %v", err)
	require.ErrorAs(t, err, &unknown)
	require.ErrorContains(t, err, "(b)")

	admit = true
	require.NoError(t, op.Probe(ctx, "nats://hub"), "an admitted dial")
	require.Equal(t, []string{recorded, recorded, otherProbeToken, otherProbeToken}, presented, "a dial presented a token other than the one it read")
}
