package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// The role claim reaper writes the takeover record of a claim before it frees the claim
// (RoleClaimFreeRecord), so the role's next holder is handed the role messages the freed holder
// did not read. These rows read that order, a record that did not store, and a claim the restart
// grace keeps; the listener's own row reads what the record then hands over
// (cmd/listener's TestARoleMessageForwardedToAHolderThatStoppedReachesTheHolderAfterTheReap).

// freeRecordWrite is one write of a reaper's record as a row sees it: the role and holder it names,
// and the holder the role's claim named when it was written.
type freeRecordWrite struct {
	role, holder, standing string
}

// recordingFreeRecord is a RoleClaimFreeRecord that stores each write it is asked for, with the
// holder reg's claim of the role names at that moment.
func recordingFreeRecord(reg *Registry, writes *[]freeRecordWrite) RoleClaimFreeRecord {
	return func(ctx context.Context, role, holder string) error {
		claim, err := reg.RoleClaim(ctx, role)
		if err != nil {
			return err
		}
		*writes = append(*writes, freeRecordWrite{role: role, holder: holder, standing: claim.HolderSessionID})
		return nil
	}
}

func TestTheRoleClaimReaperWritesATakeoverRecordWhileTheClaimStandsAndThenFreesIt(t *testing.T) {
	const (
		role       = "legion-capture-controller"
		holder     = "ses_stopped_controller"
		liveWindow = time.Minute
	)
	reg, raw, start, _ := clockedColdRegistry(t)
	putRenewedRoleClaim(t, raw, role, holder, roleClaimInstance, start.Add(-2*liveWindow))

	var writes []freeRecordWrite
	sweep, err := reg.ReapRoleClaims(context.Background(), func(string) bool { return false }, 0, liveWindow, recordingFreeRecord(reg, &writes))
	if err != nil || sweep != (RoleClaimSweep{Reaped: 1}) {
		t.Fatalf("a pass over a claim whose holder is gone = %+v, %v; want the one claim freed", sweep, err)
	}
	if want := []freeRecordWrite{{role: role, holder: holder, standing: holder}}; !slices.Equal(writes, want) {
		t.Fatalf("the pass wrote the records %+v, want %+v: one record naming the role and the holder it frees, written while the claim still names that holder", writes, want)
	}
	assertRoleHolder(t, reg, role, "")
}

func TestAClaimWhoseTakeoverRecordDidNotStoreStandsUntilAPassStoresOne(t *testing.T) {
	const (
		role       = "legion-capture-controller"
		holder     = "ses_stopped_controller"
		liveWindow = time.Minute
	)
	reg, raw, start, _ := clockedColdRegistry(t)
	putRenewedRoleClaim(t, raw, role, holder, roleClaimInstance, start.Add(-2*liveWindow))
	gone := func(string) bool { return false }

	refused := errors.New("the stream did not store the takeover record")
	sweep, err := reg.ReapRoleClaims(context.Background(), gone, 0, liveWindow, func(context.Context, string, string) error { return refused })
	if err != nil || sweep != (RoleClaimSweep{Skipped: 1}) {
		t.Fatalf("a pass whose takeover record did not store = %+v, %v; want the claim skipped and the pass ended without an error", sweep, err)
	}
	assertRoleHolder(t, reg, role, holder)

	var writes []freeRecordWrite
	sweep, err = reg.ReapRoleClaims(context.Background(), gone, 0, liveWindow, recordingFreeRecord(reg, &writes))
	if err != nil || sweep != (RoleClaimSweep{Reaped: 1}) || len(writes) != 1 {
		t.Fatalf("the next pass, whose record stores = %+v, %v, records %+v; want one record and the claim freed", sweep, err, writes)
	}
	assertRoleHolder(t, reg, role, "")
}

// A claim the restart grace keeps is not freed, so nothing is handed on from it and no record is
// written; the pass once the grace is spent writes one and frees the claim.
func TestARestoredClaimInsideItsRestartGraceGetsNoTakeoverRecord(t *testing.T) {
	const (
		role       = "legion-capture-controller"
		holder     = "ses_held_the_role_before_the_restart"
		liveWindow = time.Minute
		sessionTTL = 2 * time.Minute
	)
	reg, advance := restoredRoleClaimRegistry(t, func(raw natsgo.KeyValue, start time.Time) {
		putRenewedRoleClaim(t, raw, role, holder, roleClaimInstance, start.Add(-2*liveWindow))
	})
	gone := func(string) bool { return false }
	var writes []freeRecordWrite
	record := recordingFreeRecord(reg, &writes)

	advance(sessionTTL - time.Second)
	sweep, err := reg.ReapRoleClaims(context.Background(), gone, sessionTTL, liveWindow, record)
	if err != nil || sweep != (RoleClaimSweep{}) || len(writes) != 0 {
		t.Fatalf("a pass inside the restart grace = %+v, %v, records %+v; want nothing freed and no record", sweep, err, writes)
	}
	advance(sessionTTL)
	sweep, err = reg.ReapRoleClaims(context.Background(), gone, sessionTTL, liveWindow, record)
	if want := []freeRecordWrite{{role: role, holder: holder, standing: holder}}; err != nil || sweep != (RoleClaimSweep{Reaped: 1}) || !slices.Equal(writes, want) {
		t.Fatalf("the pass once the grace is spent = %+v, %v, records %+v; want the claim freed after the record %+v", sweep, err, writes, want)
	}
	assertRoleHolder(t, reg, role, "")
}
