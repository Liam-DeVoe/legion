package writerhold

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// Operator is a hold command's reach of the clustered hub: its Kubernetes objects, the disks its
// claims are bound to, the operator's NATS connection and the probe's.
type Operator struct {
	Hub   KubeHub
	Disks Disks
	// Connect dials the hub as the operator, on a connection made with
	// nats.CustomInboxPrefix(natsgrant.OperatorInbox). The auth service decides it under the hold
	// row's phase at that moment, so a command that moves the row connects again.
	Connect func(ctx context.Context) (*nats.Conn, error)
	// Probe dials url with the hold probe's credential (Restart.Probe's contract): nil when the
	// server admitted it, an error wrapping nats.ErrAuthorization when it refused it.
	Probe func(ctx context.Context, url string) error
	// URLs are the hub's client URLs, which the probe dials as the hub.
	URLs []string
	// Wait waits d, ending early with ctx's error; a sleep when nil.
	Wait func(ctx context.Context, d time.Duration) error
	// RestartLimit is each pod's restart limit, PodRestartLimit when zero.
	RestartLimit time.Duration
	// ExpectedAbsent are the protected streams the hold's workflow expects absent at step 1 (a
	// buckets create's target, the cutover's selection bucket); `hold set` takes those its
	// workflow names (expectedAbsent).
	ExpectedAbsent []string
}

