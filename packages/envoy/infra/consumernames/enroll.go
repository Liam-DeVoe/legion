package consumernames

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// enrollCommand is the enroll's command in the hold's journal, the one command of the workflow
// holdrecord.WorkflowEnroll (internal/holdrecord/table.go, tableFor): final and Stoppable.
const enrollCommand = "consumer-names enroll"

// enroll is `consumer-names enroll --daemon <project key> --hold <id>`: it marks a Legion daemon's
// store made after the window, which no cutover converted, as converted, so its daemon in the
// fresh mode serves (selection.Admit) and every census reads its family as it reads the others.
// Dispatch has one store, which the window converts, so the command takes a daemon's store alone.
//
// Under its intent it reads, before it writes anything: the store's marker (intake_consumers'
// sentinel row), the broker's cutover records, every consumer of the notification stream, the
// store's checkpoints (consumer_positions) and its selection rows. It stops, naming what it found,
// when the stream lists a consumer of one of the store's logical names (legion-go-<project>-<kind>)
// in any form, fixed or fresh (a fresh name, retired or not, is <logical>~<nuid>); the store holds
// a checkpoint or a selection row, or lacks either table; the store's marker reads done or
// rolled_back from another hold; or the broker holds a cutover record for the family from another
// hold. So a store whose selection state was lost is never taken for a new one, and fails closed.
// It stops too when an address the project's Deployment names, its store's database on its
// instance, is one another daemon's Deployment names (enrollSharedAddress): a store two daemons
// share is no one daemon's, and marking it would mark it for both.
// Otherwise it writes the store's marker in one conditional write that keeps a done this hold
// wrote: cutover=done naming this hold, the boundary (the stream's last sequence + 1 at the
// enrolment, where a key with no record starts no later: selection.Names' first-create rule) and
// the stream's identity (selection.StreamIdentity), which a daemon that does not read the stream's
// info tags each last floor with, so a last floor counts only on this stream. Then it writes the
// family's broker cutover record done (holdrecord.Client.PutCutover, an upsert), then its done
// entry, which completes the hold's record. A stream that carries no identity refuses the
// enrolment: the daemon could never count its last floor.
//
// Every exit after its intent writes a stop entry that records what every run of enroll in this
// hold left outside the journal (enrollLeft, enrollProgress): holdrecord.LeftUnchanged while no run
// has written this hold's store marker or the family's broker cutover record, which it reads at
// its start, since an earlier run may have crashed after a write; else each of those that exists,
// and a write whose answer was lost, each of which counts as changed, so hold release is refused
// and the way out is enroll's rerun (admitted again, as it has no done entry) or hold abort. A
// rerun finds this hold's marker or cutover record, skips what is written and completes, keeping
// the boundary and identity its marker already records (a marker of this hold that lacks either is
// written again with them). A reading or a write whose outcome it cannot know is UNKNOWN: its stop
// (admission.end) records what the run read and could not read (leftUnknown), never unchanged.
func (r *run) enroll(ctx context.Context) error {
	project := r.o.daemon
	for _, logical := range enrollLogicalNames(project) {
		if err := selection.CheckKey(logical); err != nil {
			return fmt.Errorf("--daemon %q names no daemon whose consumers can be named: %w", project, err)
		}
	}
	a, err := r.admit(ctx, enrollCommand, map[string]string{"daemon": project})
	if err != nil {
		return err
	}
	family := daemonFamily(project)
	var p enrollProgress
	err = r.enrollHeld(ctx, a, project, &p)
	unchanged, account := p.account(family, r.o.holdID)
	return a.end(ctx, err, unchanged, account)
}

// enrollProgress is what a run of enroll knows of this hold's own writes since its intent, which
// the stop of an exit that wrote none records (account).
type enrollProgress struct {
	// marker and broker: this hold's store marker and the family's broker cutover record done
	// under this hold, read or written: what every run of enroll in this hold left (enrollLeft).
	marker, broker bool
	// unsure is a write whose answer was lost, which may or may not have landed.
	unsure string
}

// account is this run's account of what enroll's runs in this hold left, for its stop
// (admission.end): unchanged while neither this hold's store marker nor the family's broker
// cutover record is written and no write's answer was lost, else each of them.
func (p enrollProgress) account(family, holdID string) (bool, string) {
	left := enrollLeft(family, holdID, p.marker, p.broker)
	switch {
	case p.unsure != "" && left == holdrecord.LeftUnchanged:
		return false, p.unsure
	case p.unsure != "":
		return false, left + "; " + p.unsure
	case left == holdrecord.LeftUnchanged:
		return true, "no run of enroll in this hold wrote this hold's store marker or the family's broker cutover record"
	}
	return false, left
}

