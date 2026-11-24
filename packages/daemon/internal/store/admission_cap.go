package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdmissionCapChange is one change of the admission cap the project's controller or the operator
// made while the daemon runs (dispatch://INFRA-5616), as
// migrations/20261124040825_admission_cap.up.sql defines its row: the cap it set, 0 for a clear,
// the caps the daemon admitted under before it and from it on, the deployment's ceiling then, who
// made it, in a pause's actor shape, why, when, and DecisionID, the regulator's decision that made
// it (ParseAdmissionDecision), "" for a change no regulator made. Project is the project's Dispatch
// key. ID is the row's, 0 before the row is written.
type AdmissionCapChange struct {
	ID         int64
	Project    string
	Cap        int
	CapBefore  int
	CapAfter   int
	Ceiling    int
	By         PauseActor
	Reason     string
	DecisionID string
	At         time.Time
}

// Cleared is whether the change is a clear, which returns the project to the deployment's cap.
func (c AdmissionCapChange) Cleared() bool { return c.Cap == 0 }

// AdmissionCapChangesShown is how many changes the state lists, newest first
// (AdmissionCapChangesTx). The table keeps every change, and the daemon's log names each.
const AdmissionCapChangesShown = 50

// EffectiveAdmissionCap is the cap a pass admits under: the deployment's configured cap
// (legion.yaml's admission_cap, else LEGION_ADMISSION_CAP), which is the ceiling, where no cap set
// while the daemon runs stands; else the smaller of that cap and the ceiling. The smaller of the
// two, and not the cap set, because the routes refuse only a cap above the ceiling of their own
// boot, and a later configuration may have lowered the ceiling under it. standing is the project's
// newest change (AdmissionCapTx), found false where none was ever made.
func EffectiveAdmissionCap(ceiling int, standing AdmissionCapChange, found bool) int {
	if !found || standing.Cleared() {
		return ceiling
	}
	return min(standing.Cap, ceiling)
}

// admissionCapColumns is a change row as scanAdmissionCapChange reads it.
const admissionCapColumns = `id, project, coalesce(cap, 0), cap_before, cap_after, ceiling, by_kind, by_session, reason,
	coalesce(decision_id, ''), changed_at`

func scanAdmissionCapChange(row pgx.Row) (AdmissionCapChange, error) {
	var c AdmissionCapChange
	var kind string
	if err := row.Scan(&c.ID, &c.Project, &c.Cap, &c.CapBefore, &c.CapAfter, &c.Ceiling, &kind, &c.By.Session,
		&c.Reason, &c.DecisionID, &c.At); err != nil {
		return AdmissionCapChange{}, err
	}
	c.By.Kind, c.At = PauseActorKind(kind), c.At.UTC()
	return c, nil
}

// AdmissionCapTx is, through tx, project's newest change of the cap, and whether one was ever
// made: the cap set while the daemon runs where it set one (EffectiveAdmissionCap reads it), and
// none where it was a clear. project is the Dispatch key.
func AdmissionCapTx(ctx context.Context, tx pgx.Tx, project string) (AdmissionCapChange, bool, error) {
	c, err := scanAdmissionCapChange(tx.QueryRow(ctx, `select `+admissionCapColumns+` from project_admission_cap_changes
		where project = $1 order by id desc limit 1`, project))
	if errors.Is(err, pgx.ErrNoRows) {
		return AdmissionCapChange{}, false, nil
	}
	if err != nil {
		return AdmissionCapChange{}, false, fmt.Errorf("read the admission cap of %s: %w", project, err)
	}
	return c, true, nil
}

// AdmissionCapChangesTx is, through tx, project's newest changes of the cap, newest first, at most
// limit of them: the state's `admission.capChanges`. Never nil.
func AdmissionCapChangesTx(ctx context.Context, tx pgx.Tx, project string, limit int) ([]AdmissionCapChange, error) {
	rows, err := tx.Query(ctx, `select `+admissionCapColumns+` from project_admission_cap_changes
		where project = $1 order by id desc limit $2`, project, limit)
	if err != nil {
		return nil, fmt.Errorf("read the admission cap changes of %s: %w", project, err)
	}
	defer rows.Close()
	changes := []AdmissionCapChange{}
	for rows.Next() {
		c, err := scanAdmissionCapChange(rows)
		if err != nil {
			return nil, fmt.Errorf("read the admission cap changes of %s: %w", project, err)
		}
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the admission cap changes of %s: %w", project, err)
	}
	return changes, nil
}