func (op *Operator) wait(ctx context.Context, d time.Duration) error {
	if op.Wait != nil {
		return op.Wait(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// expectedAbsent are the protected streams a workflow's preflight records as expected absence when
// the server answers `stream not found`: the selection bucket for the cutover's first create-bucket
// and for selection-recovery, and every protected stream for streams-update, which creates each
// one absent. A buckets workflow's target is the buckets command's, which it passes itself.
func expectedAbsent(w holdrecord.Workflow, extra []string) []string {
	out := append([]string{}, extra...)
	switch w {
	case holdrecord.WorkflowCutover, holdrecord.WorkflowSelectionRecovery:
		out = append(out, kvStreamPrefix+bus.SessionConsumersBucket)
	case holdrecord.WorkflowStreamsUpdate:
		out = append(out, ProtectedStreams()...)
	}
	return out
}

// startHold moves a released row to prepare under a new hold id and inserts the hold's journal
// (its done record) for workflow, with the step-1 map: for hub-restore, each claim's disk id and
// volume, read before the row turns held, so every later comparison with a target's step-1 disk
// reads this record and never a fresh read. A claim it cannot read is UNKNOWN, with the row still
// released. A second hold is refused: a prepare is accepted only from released.
func startHold(ctx context.Context, records *holdrecord.Client, op *Operator, w holdrecord.Workflow, reason string) (string, error) {
	row, err := records.Row(ctx)
	if err != nil {
		return "", err
	}
	if row.Phase != natsgrant.OperatorPhaseReleased {
		return "", refusef(StepPreflight, "the hold row is %s under hold %s (gen %d): a second hold is refused, since a hold is prepared only from released", row.Phase, row.HoldID, row.Gen)
	}
	var step1 map[string]string
	if w == holdrecord.WorkflowHubRestore {
		if step1, err = readStep1(ctx, op); err != nil {
			return "", err
		}
	}
	id := newHoldID()
	if _, err := records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhasePrepare, id, reason); err != nil {
		return "", err
	}
	if err := records.StartJournal(ctx, id, w, step1); err != nil {
		return id, fmt.Errorf("hold %s is prepared, and its journal was not written (hold abort --hold %s releases it): %w", id, id, err)
	}
	return id, nil
}

// readStep1 reads every claim's disk for the step-1 map: step1_disk_id.<claim> and
// step1_volume.<claim>.
func readStep1(ctx context.Context, op *Operator) (map[string]string, error) {
	_, claims, err := readDisks(ctx, op)
	if err != nil {
		return nil, err
	}
	step1 := map[string]string{}
	for _, b := range claims {
		step1["step1_disk_id."+b.Claim] = b.DiskID
		step1["step1_volume."+b.Claim] = b.Volume
	}
	return step1, nil
}

// prepare is `hold prepare`: the minter report checked, then the row moved from released to
// prepare with a new hold id and the hold's journal inserted for its workflow. Clients are still
// admitted. It prints each allowance the minter run was given, then the hold id.
func prepare(ctx context.Context, records *holdrecord.Client, op *Operator, o options, now func() time.Time, stdout io.Writer) error {
	allowances, err := PreflightMinters(ctx, op.Hub.Client, o.MintersReport, now)
	if err != nil {
		return err
	}
	printAllowances(stdout, allowances)
	id, err := startHold(ctx, records, op, holdrecord.Workflow(o.workflow), o.reason)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	return nil
}

// abort is `hold abort`. From prepare, and only while the hold's journal holds no `hold set` done
// entry (a row that reads prepare after it was put back by a database writer, which abort refuses),
// it deletes the hold's cohort record, then moves the row to released under its hold id; the
// thresholds a prepare raised stay. A prepared hold with no journal at all, which startHold leaves
// when its journal insert after the row's move did not land, takes the same path: no journal holds
// no `hold set` entry. A held row is aborted through the hold's table
// (holdrecord.Admit): in every workflow once a command has stopped and no intent is pending, the way
// out of a command that cannot succeed, and refused while a command's intent is pending or before
// any command has stopped ("no command has stopped"). Its intent carries each stop's record of what
// its command left, which it prints, so the operator ends the hold knowing the state. In hub-restore
// it also needs every claim on its expected disk (abortDisks: the newest done prove's, else step
// 1's), and names a proven disk that is gone: the disks read again, the row moved to released, the
// disks read once more (a restore that lands between the last check and the move is a restore
// outside any hold, which it names), then its done entry. On a row already released under this
// hold id it sends no move: while the journal shows prepare (a row lost in prepare and restored
// released) it is admitted, deletes the cohort record and writes its done entry, which ends the
// hold (abortReleasedPrepare); with no journal at all (a row lost in prepare before its journal was
// written) it deletes the cohort record alone and writes no entry, which the table admits with no
// journal (holdrecord's ReleasedNoJournalAbort); otherwise it writes the done entry an abort killed
// after its move left unwritten.
func abort(ctx context.Context, records *holdrecord.Client, op *Operator, stores StreamIDWriter, o options, stdout io.Writer) error {
	row, err := records.Row(ctx)
	if err != nil {
		return err
	}
	if row.HoldID != o.holdID {
		return refusef(StepPreflight, "the hold row is %s under hold %s, not %s", row.Phase, row.HoldID, o.holdID)
	}
	if row.Phase == natsgrant.OperatorPhasePrepare || row.Phase == natsgrant.OperatorPhaseReleased {
		journaled, err := hasJournal(ctx, records, o.holdID)
		if err != nil {
			return err
		}
		switch {
		case journaled:
		case row.Phase == natsgrant.OperatorPhasePrepare:
			return abortPrepared(ctx, records, row, o, stdout)
		default:
			return abortReleasedNoJournal(ctx, records, row, o, stdout)
		}
	}
	d, _, err := records.Journal(ctx, o.holdID)
	if err != nil {
		return err
	}
	if row.Phase == natsgrant.OperatorPhaseReleased {
		// A row restored released with the hold's id while its journal shows prepare (no done hold
		// set, and not ended): a row lost in prepare, or after an abort from prepare. The table
		// admits abort on that row alone (holdrecord's Request.RowReleased, which Admit sets from the
		// row); it deletes the cohort record, which the route deletes on that row too, and its done
		// entry ends the journal. Its rerun after a crash is admitted on its own pending intent.
		if !holdEnded(d) && !latestIs(d, holdrecord.CommandSet, holdrecord.StateDone) {
			return abortReleasedPrepare(ctx, records, row, o, stdout)
		}
		// An abort killed between its move and its done entry left its intent pending: the rerun
		// sends no move and writes the done entry, which completes the record and ends the hold.
		if latestIs(d, holdrecord.CommandAbort, holdrecord.StateIntent) {
			if err := records.AppendDone(ctx, o.holdID, holdrecord.CommandAbort, nil); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "hold %s was aborted (row gen %d) and its abort's done entry is now written\n", o.holdID, row.Gen)
			return nil
		}
		fmt.Fprintf(stdout, "hold %s is already released (row gen %d)\n", o.holdID, row.Gen)
		return nil
	}
	if row.Phase == natsgrant.OperatorPhasePrepare {
		// A row that reads prepare after hold set's done entry was rolled back by a database
		// writer, not prepared: the journal, not the row, says how far the hold got.
		if latestIs(d, holdrecord.CommandSet, holdrecord.StateDone) {
			return refusef(StepPreflight, "the hold row reads prepare at gen %d, and %s is done in hold %s's journal: the row was put back under the set hold, so abort takes no prepare path", row.Gen, holdrecord.CommandSet, o.holdID)
		}
		return abortPrepared(ctx, records, row, o, stdout)
	}
	if _, err := records.Admit(ctx, o.holdID, holdrecord.Request{Command: holdrecord.CommandAbort}, nil); err != nil {
		return err
	}
	after, _, err := records.Journal(ctx, o.holdID)
	if err != nil {
		return err
	}
	printStopRecords(stdout, after)
	stop := func(cause error) error {
		if err := records.AppendStop(ctx, o.holdID, holdrecord.CommandAbort, cause.Error()); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	hub := d.Workflow == holdrecord.WorkflowHubRestore
	var outputs map[string]string
	if hub {
		if err := abortDisks(ctx, op, d); err != nil {
			return stop(err)
		}
		if _, proved := holdrecord.Proven(d); proved {
			if outputs, err = abortReconcile(ctx, records, op, stores, o, stdout); err != nil {
				return stop(err)
			}
		}
	}
	moved, err := records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhaseReleased, o.holdID, o.reason)
	if err != nil {
		return err
	}
	var changed error
	if hub {
		changed = abortDisks(ctx, op, d)
	}
	if err := records.AppendDone(ctx, o.holdID, holdrecord.CommandAbort, outputs); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "aborted hold %s in %s: row gen %d, released\n", o.holdID, d.Workflow, moved.Gen)
	if changed != nil {
		return fmt.Errorf("the hold is released, and a disk changed after abort's last check, outside any hold: %w", changed)
	}
	return nil
}