// enrollHeld is enroll's work after its intent, what it knows of this hold's own writes kept in p.
func (r *run) enrollHeld(ctx context.Context, a *admission, project string, p *enrollProgress) error {
	family := daemonFamily(project)
	logicals := enrollLogicalNames(project)
	db, err := r.database(ctx, family)
	if err != nil {
		return err
	}
	store, err := r.store(ctx, family)
	if err != nil {
		return err
	}
	nc, _, err := r.conn(ctx)
	if err != nil {
		return err
	}

	// The readings, all before any write.
	var findings []string
	noTable := false
	marker, revision, markerFound, err := enrollReadMarker(ctx, store)
	switch {
	case errors.Is(err, selection.ErrNoStore):
		noTable = true
		findings = append(findings, "the store has no "+selection.IntakeConsumersTable+" table, so its daemon has not migrated it")
	case err != nil:
		return unknownf(err, "%s's marker", family)
	}
	r.read(family + "'s marker")
	p.marker = markerFound && marker.Cutover == selection.CutoverDone && marker.Hold == r.o.holdID
	if markerFound && !p.marker && (marker.Cutover == selection.CutoverDone || marker.Cutover == selection.CutoverRolledBack) {
		findings = append(findings, fmt.Sprintf("the store's marker reads %s from %s", marker.Cutover, enrollHoldName(marker.Hold)))
	}
	cutovers, err := r.records.Cutovers(ctx)
	if err != nil {
		return unknownf(err, "the broker's cutover records")
	}
	r.read("the broker's cutover records")
	for _, row := range cutovers {
		if row.Family != family {
			continue
		}
		if row.State == selection.CutoverDone && row.HoldID == r.o.holdID {
			p.broker = true
			continue
		}
		findings = append(findings, fmt.Sprintf("the broker holds the cutover record %s %s from %s", family, row.State, enrollHoldName(row.HoldID)))
	}
	listed, err := consumerNames(ctx, nc, notifications)
	if err != nil {
		return err
	}
	r.read("the consumers of " + notifications)
	for _, name := range listed {
		if slices.Contains(logicals, selection.LogicalOf(name)) {
			findings = append(findings, fmt.Sprintf("%s lists consumer %s", notifications, name))
		}
	}
	checkpoints, err := enrollCheckpoints(ctx, db)
	switch {
	case errors.Is(err, selection.ErrNoStore):
		findings = append(findings, "the store has no consumer_positions table, so its daemon has not migrated it")
	case err != nil:
		return unknownf(err, "%s's checkpoints", family)
	}
	r.read(family + "'s checkpoints")
	findings = append(findings, checkpoints...)
	if !noTable {
		rows, err := enrollSelectionRows(ctx, store)
		if err != nil {
			return unknownf(err, "%s's selection rows", family)
		}
		r.read(family + "'s selection rows")
		findings = append(findings, rows...)
	}
	stream, err := streamInfo(ctx, nc, notifications)
	if err != nil {
		return unknownf(err, "the info of %s", notifications)
	}
	r.read("the info of " + notifications)
	streamID := selection.StreamIdentity(stream)
	shared, err := r.enrollSharedAddress(ctx, project)
	if err != nil {
		return err
	}
	r.read("the daemons' Deployments")
	var problems []string
	if len(findings) > 0 {
		problems = append(problems, fmt.Sprintf("%s's store is not new: %s", family, strings.Join(findings, "; ")))
	}
	if shared != "" {
		problems = append(problems, fmt.Sprintf("%s's store address is shared: %s", family, shared))
	}
	switch {
	case streamID == "":
		problems = append(problems, fmt.Sprintf("%s carries no stream identity (%s), which the enrolled daemon tags each last floor with", notifications, selection.StreamIdentityMetadataKey))
	case p.marker && marker.StreamID != "" && marker.StreamID != streamID:
		problems = append(problems, fmt.Sprintf("this hold's marker records stream identity %s, and %s carries %s", marker.StreamID, notifications, streamID))
	}
	if len(problems) > 0 {
		return a.stop(ctx, strings.Join(problems, "; "), enrollLeft(family, r.o.holdID, p.marker, p.broker))
	}

	// Before its first write, the held re-read again (plan §3a, "Every held command"), so no writer
	// connected across the hold has made a consumer of the store's names since the listing.
	if err := a.reread(ctx); err != nil {
		return err
	}
	// The writes. A marker of this hold keeps the boundary it recorded, the lower one.
	boundary := stream.State.LastSeq + 1
	if p.marker && marker.Boundary > 0 {
		boundary = marker.Boundary
	}
	if !(p.marker && marker.Boundary > 0 && marker.StreamID != "") {
		p.unsure = family + "'s store marker, if its unanswered write landed"
		err := enrollWriteMarker(ctx, store, marker, revision, markerFound, r.o.holdID, boundary, streamID)
		var refused *enrollRefusal
		switch {
		case errors.As(err, &refused):
			p.unsure = ""
			return a.stop(ctx, fmt.Sprintf("%s's marker: %v", family, err), enrollLeft(family, r.o.holdID, p.marker, p.broker))
		case err != nil:
			return unknownf(err, "%s's marker", family)
		}
		p.unsure, p.marker = "", true
	}
	if !p.broker {
		p.unsure = "the broker's cutover record " + family + ", if its unanswered write landed"
		_, err := r.records.PutCutover(ctx, family, selection.CutoverDone, r.o.holdID)
		var route *holdrecord.RouteError
		switch {
		case errors.As(err, &route):
			p.unsure = ""
			return a.stop(ctx, fmt.Sprintf("the broker refused the cutover record %s: %v", family, err), enrollLeft(family, r.o.holdID, p.marker, p.broker))
		case err != nil:
			return unknownf(err, "the broker's cutover record %s, after the store's marker was written", family)
		}
		p.unsure, p.broker = "", true
	}
	outputs := map[string]string{"family": family, "cutover": selection.CutoverDone, "boundary": formatSeq(boundary), "stream_id": streamID}
	if err := a.done(ctx, outputs); err != nil {
		return err
	}
	fmt.Fprintf(r.stdout, "enroll: %s: the store's marker and the broker's cutover record read %s under hold %s; boundary %s, stream identity %s\n",
		family, selection.CutoverDone, r.o.holdID, formatSeq(boundary), streamID)
	return nil
}

