package consumernames

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/natsgrant"
)

// cleanupMaxConflicts is how many refused writes of one record cleanup reads again past before it
// leaves the record for its next run.
const cleanupMaxConflicts = 10

// cleanupTarget is one record cleanup works on: its family's store and its logical name.
type cleanupTarget struct {
	family  string
	logical string
	store   selection.Store
}

// cleanup deletes, with the row held under --hold, the retired names of every family's records
// that the server still holds, and drops from each record every retired name the server reports
// absent: the session family's and every listener durable's records in the selection bucket,
// walked behind the hold's bucket barrier (behindBarrier, writerhold.ReadBucket), and Dispatch's
// and each daemon's intake_consumers rows. It is the one command that deletes a daemon's names,
// whose grant has no consumer delete (§2, "Cleanup"). It deletes only names a record retired, never
// a name the record still selects (its current, or its open operation's candidate or predecessor),
// and never by a name pattern. Each record's write is conditional on the revision it read; a
// refused write (selection.ErrConflict) reads the record again, which finds the names it deleted
// absent.
//
// It reads every store before it deletes anything: a store a family needs that no flag names
// refuses the run (Dispatch's, and each daemon's that the broker's cutover rows or
// --daemon-database name), and a reading that is not whole is UNKNOWN; either way nothing is
// deleted. It does not use selection.Names.Cleanup, which runs Step 0 on a record's open
// operation (a creator's protocol, whose owner may be the process the hold stopped) and refuses a
// store whose marker is not done, so it could not clean the names a rollback retired. No workflow
// of the hold's table names cleanup (internal/holdrecord/table.go tableFor), so it writes no
// journal entry. It runs the held re-read when it starts (requirePhase) and again before its first
// delete.
func (r *run) cleanup(ctx context.Context) error {
	if _, err := r.requirePhase(ctx, natsgrant.OperatorPhaseHeld); err != nil {
		return err
	}
	cutovers, err := r.records.Cutovers(ctx)
	if err != nil {
		return unknownf(err, "the broker's cutover rows")
	}
	families := pgFamilies(r.o, cutovers)
	var unnamed []string
	for _, family := range families {
		if file, _ := r.o.storeFile(family); file == "" {
			unnamed = append(unnamed, family)
		}
	}
	if len(unnamed) > 0 {
		return fmt.Errorf("cleanup reads every family's store before it deletes a name, and no flag names the store of %s (--dispatch-database-file, --daemon-database <project>=<file>)", strings.Join(unnamed, ", "))
	}
	nc, _, err := r.conn(ctx)
	if err != nil {
		return unknownf(err, "the hub")
	}
	targets, err := r.cleanupSessionTargets(ctx, nc)
	if err != nil {
		return err
	}
	for _, family := range families {
		more, err := r.cleanupPGTargets(ctx, family)
		if err != nil {
			return err
		}
		targets = append(targets, more...)
	}
	// Every held command reads the hold again before its mutation steps (heldReread), not only
	// when it starts: its refusal deletes nothing.
	if err := r.heldReread(ctx); err != nil {
		return err
	}

	var deletedAll, droppedAll int
	var failures []string
	for _, target := range targets {
		deleted, dropped, err := cleanupRecord(ctx, nc, target)
		deletedAll += len(deleted)
		droppedAll += len(dropped)
		if len(deleted) > 0 || len(dropped) > 0 {
			fmt.Fprintf(r.stdout, "cleanup %s %s: deleted %v, dropped from retired %v\n", target.family, target.logical, deleted, dropped)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s %s: %v", target.family, target.logical, err))
		}
	}
	fmt.Fprintf(r.stdout, "cleanup: %d records with retired names, %d names deleted, %d dropped from retired\n", len(targets), deletedAll, droppedAll)
	if len(failures) > 0 {
		return fmt.Errorf("cleanup left retired names for its next run: %s", strings.Join(failures, "; "))
	}
	return nil
}