// abortReleasedPrepare ends hold o.holdID, whose row reads released with its hold id and whose
// journal shows prepare: its admission, the cohort record deleted, then its done entry.
func abortReleasedPrepare(ctx context.Context, records *holdrecord.Client, row holdrecord.Row, o options, stdout io.Writer) error {
	if _, err := records.Admit(ctx, o.holdID, holdrecord.Request{Command: holdrecord.CommandAbort}, nil); err != nil {
		return err
	}
	if err := records.DeleteCohort(ctx, o.holdID); err != nil {
		return err
	}
	if err := records.AppendDone(ctx, o.holdID, holdrecord.CommandAbort, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "aborted hold %s, whose row reads released (gen %d) and whose journal shows prepare: its cohort record is deleted and the hold has ended\n", o.holdID, row.Gen)
	return nil
}

// abortReleasedNoJournal ends hold o.holdID, whose row reads released with its hold id and which
// has no journal (a row lost in prepare before its journal was written, and restored released): the
// cohort record deleted alone, which the cohort-delete route admits on that row with no journal,
// and no entry, since there is no journal to write it in. A rerun deletes nothing and answers nil.
func abortReleasedNoJournal(ctx context.Context, records *holdrecord.Client, row holdrecord.Row, o options, stdout io.Writer) error {
	if err := records.DeleteCohort(ctx, o.holdID); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "aborted hold %s, whose row reads released (gen %d) and which has no journal: its cohort record is deleted\n", o.holdID, row.Gen)
	return nil
}

// abortPrepared is abort's prepare path on row, prepared under o's hold id: the hold's cohort record
// deleted, then the row moved to released under its hold id.
func abortPrepared(ctx context.Context, records *holdrecord.Client, row holdrecord.Row, o options, stdout io.Writer) error {
	if err := records.DeleteCohort(ctx, o.holdID); err != nil {
		return err
	}
	moved, err := records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhaseReleased, o.holdID, o.reason)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "aborted hold %s from prepare: row gen %d, released\n", o.holdID, moved.Gen)
	return nil
}

// abortReconcile is hub-restore's abort's reconcile of the daemon stores after a done prove: the
// restore put ENVOY_NOTIFICATIONS back, so each daemon store's recorded stream identity is checked
// before the release admits the daemons. Its families are the cutover rows' daemon/<P> families;
// each store is read at the address its daemon's Deployment names (KubeDaemonDeployments, the
// hold's own Kubernetes client), with the operator's own --daemon-database entries, by the writer
// cmd/envoy-infra passes in (stores). The abort goes on only when the writer's result places every
// daemon family in exactly one list (StreamIDResult.Covers) and leaves none unreconciled; any other
// answer, a writer missing among them, is UNKNOWN or a refusal naming each store, and the abort
// writes its stop, whose reason names each store found gone with the server's answer and its
// address. Otherwise it returns the abort's done outputs: stream_id.daemon/<P> for each store it
// rewrote and gone.daemon/<P> for each found gone, as the census records them. A rerun of a crashed
// abort runs it again from the start, trusting nothing the crashed run wrote.
func abortReconcile(ctx context.Context, records *holdrecord.Client, op *Operator, stores StreamIDWriter, o options, stdout io.Writer) (map[string]string, error) {
	cutovers, err := records.Cutovers(ctx)
	if err != nil {
		return nil, err
	}
	families := daemonFamilies(cutovers)
	if len(families) == 0 {
		return nil, nil
	}
	nc, err := op.Connect(ctx)
	if err != nil {
		return nil, unknownf(StepPreflight, err, "connect to reconcile the daemon stores")
	}
	defer nc.Close()
	return reconcileStreamIDs(ctx, nc, stores, KubeDaemonDeployments(op.Hub.Client), families, o.daemonDatabases, stdout)
}

// printStopRecords prints what each stopped command left, as abort's latest intent carries it, in
// the journal's order of the stops (holdrecord's Entry.Left).
func printStopRecords(stdout io.Writer, d holdrecord.Done) {
	intent, ok := d.Latest(holdrecord.CommandAbort, holdrecord.StateIntent)
	if !ok {
		return
	}
	items, _ := intent.Left()
	for _, item := range items {
		left := item.Left
		if left == "" {
			left = "nothing recorded, so counted as changed"
		}
		fmt.Fprintf(stdout, "left by a stopped command: %s: %s\n", item.Command, left)
	}
}

// latestIs reports whether command's latest entry in d is in state (holdrecord's Done.Latest).
func latestIs(d holdrecord.Done, command, state string) bool {
	e, ok := d.Latest(command)
	return ok && e.State == state
}

// holdEnded reports whether the hold has ended: a done `hold release` or `hold abort`
// (holdrecord's EndIsFinal), after which the table admits no command in it.
func holdEnded(d holdrecord.Done) bool {
	_, released := d.Latest(holdrecord.CommandRelease, holdrecord.StateDone)
	_, aborted := d.Latest(holdrecord.CommandAbort, holdrecord.StateDone)
	return released || aborted
}