// enrollSharedAddress is why project's store is no one daemon's, "" when nothing says so: each
// address a Deployment of project names (writerhold.DaemonProjectAnnotation, with its store's
// database and instance annotations) that another Deployment names too (daemonAddressSharers),
// naming each Deployment and its project, as the census's rule for the daemon stores refuses such
// an address (censusReadDaemonStore). The daemons' Deployments are listed with the hold's own
// Kubernetes client (writerhold.KubeDaemonDeployments); a listing it cannot read is UNKNOWN.
func (r *run) enrollSharedAddress(ctx context.Context, project string) (string, error) {
	op, err := r.operatorHub(ctx)
	if err != nil {
		return "", unknownf(err, "the daemons' Deployments")
	}
	listed, err := writerhold.KubeDaemonDeployments(op.Hub.Client)(ctx)
	if err != nil {
		return "", unknownf(err, "the daemons' Deployments, against which %s's store address is checked", daemonFamily(project))
	}
	var shared []string
	for _, d := range listed {
		database, instance := d.Annotations[writerhold.DaemonStoreDatabaseAnnotation], d.Annotations[writerhold.DaemonStoreInstanceAnnotation]
		if d.Annotations[writerhold.DaemonProjectAnnotation] != project || database == "" || instance == "" {
			continue
		}
		if sharers := daemonAddressSharers(listed, d.Name, database, instance); len(sharers) > 0 {
			shared = append(shared, daemonSharedAddress(project, d.Name, database, instance, sharers))
		}
	}
	return strings.Join(shared, "; "), nil
}

// enrollLeft is what every run of enroll in this hold left outside the journal, which its stop
// records: holdrecord.LeftUnchanged while no run has written this hold's store marker (marker) or
// the family's broker cutover record (broker), else each of them that exists.
func enrollLeft(family, holdID string, marker, broker bool) string {
	var left []string
	if marker {
		left = append(left, "store marker done")
	}
	if broker {
		left = append(left, "broker cutover record done")
	}
	if len(left) == 0 {
		return holdrecord.LeftUnchanged
	}
	return fmt.Sprintf("%s: %s, hold %s's", family, strings.Join(left, " and "), holdID)
}

// enrollRefusal is a marker write the store refused for good: another writer's marker put there
// under the write, or a conditional write refused again and again. The write did not land.
type enrollRefusal struct{ what string }

func (e *enrollRefusal) Error() string { return e.what }