// ParseAdmissionDecision reads a regulator's decision id of project, `admission:<KEY>:<time>`
// (dispatch://INFRA-5616/spec#b-r6-fenced-ticks): KEY is project, the Dispatch key, and time the
// RFC 3339 time, in UTC (`Z`), of the newest value the decision rests on, which orders the project's
// decisions. It answers that time, and an error naming the form for an id of any other form.
func ParseAdmissionDecision(project, id string) (time.Time, error) {
	prefix := "admission:" + project + ":"
	stamp, ok := strings.CutPrefix(id, prefix)
	if !ok {
		return time.Time{}, fmt.Errorf("the decision id %q is not of the form %s<RFC 3339 UTC time>", id, prefix)
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || !strings.HasSuffix(stamp, "Z") {
		return time.Time{}, fmt.Errorf("the decision id %q is not of the form %s<RFC 3339 UTC time>: %q is no RFC 3339 time in UTC", id, prefix, stamp)
	}
	return at.UTC(), nil
}

// DecisionFencedError refuses a change of the cap that names a regulator decision when the project
// already holds a change of a decision at the same time or a later one
// (dispatch://INFRA-5616/spec#b-r6-fenced-ticks): Decision is the decision the change named and
// Newest the project's newest decision.
type DecisionFencedError struct {
	Project  string
	Decision string
	Newest   string
}

func (e *DecisionFencedError) Error() string {
	return fmt.Sprintf("the admission cap of %s already holds a change of decision %s, whose time is not before %s's: a decision sets the cap once, and a late decision never undoes a newer one",
		e.Project, e.Newest, e.Decision)
}

// FenceAdmissionCapDecisionTx is the fence of the regulator's decisions, through tx. It takes the
// project's transaction lock on its cap changes first, so two changes of one project are judged one
// after the other and never against the same newest decision, and refuses decision, with a
// *DecisionFencedError, when the project holds a change of a decision at decision's time or later.
// A decision of another form is an error (ParseAdmissionDecision). A change that names no decision
// ("") is not fenced, and still takes the lock.
func FenceAdmissionCapDecisionTx(ctx context.Context, tx pgx.Tx, project, decision string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('project_admission_cap_changes:' || $1, 0))`, project); err != nil {
		return fmt.Errorf("lock the admission cap changes of %s: %w", project, err)
	}
	if decision == "" {
		return nil
	}
	at, err := ParseAdmissionDecision(project, decision)
	if err != nil {
		return err
	}
	var newest string
	var newestAt time.Time
	err = tx.QueryRow(ctx, `select decision_id, decision_at from project_admission_cap_changes
		where project = $1 and decision_at is not null order by decision_at desc, id desc limit 1`, project).Scan(&newest, &newestAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the newest decision of the admission cap of %s: %w", project, err)
	}
	if !newestAt.Before(at) {
		return &DecisionFencedError{Project: project, Decision: decision, Newest: newest}
	}
	return nil
}

// RecordAdmissionCapChangeTx appends change through tx and answers it with its id. It is the one
// writer of project_admission_cap_changes, and admission's setAdmissionCap its one caller
// (admission_cap_writers_test.go holds both). It refuses a cap below 0, a reason that is only white
// space, a decision id of another form than ParseAdmissionDecision reads and an actor of neither
// shape before the table's checks would, and stores the decision's time beside its id.
func RecordAdmissionCapChangeTx(ctx context.Context, tx pgx.Tx, change AdmissionCapChange) (AdmissionCapChange, error) {
	if change.Cap < 0 {
		return AdmissionCapChange{}, fmt.Errorf("change the admission cap of %s to %d: a cap is at least 1, and a clear names none", change.Project, change.Cap)
	}
	if strings.TrimSpace(change.Reason) == "" {
		return AdmissionCapChange{}, fmt.Errorf("change the admission cap of %s: a change says why it is made", change.Project)
	}
	if err := change.By.validate(); err != nil {
		return AdmissionCapChange{}, fmt.Errorf("change the admission cap of %s: %w", change.Project, err)
	}
	var named *int
	if !change.Cleared() {
		named = &change.Cap
	}
	var decision *string
	var decisionAt *time.Time
	if change.DecisionID != "" {
		at, err := ParseAdmissionDecision(change.Project, change.DecisionID)
		if err != nil {
			return AdmissionCapChange{}, fmt.Errorf("change the admission cap of %s: %w", change.Project, err)
		}
		decision, decisionAt = &change.DecisionID, &at
	}
	if err := tx.QueryRow(ctx, `insert into project_admission_cap_changes
		(project, cap, cap_before, cap_after, ceiling, by_kind, by_session, reason, decision_id, decision_at, changed_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning id`,
		change.Project, named, change.CapBefore, change.CapAfter, change.Ceiling, string(change.By.Kind), change.By.Session,
		change.Reason, decision, decisionAt, change.At).Scan(&change.ID); err != nil {
		return AdmissionCapChange{}, fmt.Errorf("change the admission cap of %s: %w", change.Project, err)
	}
	return change, nil
}
