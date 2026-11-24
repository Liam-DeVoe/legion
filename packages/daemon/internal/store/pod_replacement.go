package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// PodReplacement is the record of a replacement of an issue's pod that may take a disk (R7 of
// #5285, GPT-6.1 Sol's points 1 and 2 on #5285): the daemon writes it before the
// runtime acts, and deletes it once the act is made, in the transaction that queues the issue's
// architect's notice of what the act took, or once the runtime shows the act was not made
// (FinishPodReplacement). Operation is the act's own identity, which keys the row
// (pod_replacements): two acts on the same pod are two rows, and no write goes over a row that
// stands (GPT-6.1 Sol's point 3 on #5285 at fa9deb04). PodUID is the pod the act takes, "" where
// the issue's Sandbox ran no pod of its own. Act is what the act takes and the uid of its target
// (runtime.RecordedAct; GPT-6.1 Sol's point 4), which the daemon has the runtime read to say
// whether the act was made. Record is the daemon's own encoding of the replacement, which the
// store keeps whole.
type PodReplacement struct {
	Operation string
	Issue     string
	PodUID    string
	Act       runtime.RecordedAct
	Record    []byte
}

// RecordPodReplacement writes r, the record of a replacement of one of project's issue pods, with
// the moment of this write. A row of r's operation that stands already is kept as it is: a record is
// never written over.
func (s *Store) RecordPodReplacement(ctx context.Context, project string, r PodReplacement) error {
	if project == "" || r.Operation == "" || r.Issue == "" || r.Act.Takes == "" || r.Act.Target == "" || len(r.Record) == 0 {
		return errors.New("the record of a pod's replacement needs its project, its operation, its issue, what it takes, its target and the record")
	}
	if err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into pod_replacements (project, operation, issue, pod_uid, takes, target_uid, record, written_at)
			values ($1, $2, $3, $4, $5, $6, $7::jsonb, now())
			on conflict (project, operation) do nothing`,
			project, r.Operation, r.Issue, r.PodUID, string(r.Act.Takes), r.Act.Target, string(r.Record))
		return err
	}); err != nil {
		return fmt.Errorf("record the replacement %s of %s's pod %s: %w", r.Operation, r.Issue, r.PodUID, err)
	}
	return nil
}

// PodReplacements is every record of a replacement of project's issue pods that stands, oldest
// first.
func (s *Store) PodReplacements(ctx context.Context, project string) ([]PodReplacement, error) {
	var standing []PodReplacement
	err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select operation, issue, pod_uid, takes, target_uid, record::text
			from pod_replacements where project = $1 order by written_at, operation`, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r             PodReplacement
				takes, record string
			)
			if err := rows.Scan(&r.Operation, &r.Issue, &r.PodUID, &takes, &r.Act.Target, &record); err != nil {
				return err
			}
			r.Act.Takes, r.Record = runtime.Takes(takes), []byte(record)
			standing = append(standing, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read the records of %s's pod replacements: %w", project, err)
	}
	return standing, nil
}

// FinishPodReplacement deletes the record of operation, a replacement of issue's pod, and, in the
// same transaction, queues notice, where it is not nil, as the issue's outbox row (insertOutboxRow):
// the notice is queued exactly when the record goes, so a store that refuses either keeps both as
// they were, and a second finish of the same record queues nothing. A nil notice deletes the record
// alone: an act the runtime shows was not made, or one its last word refused. It answers whether it
// found the record. A notice of an issue of another project than project is queued by no daemon of
// this one, so the record goes with no row queued, and told says so.
func (s *Store) FinishPodReplacement(ctx context.Context, project, issue, operation string, notice *IdleReleaseRow) (finished, told bool, err error) {
	err = Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from pod_replacements where project = $1 and operation = $2`, project, operation)
		if err != nil {
			return err
		}
		finished = tag.RowsAffected() == 1
		if !finished || notice == nil || !claim.KeyOfProject(project, issue) {
			return nil
		}
		if _, err := insertOutboxRow(ctx, tx, project, issue, *notice); err != nil {
			return err
		}
		told = true
		return nil
	})
	if err != nil {
		return false, false, fmt.Errorf("finish the replacement %s of %s's pod: %w", operation, issue, err)
	}
	return finished, told, nil
}