// hasJournal reports whether hold holdID has a journal: false when the broker lists no record of
// the journal's kind, "done", for it, which startHold leaves when its journal insert after the
// row's move did not land. A journal the broker has seen and that is gone outside its routes reads
// below its floor, holdrecord.ErrUnknown, never as none. The kind's literal goes when holdrecord's
// Journal answers a hold with no journal with an error of its own.
func hasJournal(ctx context.Context, records *holdrecord.Client, holdID string) (bool, error) {
	found, err := records.Records(ctx, holdID, "done")
	if err != nil {
		return false, err
	}
	return len(found) > 0, nil
}

// readDisks is every claim's disk id now, by target (claim name), with the claims' bindings.
func readDisks(ctx context.Context, op *Operator) (map[string]string, map[string]ClaimBinding, error) {
	r, err := Precondition(ctx, op.Hub)
	if err != nil {
		return nil, nil, err
	}
	claims, err := ReadClaims(ctx, op.Hub, r, op.Disks)
	if err != nil {
		return nil, nil, err
	}
	read := map[string]string{}
	byClaim := map[string]ClaimBinding{}
	for _, b := range claims {
		read[b.Claim] = b.DiskID
		byClaim[b.Claim] = b
	}
	return read, byClaim, nil
}

// abortDisks is hub-restore's abort check: every target on its expected disk (holdrecord.Expected:
// the newest done prove's for each target it proved, step 1's for every other). A proven target on
// another disk is read by the proven disk's name (in its current disk's project and location, where
// a restore puts it): when that name answers 404, or another numeric id than the prove recorded, the
// proven disk is gone, and the refusal names it, so the stop it writes records the gone disk and the
// operator restores the target onto another disk and proves it again; when the proven disk is still
// there, the refusal says to restore the target back onto it. Any other answer is UNKNOWN.
func abortDisks(ctx context.Context, op *Operator, d holdrecord.Done) error {
	want, ok := holdrecord.Expected(d)
	if !ok {
		return unknownf(StepPreflight, nil, "the hold's journal holds no step-1 map")
	}
	read, bindings, err := readDisks(ctx, op)
	if err != nil {
		return err
	}
	proof, proved := holdrecord.Proven(d)
	var gone []string
	for _, t := range sortedKeys(want) {
		name, recorded := proof.Disks[t]
		if !proved || !recorded || read[t] == want[t] {
			continue
		}
		ref := bindings[t].Disk
		ref.Name = name
		id, err := op.Disks.DiskID(ctx, ref)
		switch {
		case errors.Is(err, ErrDiskNotFound):
			gone = append(gone, fmt.Sprintf("the done prove's disk of %s is gone: %s answers 404", t, ref))
		case err != nil:
			return unknownf(StepPreflight, err, "read the done prove's disk %s of %s", ref, t)
		case strconv.FormatUint(id, 10) != want[t]:
			gone = append(gone, fmt.Sprintf("the done prove's disk of %s is gone: %s now has id %d, not the proven %s", t, ref, id, want[t]))
		default:
			return refusef(StepPreflight, "%s is on disk %s, not the done prove's %s (id %s), which is still there: restore %s back onto it", t, read[t], ref, want[t], t)
		}
	}
	if len(gone) > 0 {
		return refusef(StepPreflight, "%s; restore each onto another disk and prove it, then abort again", strings.Join(gone, "; "))
	}
	return sameDisks(StepPreflight, "the disk the newest done prove, or step 1, records", want, read)
}

// sameDisks refuses, in step, a target whose disk id read differs from want, and is UNKNOWN when
// the two do not name the same targets.
func sameDisks(step, what string, want, read map[string]string) error {
	if len(want) != len(read) {
		return unknownf(step, nil, "the targets read (%s) are not the journal's (%s)", strings.Join(sortedKeys(read), ", "), strings.Join(sortedKeys(want), ", "))
	}
	var moved []string
	for _, t := range sortedKeys(want) {
		got, found := read[t]
		if !found {
			return unknownf(step, nil, "target %s is in the journal and was not read", t)
		}
		if got != want[t] {
			moved = append(moved, fmt.Sprintf("%s is on disk %s, not %s (%s)", t, got, want[t], what))
		}
	}
	if len(moved) > 0 {
		return refusef(step, "%s", strings.Join(moved, "; "))
	}
	return nil
}

