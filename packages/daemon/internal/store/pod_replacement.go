package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// PodReplacement is the record of a replacement of an issue's pod that may take a disk (R7 of
// dispatch://INFRA-3271, GPT-6.1 Sol's points 1 and 2 on #5285): the daemon writes it before the
// runtime acts, and deletes it once the act is made, in the transaction that queues the issue's
// architect's notice of what the act took (FinishPodReplacement), or once the runtime shows the act
// was not made (ForgetPodReplacement). One stands for each issue and pod (pod_replacements). Record
// is the daemon's own encoding of the replacement, which the store keeps whole and compares whole:
// a finish or a forget acts on the row only where it still holds that record, so neither takes a
// later replacement's row of the same pod.
type PodReplacement struct {
	Issue  string
	PodUID string
	Record []byte
}

// RecordPodReplacement writes the record of issue's replacement of podUID, or writes it again over
// the row that stands for that pod, with the moment of this write.
func (s *Store) RecordPodReplacement(ctx context.Context, project, issue, podUID string, record []byte) error {
	if project == "" || issue == "" || len(record) == 0 {
		return errors.New("the record of a pod's replacement needs its project, its issue and the record")
	}
	if err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into pod_replacements (project, issue, pod_uid, record, written_at)
			values ($1, $2, $3, $4::jsonb, now())
			on conflict (project, issue, pod_uid) do update set record = excluded.record, written_at = excluded.written_at`,
			project, issue, podUID, string(record))
		return err
	}); err != nil {
		return fmt.Errorf("record the replacement of %s's pod %s: %w", issue, podUID, err)
	}
	return nil
}

// PodReplacements is every record of a replacement of project's issue pods that stands, oldest
// first.
func (s *Store) PodReplacements(ctx context.Context, project string) ([]PodReplacement, error) {
	var standing []PodReplacement
	err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select issue, pod_uid, record::text
			from pod_replacements where project = $1 order by written_at, issue, pod_uid`, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r      PodReplacement
				record string
			)
			if err := rows.Scan(&r.Issue, &r.PodUID, &record); err != nil {
				return err
			}
			r.Record = []byte(record)
			standing = append(standing, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read the records of %s's pod replacements: %w", project, err)
	}
	return standing, nil
}

// FinishPodReplacement deletes the record of issue's replacement of podUID that holds record, and,
// in the same transaction, queues notice, where it is not nil, as the issue's outbox row
// (insertOutboxRow): the notice is queued exactly when the record goes, so a store that refuses
// either keeps both as they were, and a second finish of the same record queues nothing. It answers
// whether it found the record. A notice of an issue of another project than project is queued by
// no daemon of this one, so the record goes with no row queued, and told says so.
func (s *Store) FinishPodReplacement(ctx context.Context, project, issue, podUID string, record []byte, notice *IdleReleaseRow) (finished, told bool, err error) {
	err = Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from pod_replacements where project = $1 and issue = $2 and pod_uid = $3 and record = $4::jsonb`,
			project, issue, podUID, string(record))
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
		return false, false, fmt.Errorf("finish the replacement of %s's pod %s: %w", issue, podUID, err)
	}
	return finished, told, nil
}

// ForgetPodReplacement deletes the record of issue's replacement of podUID that holds record, where
// the runtime shows the act was not made: the pod still runs as the issue's own. It answers whether
// it found the record.
func (s *Store) ForgetPodReplacement(ctx context.Context, project, issue, podUID string, record []byte) (bool, error) {
	var forgot bool
	if err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from pod_replacements where project = $1 and issue = $2 and pod_uid = $3 and record = $4::jsonb`,
			project, issue, podUID, string(record))
		forgot = err == nil && tag.RowsAffected() == 1
		return err
	}); err != nil {
		return false, fmt.Errorf("forget the replacement of %s's pod %s: %w", issue, podUID, err)
	}
	return forgot, nil
}