// enrollLogicalNames are the logical names of project's daemon: legion-go-<project>-<kind> for
// each kind it opens (daemonKinds).
func enrollLogicalNames(project string) []string {
	names := make([]string, 0, len(daemonKinds))
	for _, kind := range daemonKinds {
		names = append(names, daemonFixedPrefix+project+"-"+kind)
	}
	return names
}

// enrollHoldName names the hold a marker or a cutover record was written under.
func enrollHoldName(holdID string) string {
	if holdID == "" {
		return "no named hold"
	}
	return "hold " + holdID
}

// enrollReadMarker reads the store's sentinel with its revision (familyStore's sentinel, through
// the store's own check), and whether the row exists. A missing table is selection.ErrNoStore; a
// row that is not exactly the store's own sentinel is the store's selection.ErrMalformedSentinel,
// a read error, never a missing marker.
func enrollReadMarker(ctx context.Context, store familyStore) (selection.StoreState, uint64, bool, error) {
	state, revision, found, err := store.sentinel(ctx)
	switch {
	case pgNoTable(err):
		return selection.StoreState{}, 0, false, fmt.Errorf("%w: %s (%w)", selection.ErrNoStore, selection.IntakeConsumersTable, err)
	case err != nil || !found:
		return selection.StoreState{}, 0, false, err
	}
	return state, revision, true, nil
}

// enrollWriteMarker writes the store's marker through store's conditional sentinel write: cutover
// done naming holdID, boundary and streamID, keeping every other field of the marker it read
// (state, at revision, found). A refused write reads again, and ends once it reads a whole marker
// this hold wrote; a marker of this hold that lacks the boundary or the identity is written again.
// A marker another writer put there in the meantime, or a write refused every time, is an
// *enrollRefusal: the write did not land. Any other error is a write whose outcome is not known.
func enrollWriteMarker(ctx context.Context, store familyStore, state selection.StoreState, revision uint64, found bool, holdID string, boundary uint64, streamID string) error {
	for range 8 {
		write := state
		write.Cutover, write.Hold, write.Boundary, write.StreamID = selection.CutoverDone, holdID, boundary, streamID
		if !found {
			revision = 0
		}
		_, err := store.WriteSentinel(ctx, write, revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, selection.ErrConflict) {
			return err
		}
		if state, revision, found, err = enrollReadMarker(ctx, store); err != nil {
			return err
		}
		own := found && state.Cutover == selection.CutoverDone && state.Hold == holdID
		switch {
		case own && state.Boundary > 0 && state.StreamID != "":
			return nil
		case found && state.Cutover != "" && !own:
			return &enrollRefusal{fmt.Sprintf("the marker changed under the write: it reads %s from %s", state.Cutover, enrollHoldName(state.Hold))}
		}
	}
	return &enrollRefusal{"the marker's conditional write was refused eight times"}
}

// enrollCheckpoints names each checkpoint the store's daemon keeps (consumer_positions, keyed by
// the logical name: packages/daemon/internal/intake/durable_names.go, missingRecord). A missing
// table is selection.ErrNoStore.
func enrollCheckpoints(ctx context.Context, db Database) ([]string, error) {
	rows, err := db.Query(ctx, `select consumer, stream_seq from consumer_positions order by consumer`)
	if pgNoTable(err) {
		return nil, fmt.Errorf("%w: consumer_positions (%w)", selection.ErrNoStore, err)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var consumer string
		var seq int64
		if err := rows.Scan(&consumer, &seq); err != nil {
			return nil, err
		}
		found = append(found, fmt.Sprintf("consumer_positions holds a checkpoint of %s at stream sequence %d", consumer, seq))
	}
	if err := rows.Err(); pgNoTable(err) {
		return nil, fmt.Errorf("%w: consumer_positions (%w)", selection.ErrNoStore, err)
	} else if err != nil {
		return nil, err
	}
	return found, nil
}

// enrollSelectionRows names each selection row of store but its sentinel (selection.PGStore.List).
func enrollSelectionRows(ctx context.Context, store familyStore) ([]string, error) {
	lister, ok := store.(interface {
		List(ctx context.Context) (map[string]selection.Record, error)
	})
	if !ok {
		return nil, fmt.Errorf("%T lists no selection rows", store)
	}
	records, err := lister.List(ctx)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, logical := range slices.Sorted(maps.Keys(records)) {
		found = append(found, fmt.Sprintf("%s holds the selection row of %s", selection.IntakeConsumersTable, logical))
	}
	return found, nil
}