// release is `hold release`. Its admission (holdrecord.Admit) is its decision: the record holds no
// entry but hold set's and its own, or it is complete for its workflow, or every stop in it left
// nothing changed, else ErrOutOfOrder. In hub-restore, before its PUT, every claim must be on the
// disk the latest done prove records for it, and every other target on its step-1 disk
// (holdrecord.Expected); a refusal of that check writes a stop entry, which keeps the completion.
// Its PUT moves the row to released, and its done entry after it ends the hold: from then the
// table admits no command in it (holdrecord's EndIsFinal). In hub-restore it reads the disks once
// more after the move, as abort does: a restore that lands between its check and its move is a
// restore outside any hold (the model's assumption A1), which it names, exiting 1 with the hold
// released. Run again on a row already released under its hold id, it sends no PUT, and writes the
// done entry a release killed between its PUT and its done left unwritten.
func release(ctx context.Context, records *holdrecord.Client, op *Operator, o options, stdout io.Writer) error {
	row, err := records.Row(ctx)
	if err != nil {
		return err
	}
	if row.HoldID != o.holdID {
		return refusef(StepPreflight, "the hold row is %s under hold %s, not %s", row.Phase, row.HoldID, o.holdID)
	}
	if row.Phase == natsgrant.OperatorPhaseReleased {
		d, _, err := records.Journal(ctx, o.holdID)
		if err != nil {
			return err
		}
		if latestIs(d, holdrecord.CommandRelease, holdrecord.StateIntent) {
			if err := records.AppendDone(ctx, o.holdID, holdrecord.CommandRelease, nil); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "hold %s was released (row gen %d) and its release's done entry is now written\n", o.holdID, row.Gen)
		}
		fmt.Fprintf(stdout, "hold %s is already released (row gen %d); its record's hold_gen is %d\n", o.holdID, row.Gen, d.HoldGen)
		return nil
	}
	if row.Phase != natsgrant.OperatorPhaseHeld {
		return refusef(StepPreflight, "hold %s is %s, not held: a prepared hold is ended by hold abort", o.holdID, row.Phase)
	}
	w, err := records.Admit(ctx, o.holdID, holdrecord.Request{Command: holdrecord.CommandRelease}, nil)
	if err != nil {
		return err
	}
	d, _, err := records.Journal(ctx, o.holdID)
	if err != nil {
		return err
	}
	if w == holdrecord.WorkflowHubRestore {
		if err := onExpectedDisks(ctx, op, d); err != nil {
			if stopErr := records.AppendStop(ctx, o.holdID, holdrecord.CommandRelease, err.Error()); stopErr != nil {
				return errors.Join(err, stopErr)
			}
			return err
		}
	}
	moved, err := records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhaseReleased, o.holdID, o.reason)
	if err != nil {
		return err
	}
	var changed error
	if w == holdrecord.WorkflowHubRestore {
		changed = onExpectedDisks(ctx, op, d)
	}
	if err := records.AppendDone(ctx, o.holdID, holdrecord.CommandRelease, nil); err != nil {
		return fmt.Errorf("hold %s is released (row gen %d), and its done entry was not written: run hold release --hold %s again: %w", o.holdID, moved.Gen, o.holdID, err)
	}
	fmt.Fprintf(stdout, "released hold %s: row gen %d; the record's hold_gen is %d\n", o.holdID, moved.Gen, d.HoldGen)
	if changed != nil {
		return fmt.Errorf("the hold is released, and a disk changed after release's last check, outside any hold: %w", changed)
	}
	return nil
}

// onExpectedDisks refuses unless every target is on its expected disk (holdrecord.Expected).
func onExpectedDisks(ctx context.Context, op *Operator, d holdrecord.Done) error {
	want, ok := holdrecord.Expected(d)
	if !ok {
		return unknownf(StepPreflight, nil, "the hold's journal holds no step-1 map")
	}
	read, _, err := readDisks(ctx, op)
	if err != nil {
		return err
	}
	return sameDisks(StepPreflight, "the disk the latest prove, or step 1, records", want, read)
}

// set is `hold set`: with --workflow, a hold with no prepare step, it first prepares one
// (startHold) and runs the preflight at once; with --hold, it sets the prepared hold. Its steps,
// each a reading of the hub itself (§3a): the preflight under the prepare grant, with the probe's
// first run, which must be admitted, its intent with the row still prepare, the row moved to held,
// then, under the held grant, the probe's refusal, the writer census, the restart of every declared
// server with its proof, the meta barrier and the census inside it, and the report; then its done
// entry. A refusal after its intent writes a stop entry, so `hold set`'s rerun, `hold abort` in
// hub-restore, or `hold release` with nothing changed is admitted. A rerun that finds the row held
// under its hold id is admitted again (Set), writes a new intent and runs the held steps again.
func set(ctx context.Context, records *holdrecord.Client, op *Operator, o options, now func() time.Time, stdout io.Writer) error {
	allowances, err := PreflightMinters(ctx, op.Hub.Client, o.MintersReport, now)
	if err != nil {
		return err
	}
	printAllowances(stdout, allowances)
	id := o.holdID
	if id == "" {
		if id, err = startHold(ctx, records, op, holdrecord.Workflow(o.workflow), o.reason); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "prepared hold %s\n", id)
	}
	reading, err := Set(ctx, records, op, id, o.reason, restoreInputs(o), allowanceOutputs(allowances))
	if err != nil {
		return err
	}
	printReport(stdout, id, reading)
	return nil
}