// cleanupSessionTargets are the selection bucket's records that hold a retired name, a session's
// or a listener durable's, walked behind the hold's bucket barrier (behindBarrier), as every held
// reader of the bucket reads it, each value by its key's kind (readSelectionValue). A missing
// bucket holds none. A malformed value anywhere in the bucket is UNKNOWN, naming each such key, so
// cleanup deletes nothing.
func (r *run) cleanupSessionTargets(ctx context.Context, nc *nats.Conn) ([]cleanupTarget, error) {
	if _, err := streamInfo(ctx, nc, selectionStreamName); err != nil {
		if apiErrorIs(err, jetstream.JSErrCodeStreamNotFound) {
			return nil, nil
		}
		return nil, unknownf(err, "the info of %s", selectionStreamName)
	}
	var keys []statusKey
	if err := behindBarrier(ctx, nc, r.o.holdID, func(ctx context.Context) error {
		var err error
		keys, err = statusSelectionKeys(ctx, nc)
		return err
	}); err != nil {
		return nil, err
	}
	store, err := r.store(ctx, familySession)
	if err != nil {
		return nil, err
	}
	var targets []cleanupTarget
	var malformed []string
	for _, key := range keys {
		if key.Deleted {
			continue
		}
		value, err := readSelectionValue(key)
		switch {
		case err != nil:
			malformed = append(malformed, fmt.Sprintf("%s (%v)", key.Key, err))
		case value.record != nil && len(value.record.Retired) > 0:
			targets = append(targets, cleanupTarget{family: familySession, logical: key.Key, store: store})
		}
	}
	if len(malformed) > 0 {
		return nil, unknownf(nil, "the values of %s in %s are malformed, so cleanup reads no record there", strings.Join(malformed, ", "), selection.SessionBucket)
	}
	return targets, nil
}

// cleanupPGTargets are family's intake_consumers records that hold a retired name. A missing table
// holds none.
func (r *run) cleanupPGTargets(ctx context.Context, family string) ([]cleanupTarget, error) {
	db, err := r.database(ctx, family)
	if err != nil {
		return nil, fmt.Errorf("%s's store: %w", family, err)
	}
	store := selection.NewPGStore(db)
	records, err := store.List(ctx)
	if errors.Is(err, selection.ErrNoStore) {
		return nil, nil
	}
	if err != nil {
		return nil, unknownf(err, "%s's records", family)
	}
	var targets []cleanupTarget
	for logical, record := range records {
		if len(record.Retired) > 0 {
			targets = append(targets, cleanupTarget{family: family, logical: logical, store: store})
		}
	}
	slices.SortFunc(targets, func(a, b cleanupTarget) int { return strings.Compare(a.logical, b.logical) })
	return targets, nil
}

// cleanupRecord deletes target's retired names the server holds and drops from its record each one
// the server reports absent, the deleted ones included, in a write conditional on the revision it
// read. It answers the names it deleted and dropped. A retired name the record still selects is
// left as it is. A name whose info or delete failed stays retired, and is answered in the error.
func cleanupRecord(ctx context.Context, nc *nats.Conn, target cleanupTarget) ([]string, []string, error) {
	var deleted []string
	var failed []string
	for range cleanupMaxConflicts {
		record, revision, err := target.store.Read(ctx, target.logical)
		if err != nil {
			return deleted, nil, err
		}
		selected := []string{record.Current}
		if record.Op != nil {
			selected = append(selected, record.Op.Candidate, record.Op.Old)
		}
		failed = failed[:0]
		var gone []string
		for _, name := range record.Retired {
			if slices.Contains(selected, name) {
				continue
			}
			info, err := consumerInfo(ctx, nc, notifications, name)
			if err != nil {
				failed = append(failed, fmt.Sprintf("the info of %s: %v", name, err))
				continue
			}
			if info != nil {
				if err := deleteConsumer(ctx, nc, notifications, name); err != nil {
					failed = append(failed, fmt.Sprintf("the delete of %s: %v", name, err))
					continue
				}
				if !slices.Contains(deleted, name) {
					deleted = append(deleted, name)
				}
			}
			gone = append(gone, name)
		}
		var failure error
		if len(failed) > 0 {
			failure = errors.New(strings.Join(failed, "; "))
		}
		if len(gone) == 0 {
			return deleted, nil, failure
		}
		next := record
		next.Retired = slices.DeleteFunc(slices.Clone(record.Retired), func(name string) bool { return slices.Contains(gone, name) })
		if len(next.Retired) == 0 {
			next.Retired = nil
		}
		_, err = target.store.Write(ctx, target.logical, next, revision)
		if errors.Is(err, selection.ErrConflict) {
			continue
		}
		if err != nil {
			return deleted, nil, err
		}
		return deleted, gone, failure
	}
	return deleted, nil, fmt.Errorf("its record changed under %d writes: %w", cleanupMaxConflicts, selection.ErrConflict)
}
