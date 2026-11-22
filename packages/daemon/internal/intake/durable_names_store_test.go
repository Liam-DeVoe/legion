package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/consumernames"
)

// The fresh names' store and sentinel rows: what the daemon's selection store must hold before a
// fresh daemon calls the server at all, and which project's sentinel each daemon reads.

// On fresh names the daemon refuses every consumer call until the operator's convert has written
// the cutover marker "done" into its database: with the table missing, the sentinel missing, the
// cutover not run and after a rollback, the boot stops with the ERROR line, before any consumer is
// created or read, and never falls back to the fixed names.
func TestFreshNamesRefuseUntilTheCutoverIsDone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker string
		write  bool
		drop   bool
		want   error
	}{
		{name: "no selection table", drop: true, want: consumernames.ErrNoStore},
		{name: "no sentinel", want: consumernames.ErrNoSentinel},
		{name: "the cutover not run", write: true, want: consumernames.ErrCutoverNotRun},
		{name: "after a rollback", write: true, marker: consumernames.CutoverRolledBack, want: consumernames.ErrCutoverNotRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			switch {
			case tc.drop:
				if _, err := pool.Exec(context.Background(), `drop table intake_consumers`); err != nil {
					t.Fatalf("drop the selection table: %v", err)
				}
			case tc.write:
				cutover(t, pool, tc.marker)
			}
			js, stream, _ := freshJetStream(t)
			calls := watchConsumerCalls(t, js.Conn())
			logs := &lockedBuffer{}
			spec := freshSpec(t, logs, js)

			consumers, err := OpenConsumers(context.Background(), js, pool, spec)
			if !errors.Is(err, tc.want) {
				t.Fatalf("OpenConsumers = %v, %v; want the refusal %v", consumers, err, tc.want)
			}
			if touched := calls.touched(t); len(touched) != 0 {
				t.Fatalf("the refused boot sent the server %v, want no consumer call", touched)
			}
			if names := consumerNames(t, stream); len(names) != 0 {
				t.Fatalf("the stream holds %v after the refused boot, want no consumer", names)
			}
			line := oneErrorLine(t, logs, dispatchConsumerName(spec.Project), consumernames.OperationSelect)
			if line["physical"] != "" {
				t.Fatalf("the ERROR line names the physical name %v, want none: no record was read", line["physical"])
			}
			if !strings.Contains(fmt.Sprint(line["error"]), tc.want.Error()) {
				t.Fatalf("the ERROR line's error = %v, want it to hold %q", line["error"], tc.want)
			}
		})
	}
}

// On fresh names the daemon tags each last floor with the stream identity the operator recorded
// in the sentinel (convert and enroll write it), since a held daemon may not read the stream's info.
// A sentinel that records none is a store neither wrote: the boot refuses it with the ERROR line,
// naming stream_id, before any consumer call. The control is the rig's sentinel, which records
// one, and opens.
func TestFreshNamesRefuseASentinelWithNoStreamIdentity(t *testing.T) {
	pool := migratedPool(t)
	if _, err := storeOf(t, rigFamily(), pool).WriteSentinel(context.Background(), consumernames.StoreState{Family: rigFamily(), Cutover: consumernames.CutoverDone}, 0); err != nil {
		t.Fatalf("write a sentinel with no stream identity: %v", err)
	}
	js, stream, _ := freshJetStream(t)
	calls := watchConsumerCalls(t, js.Conn())
	logs := &lockedBuffer{}
	spec := freshSpec(t, logs, js)
	consumers, err := OpenConsumers(context.Background(), js, pool, spec)
	if err == nil || !strings.Contains(err.Error(), "stream_id") {
		t.Fatalf("OpenConsumers on a sentinel with no stream identity = %v, %v; want a refusal naming stream_id", consumers, err)
	}
	if touched := calls.touched(t); len(touched) != 0 {
		t.Fatalf("the refused boot sent the server %v, want no consumer call", touched)
	}
	if names := consumerNames(t, stream); len(names) != 0 {
		t.Fatalf("the stream holds %v after the refused boot, want no consumer", names)
	}
	oneErrorLine(t, logs, dispatchConsumerName(spec.Project), consumernames.OperationSelect)

	control := migratedPool(t)
	cutover(t, control, consumernames.CutoverDone)
	openIntake(t, js, control, freshSpec(t, &lockedBuffer{}, js))
}