// restoreInputs are hub-restore's inputs, which hold set's intent carries: disk.<target> and
// snapshot.<target> for each restored target.
func restoreInputs(o options) map[string]string {
	if len(o.restored) == 0 && len(o.snapshots) == 0 {
		return nil
	}
	in := map[string]string{}
	for _, pair := range o.restored {
		t, v, _ := strings.Cut(pair, "=")
		in["disk."+t] = v
	}
	for _, pair := range o.snapshots {
		t, v, _ := strings.Cut(pair, "=")
		in["snapshot."+t] = v
	}
	return in
}

// Set runs hold set's steps on the hold holdID, prepared already, and returns the report's clean
// reading. inputs are its intent's (hub-restore's restore inputs), and recorded are entries its done
// entry records beside the restart's (the preflight's minter allowances, minters_allowance.<i>).
// `envoy-infra buckets` and the streams words run it for the hold of their one command. Unless its
// own intent is pending (the rerun of a set that crashed), it is admitted through holdrecord.Admit
// before anything changes, whatever the row's phase: the first run with the row prepare, and the
// rerun after its own stop with the row held, so the rerun takes the one-command lock and writes a
// new intent before it restarts any server, and its done entry follows that intent. A set already
// done is refused there.
func Set(ctx context.Context, records *holdrecord.Client, op *Operator, holdID, reason string, inputs, recorded map[string]string) (*ReportReading, error) {
	row, err := records.Row(ctx)
	if err != nil {
		return nil, err
	}
	if row.HoldID != holdID || (row.Phase != natsgrant.OperatorPhasePrepare && row.Phase != natsgrant.OperatorPhaseHeld) {
		return nil, refusef(StepPreflight, "the hold row is %s under hold %s, not prepared or held under %s", row.Phase, row.HoldID, holdID)
	}
	d, _, err := records.Journal(ctx, holdID)
	if err != nil {
		return nil, err
	}
	pending := latestIs(d, holdrecord.CommandSet, holdrecord.StateIntent)
	stop := func(cause error) error {
		if !pending {
			return cause
		}
		if err := records.AppendStop(ctx, holdID, holdrecord.CommandSet, cause.Error()); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}

	// Step 1, under the prepare grant (or the held one on a rerun), mutating nothing.
	nc, err := op.Connect(ctx)
	if err != nil {
		return nil, stop(unknownf(StepPreflight, err, "connect to the hub as the operator"))
	}
	defer func() { nc.Close() }()
	if row.Phase == natsgrant.OperatorPhasePrepare {
		if err := probeAdmitted(ctx, op); err != nil {
			return nil, stop(err)
		}
	}
	render, err := Precondition(ctx, op.Hub)
	if err != nil {
		return nil, stop(err)
	}
	declared, err := Discover(ctx, op.Hub)
	if err != nil {
		return nil, stop(err)
	}
	discovered := time.Now()
	claims, err := ReadClaims(ctx, op.Hub, render, op.Disks)
	if err != nil {
		return nil, stop(err)
	}
	streams, err := ReadProtectedStreams(ctx, nc, expectedAbsent(d.Workflow, op.ExpectedAbsent))
	if err != nil {
		return nil, stop(err)
	}
	present := presentStreams(streams)

	if !pending {
		if _, err := records.Admit(ctx, holdID, holdrecord.Request{Command: holdrecord.CommandSet}, inputs); err != nil {
			return nil, err
		}
		pending = true
	}
	if row.Phase == natsgrant.OperatorPhasePrepare {
		if _, err := records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhaseHeld, holdID, reason); err != nil {
			return nil, stop(err)
		}
	}

	// Step 2: under the held grant, after one authorization timeout, so a decision that started
	// before the row was written has ended.
	nc.Close()
	if nc, err = op.Connect(ctx); err != nil {
		return nil, stop(unknownf(StepProbe, err, "connect to the hub as the operator under the held grant"))
	}
	if left := declared.AuthTimeout() - time.Since(discovered); left > 0 {
		if err := op.wait(ctx, left); err != nil {
			return nil, stop(err)
		}
	}
	if err := probeRefused(ctx, op, strings.Join(op.URLs, ","), "the hub"); err != nil {
		return nil, stop(err)
	}
	if err := op.wholeCensus(ctx, nc, present); err != nil {
		return nil, stop(err)
	}

	// Steps 3 to 5: every declared server restarted and proved, one at a time, then the meta
	// barrier, with the writer census again inside it, since the restart.
	restarted, err := restartProveAndBarrier(ctx, op, nc, render, declared, holdID, claims, func(ctx context.Context) error {
		return op.wholeCensus(ctx, nc, present)
	})
	if err != nil {
		return nil, stop(err)
	}

	// Step 6: the report, against step 1's server ids: every server must have restarted since.
	report := Report{Hub: op.Hub, Declared: declared, AuthUser: render.AuthUser, Wait: op.Wait}
	reading, err := report.Run(ctx, nc)
	if err != nil {
		return nil, stop(err)
	}
	for _, server := range reading.Servers {
		for _, r := range restarted {
			if r.Pod == server.Pod {
				if err := CheckReload(StepReport, r.Pod, r.Varz, server.Varz); err != nil {
					return nil, stop(err)
				}
			}
		}
	}
	outputs := maps.Clone(recorded)
	if outputs == nil {
		outputs = map[string]string{}
	}
	for _, r := range restarted {
		outputs["server_id."+r.Pod] = r.Varz.ServerID
		outputs["exit."+r.Pod] = r.Exit.String()
	}
	if err := records.AppendDone(ctx, holdID, holdrecord.CommandSet, outputs); err != nil {
		return nil, err
	}
	return reading, nil
}

