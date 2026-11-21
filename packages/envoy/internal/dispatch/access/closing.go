package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A developer box holds no credential that lists a private project after that project's full
// switch: every agent on a box runs as the box's own user, so nothing on the box can keep such a
// credential to one of them. Two kinds of credential a box can hold list projects, a personal
// `dsp_` token Dispatch makes and a token the secrets broker mints a machine login or a person's
// sign-in on a machine. One rule says which projects neither may list, closedSQL: a project in its
// closing state, and a project private by its full switch (full_switch_at, RecordFullSwitch). A
// project private before its full switch, on its first day, is open to them until its owner's
// switch to private starts its closing state.
//
//   - The membership answer says whether a project is closed (Answer), and the broker mints and
//     renews no developer-box token of a closed one.
//   - RefuseClosedTokenProjects refuses a personal token that would list a closed project.
//   - StartClosing, the first step of the owner's switch to private, revokes every live personal
//     token that lists the project, and the switch completes only once the broker counts no
//     developer-box token of it still live (internal/dispatch/api's closeForDeveloperBoxes). The
//     switch's own commit then records the full switch, which keeps the project closed.
//
// So every way into the rule runs through StartClosing's revocation. projects.closing_since holds
// when the closing state began, on the database's clock, and is null otherwise; SetVisibility ends
// the state either way, completing the switch or abandoning it.

// ErrClosedTokenProject is a personal token that would list a project closed to developer boxes:
// Dispatch makes none.
var ErrClosedTokenProject = errors.New("no personal token lists a project that is private or turning private")

// closedSQL is the rule above over the projects row p: true when no developer-box credential may
// list p.
func closedSQL(p string) string {
	return `(` + p + `.closing_since is not null or (` + p + `.visibility = 'private' and ` + p + `.full_switch_at is not null))`
}

// Closing is a project's closing state as a switch to private reads it, under the project's owner
// lock: when its closing state began, nil when it is in none, beside the database's own time at
// the read, which the switch ages that start against.
type Closing struct {
	Since *time.Time
	Now   time.Time
}

// ReadClosing answers project key's closing state, read in tx; ErrProjectNotFound for a key no
// project holds.
func ReadClosing(ctx context.Context, tx pgx.Tx, key string) (Closing, error) {
	var state Closing
	err := tx.QueryRow(ctx, `select closing_since, statement_timestamp() from projects where key = $1`, key).Scan(&state.Since, &state.Now)
	if errors.Is(err, pgx.ErrNoRows) {
		return Closing{}, fmt.Errorf("%w: %s", ErrProjectNotFound, key)
	}
	if err != nil {
		return Closing{}, fmt.Errorf("read the closing state of project %s: %w", key, err)
	}
	return state, nil
}

// StartClosing puts project key in its closing state, in tx and under the project's owner lock,
// which its caller holds (LockForAccessWrite): it sets closing_since to the statement's own start,
// revokes every live personal token that lists the project, and appends project.access_changed.
// It answers the project's access as tx left it, the event, and how many tokens it revoked.
//
// The project's row is written before the tokens, so a personal token's mint or edit that lists
// the project, which holds the row FOR SHARE until it commits (RefuseClosedTokenProjects), either
// commits first, and its token is revoked here, or waits for this write and is refused.
func StartClosing(ctx context.Context, tx pgx.Tx, ev *events.Broker, key string, by model.Actor) (model.ProjectAccess, model.Event, int64, error) {
	if err := lockProject(ctx, tx, key); err != nil {
		return model.ProjectAccess{}, model.Event{}, 0, err
	}
	started, err := tx.Exec(ctx, `update projects set closing_since = statement_timestamp() where key = $1`, key)
	if err != nil {
		return model.ProjectAccess{}, model.Event{}, 0, fmt.Errorf("start the closing state of project %s: %w", key, err)
	}
	if started.RowsAffected() == 0 {
		return model.ProjectAccess{}, model.Event{}, 0, fmt.Errorf("%w: %s", ErrProjectNotFound, key)
	}
	tag, err := tx.Exec(ctx, `update agent_tokens set revoked_at = now() where revoked_at is null and $1 = any(projects)`, key)
	if err != nil {
		return model.ProjectAccess{}, model.Event{}, 0, fmt.Errorf("revoke the personal tokens that list project %s: %w", key, err)
	}
	written, event, err := appendProjectChange(ctx, tx, ev, key, model.AccessVisibilityChanged, "", by)
	if err != nil {
		return model.ProjectAccess{}, model.Event{}, 0, err
	}
	return written, event, tag.RowsAffected(), nil
}

// RefuseClosedTokenProjects is the check a personal token's mint and its edit make, in the
// transaction that writes the token: it takes each of projects' rows FOR SHARE, in key order, and
// answers ErrClosedTokenProject naming the first that is closed to developer boxes (closedSQL), nil
// when none is. The shared row lock is what orders it against StartClosing, whose write of the row
// waits for it, so no token that lists the project commits after the closing state's revocation
// has run. projects are keys the caller sees, so the refusal names nothing the caller may not see.
func RefuseClosedTokenProjects(ctx context.Context, tx pgx.Tx, projects []string) error {
	if len(projects) == 0 {
		return nil
	}
	var closed string
	err := tx.QueryRow(ctx, `
		with listed as (
			select p.key, `+closedSQL("p")+` as closed
			from projects p where p.key = any($1) order by p.key for share
		)
		select coalesce(min(key) filter (where closed), '') from listed
	`, projects).Scan(&closed)
	if err != nil {
		return fmt.Errorf("read the projects a personal token lists: %w", err)
	}
	if closed != "" {
		return fmt.Errorf("%w: %s is one", ErrClosedTokenProject, closed)
	}
	return nil
}