// Two projects' daemons share one database, as legion deploys them, and each reads only its own
// family's sentinel (daemon/<project>): P's cutover marks P's sentinel done, P's fresh daemon opens
// its fresh names, and Q's fixed daemon, whose own sentinel is not there, opens Q's fixed names and
// is not stopped. The control is one sentinel row for both, as the table held before: P's done in
// the row Q reads stops Q's fixed daemon.
func TestTwoProjectsOnOneDatabaseReadOnlyTheirOwnSentinel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shared bool
	}{
		{name: "each project's own sentinel"},
		{name: "control: one sentinel row for both", shared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			js, stream, _ := freshJetStream(t)
			p := freshSpec(t, &lockedBuffer{}, js)
			q := consumerSpec(&lockedBuffer{})
			q.Project = "OTHER"
			q.Names = consumernames.ModeFixed
			cutover(t, pool, consumernames.CutoverDone)
			if tc.shared {
				cutoverOf(t, pool, familyOf(t, q.Project), consumernames.CutoverDone)
			}

			openIntake(t, js, pool, p)
			_, err := OpenConsumers(context.Background(), js, pool, q)
			if tc.shared {
				if !errors.Is(err, errStoreConverted) {
					t.Fatalf("Q's fixed daemon over P's done in the row it reads = %v, want the converted store's refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Q's fixed daemon beside P's cutover = %v, want its fixed names opened", err)
			}
			for _, name := range fixedNames(q.Project) {
				if heldInfo(t, stream, name) == nil {
					t.Fatalf("Q's fixed name %s is not on the stream", name)
				}
			}
			for _, logical := range fixedNames(p.Project) {
				if !isFresh(logical, currentName(t, pool, logical)) {
					t.Fatalf("P's %s selects no fresh name", logical)
				}
			}
			if _, err := storeOf(t, familyOf(t, q.Project), pool).State(context.Background()); !errors.Is(err, consumernames.ErrNoSentinel) {
				t.Fatalf("Q's store reads a sentinel (%v), want none: P's cutover wrote only P's", err)
			}
		})
	}
}

// A daemon's store reads only a sentinel that names its own family: a sentinel row under the
// daemon's key that names another family (written raw, since the store's own write refuses it) is a
// malformed sentinel (ErrMalformedSentinel), never a missing one, and the fresh boot refuses with
// the ERROR line before any consumer call. The control is the same raw row naming the daemon's own
// family, on which the boot opens.
func TestADaemonStoreRefusesASentinelOfAnotherFamily(t *testing.T) {
	for _, tc := range []struct {
		name    string
		family  string
		refused bool
	}{
		{name: "another project's family", family: familyOf(t, "OTHER"), refused: true},
		{name: "Dispatch's family", family: consumernames.DispatchFamily, refused: true},
		{name: "control: its own family", family: rigFamily()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			plantSentinel(t, pool, consumernames.StoreState{Family: tc.family, Cutover: consumernames.CutoverDone, StreamID: sentinelStreamID})
			js, stream, _ := freshJetStream(t)
			logs := &lockedBuffer{}
			spec := freshSpec(t, logs, js)
			_, err := OpenConsumers(context.Background(), js, pool, spec)
			if !tc.refused {
				if err != nil {
					t.Fatalf("OpenConsumers over its own family's sentinel = %v, want it opened", err)
				}
				return
			}
			if !errors.Is(err, consumernames.ErrMalformedSentinel) {
				t.Fatalf("OpenConsumers over a sentinel naming %s = %v, want the malformed sentinel's refusal", tc.family, err)
			}
			if names := consumerNames(t, stream); len(names) != 0 {
				t.Fatalf("the stream holds %v after the refused boot, want no consumer", names)
			}
			oneErrorLine(t, logs, dispatchConsumerName(spec.Project), consumernames.OperationSelect)
		})
	}
}