// probeAdmitted is the probe's first run, outside the hold, with the row still prepare: it connects
// to the hub and must be admitted, so the probe's refusal in the hold (step 2 and each restarted
// server's proof) cannot come from its own credential, an expired token or a probe entry the auth
// service does not list (plan §3a step 2). Any error refuses the hold, naming it.
func probeAdmitted(ctx context.Context, op *Operator) error {
	if err := op.Probe(ctx, strings.Join(op.URLs, ",")); err != nil {
		return refusef(StepProbe, "the hold's probe was not admitted outside the hold, so its refusal in the hold would not show the hold's: %v", err)
	}
	return nil
}

// censusReadings bounds how many times the writer census is read while its answer is not whole,
// and censusPause is the pause between two readings.
const (
	censusReadings = 10
	censusPause    = 3 * time.Second
)

// wholeCensus is the writer census (TakeCensus) read until one answer is whole: a server just
// restarted lists a stream whose leader it has not heard yet under `missing`, which is a reading
// that is not whole, never a clean one, so it is read again, censusReadings times at most, after
// which the step is UNKNOWN. A finding stops the hold at once.
func (op *Operator) wholeCensus(ctx context.Context, nc *nats.Conn, present []string) error {
	var err error
	for range censusReadings {
		if _, err = TakeCensus(ctx, nc, present); err == nil {
			return nil
		}
		var unknown *Unknown
		if !errors.As(err, &unknown) {
			return err
		}
		if waitErr := op.wait(ctx, censusPause); waitErr != nil {
			return errors.Join(err, waitErr)
		}
	}
	return err
}

// probeRefused connects the probe to url and requires the refusal: an error wrapping
// nats.ErrAuthorization. An admitted probe stops the hold; any other error is UNKNOWN.
func probeRefused(ctx context.Context, op *Operator, url, what string) error {
	err := op.Probe(ctx, url)
	switch {
	case err == nil:
		return refusef(StepProbe, "%s admitted the hold's probe under a held row: the auth service does not refuse clients during the hold", what)
	case errors.Is(err, nats.ErrAuthorization):
		return nil
	}
	return unknownf(StepProbe, err, "the probe's connect to %s", what)
}

// presentStreams are the names of the step-1 readings of streams that are present.
func presentStreams(readings []StreamReading) []string {
	var out []string
	for _, r := range readings {
		if !r.Absent {
			out = append(out, r.Name)
		}
	}
	return out
}

// printReport prints the report's clean reading: the hold's record of who remained.
func printReport(stdout io.Writer, holdID string, reading *ReportReading) {
	fmt.Fprintf(stdout, "hold %s: only the operator (server %s, cid %d) and the auth service remain, read %d times\n",
		holdID, reading.Caller.ServerID, reading.Caller.CID, reading.Readings)
	for _, s := range reading.Servers {
		fmt.Fprintf(stdout, "  %s %s (%s): %d connections\n", s.Pod, s.Varz.ServerName, s.Varz.ServerID, len(s.Connections))
		for _, c := range s.Connections {
			fmt.Fprintf(stdout, "    cid %d name %q user %q\n", c.CID, c.Name, c.AuthorizedUser)
		}
	}
}

