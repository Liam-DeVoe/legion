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
// runtime acts, and deletes it once the act is made, with every other record of the same target,
// in the transaction that queues the issue's architect's notice of what the act took
// (FinishPodReplacement), or once the runtime shows the act was not made (ForgetPodReplacement).
// Operation is the act's own identity, which keys the row (pod_replacements): two acts on the same
// pod are two rows, and no write goes over a row that stands (GPT-6.1 Sol's point 3 on #5285 at
// fa9deb04). PodUID is the pod the act takes, "" where
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

// FinishPodReplacement finishes the record of operation, a replacement of issue's pod whose act is
// made: it deletes that record and every other record of an act on the same target (what the act
// takes and the uid of what it takes), and, in the same transaction, queues notice, where it is
// not nil, as the issue's outbox row (insertOutboxRow). One target is taken once, so every act
// recorded on it is over once one of them is made, and the issue's architect is told of it once
// (the Legion reviewer's Important 1 on #5285 at 7da847c0): two acts on one target each stand
// recorded under an operation of their own (a replacement by issue and the launch that replaced the
// round's pod while it waited, a launch that failed and the act after it, a launch that stopped
// before its delete and the next launch), and the runtime reads each made from the target alone.
// The notice is queued exactly when the records go, so a store that refuses either keeps both as
// they were, and a later finish of any of those records finds nothing and queues nothing. A nil
// notice finishes the records with nothing queued: an act made over a read that found nothing,
// which is the last read of the target before it was taken. It answers whether it found the record
// of operation. A notice of an issue of another project than project is queued by no daemon of
// this one, so the records go with no row queued, and told says so.
//
// withPod, where it is not "", is a pod the act took with its own target: a launch's delete of the
// issue's Sandbox and its kept volume over a live pod of that Sandbox (runtime.TakenPod.WithPod).
// The records of acts that take that pod are over with it, and go too (the Legion reviewer's
// Important 1 on #5285 at acda4d1e).
func (s *Store) FinishPodReplacement(ctx context.Context, project, issue, operation, withPod string, notice *IdleReleaseRow) (finished, told bool, err error) {
	err = Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var takes, target string
		err := tx.QueryRow(ctx, `delete from pod_replacements where project = $1 and issue = $2 and operation = $3
			returning takes, target_uid`, project, issue, operation).Scan(&takes, &target)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		finished = true
		if _, err := tx.Exec(ctx, `delete from pod_replacements where project = $1 and issue = $2
			and ((takes = $3 and target_uid = $4) or ($5 <> '' and takes = $6 and target_uid = $5))`,
			project, issue, takes, target, withPod, string(runtime.TakesPod)); err != nil {
			return err
		}
		if notice == nil || !claim.KeyOfProject(project, issue) {
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

// ForgetPodReplacement deletes the record of operation, a replacement of issue's pod whose act was
// not made: the runtime shows the target still in use, or the act's last word refused it. It
// deletes that record alone, since an act not made says nothing of another act on the same
// target, which the runtime reads of its own.
func (s *Store) ForgetPodReplacement(ctx context.Context, project, issue, operation string) error {
	if err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from pod_replacements where project = $1 and issue = $2 and operation = $3`, project, issue, operation)
		return err
	}); err != nil {
		return fmt.Errorf("forget the replacement %s of %s's pod: %w", operation, issue, err)
	}
	return nil
}