// prove is `hold prove` in hub-restore: each target its --restored names is the restored one, on
// the disk named, and every other target must be on its step-1 disk. It is admitted with Moved's
// answer for its own reading of every target, writes its intent before its first restart, runs
// steps 3 to 6 again on the restored servers, and writes its done entry with disk.<target> and
// disk_id.<target> for each restored target. A restored target still on its step-1 disk is refused
// (a restore that did not happen ends with hold abort), and a refusal after the intent writes a
// stop entry, so a corrected prove is admitted.
func prove(ctx context.Context, records *holdrecord.Client, op *Operator, o options, stdout io.Writer) error {
	d, _, err := records.Journal(ctx, o.holdID)
	if err != nil {
		return err
	}
	restored := map[string]string{}
	for _, pair := range o.restored {
		t, v, _ := strings.Cut(pair, "=")
		restored[t] = v
	}
	read, bindings, err := readDisks(ctx, op)
	if err != nil {
		return err
	}
	moved, err := holdrecord.Moved(d, read)
	if err != nil {
		return err
	}
	if _, err := records.Admit(ctx, o.holdID, holdrecord.Request{Command: holdrecord.CommandProve, DisksMoved: moved}, restoreInputs(o)); err != nil {
		return err
	}
	stop := func(cause error) error {
		if err := records.AppendStop(ctx, o.holdID, holdrecord.CommandProve, cause.Error()); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	step1, _ := holdrecord.Step1(d)
	want := map[string]string{}
	var findings []string
	for _, t := range sortedKeys(read) {
		s1 := step1["step1_disk_id."+t]
		disk, isRestored := restored[t]
		switch {
		case !isRestored:
			want[t] = s1
		case read[t] == s1:
			findings = append(findings, fmt.Sprintf("%s is still on its step-1 disk %s: a restore that did not happen ends with hold abort", t, s1))
		case bindings[t].Disk.Name != disk && bindings[t].Volume != disk:
			findings = append(findings, fmt.Sprintf("%s is bound to disk %s (volume %s), not %s", t, bindings[t].Disk.Name, bindings[t].Volume, disk))
		default:
			want[t] = read[t]
		}
	}
	for t := range restored {
		if _, found := read[t]; !found {
			findings = append(findings, fmt.Sprintf("--restored names %s, which is no claim of the hub", t))
		}
	}
	if len(findings) > 0 {
		return stop(refusef(StepProof, "%s", strings.Join(findings, "; ")))
	}
	if err := sameDisks(StepProof, "the disk this prove expects", want, read); err != nil {
		return stop(err)
	}
	reading, err := proveServers(ctx, op, o.holdID, bindings)
	if err != nil {
		return stop(err)
	}
	outputs := map[string]string{}
	for t := range restored {
		outputs["disk."+t] = bindings[t].Disk.Name
		outputs["disk_id."+t] = read[t]
	}
	if err := records.AppendDone(ctx, o.holdID, holdrecord.CommandProve, outputs); err != nil {
		return err
	}
	printReport(stdout, o.holdID, reading)
	return nil
}

// proveServers runs steps 3 to 6 on the hub's servers, each claim held to bindings, for a prove.
func proveServers(ctx context.Context, op *Operator, holdID string, bindings map[string]ClaimBinding) (*ReportReading, error) {
	nc, err := op.Connect(ctx)
	if err != nil {
		return nil, unknownf(StepRestart, err, "connect to the hub as the operator")
	}
	defer func() { nc.Close() }()
	render, err := Precondition(ctx, op.Hub)
	if err != nil {
		return nil, err
	}
	declared, err := Discover(ctx, op.Hub)
	if err != nil {
		return nil, err
	}
	if _, err := restartProveAndBarrier(ctx, op, nc, render, declared, holdID, bindings, nil); err != nil {
		return nil, err
	}
	return Report{Hub: op.Hub, Declared: declared, AuthUser: render.AuthUser, Wait: op.Wait}.Run(ctx, nc)
}

// RestartProveAndBarrier runs §3a's steps 3 to 5 on the hub inside hold holdID: every declared
// server restarted, one eviction at a time, and proved (a new server id, the probe refused on it,
// its routes, image, revision and claim on the disk it is bound to now), then the meta barrier. It
// returns the restarted servers, each with its new /varz. A command that must leave nothing a
// server accepted before its own step (t-7's `restore --finish`) runs it first.
func RestartProveAndBarrier(ctx context.Context, op *Operator, holdID string) (Declared, error) {
	nc, err := op.Connect(ctx)
	if err != nil {
		return nil, unknownf(StepRestart, err, "connect to the hub as the operator")
	}
	defer func() { nc.Close() }()
	render, err := Precondition(ctx, op.Hub)
	if err != nil {
		return nil, err
	}
	declared, err := Discover(ctx, op.Hub)
	if err != nil {
		return nil, err
	}
	claims, err := ReadClaims(ctx, op.Hub, render, op.Disks)
	if err != nil {
		return nil, err
	}
	restarted, err := restartProveAndBarrier(ctx, op, nc, render, declared, holdID, claims, nil)
	if err != nil {
		return nil, err
	}
	return restartedDeclared(restarted), nil
}

// restartProveAndBarrier is steps 3 to 5 on nc, each claim held to bindings: the restart of every
// declared server with its proof, then the meta barrier, which runs listing inside it (`hold set`'s
// writer census; nil for none). It returns the restarted servers.
func restartProveAndBarrier(ctx context.Context, op *Operator, nc *nats.Conn, render *Render, declared Declared, holdID string, bindings map[string]ClaimBinding, listing func(context.Context) error) ([]Restarted, error) {
	restart := Restart{Hub: op.Hub, Declared: declared, Limit: op.RestartLimit, Probe: op.Probe,
		Bind: func(ctx context.Context, pod string, step1, now Varz) error {
			if err := CheckReload(StepProof, pod, step1, now); err != nil {
				return err
			}
			return render.ProveBinding(ctx, op.Hub, op.Disks, pod, now, bindings[render.Claim(pod)])
		}}
	restarted, err := restart.Run(ctx, nc)
	if err != nil {
		return nil, err
	}
	if err := (MetaBarrier{Hub: op.Hub, Declared: restartedDeclared(restarted), HoldID: holdID}).Run(ctx, nc, listing); err != nil {
		return nil, err
	}
	return restarted, nil
}

// restartedDeclared is the restarted servers as declared ones, each with its new /varz.
func restartedDeclared(restarted []Restarted) Declared {
	after := make(Declared, len(restarted))
	for i, r := range restarted {
		after[i] = DeclaredServer{Pod: r.Pod, Varz: r.Varz}
	}
	return after
}
