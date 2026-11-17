package legion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Settings is one project's Legion setting: the daemon Dispatch reads that project's state from.
type Settings struct {
	Project   string      `json:"project"`
	StateURL  string      `json:"state_url"`
	CreatedBy model.Actor `json:"created_by"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// settingsColumns is the legion_settings select list scanSettings reads, in scan order; every
// query that returns a setting selects exactly this.
const settingsColumns = `project, state_url, created_by, updated_at`

// ErrNoSettings is "this project names no Legion daemon": the sampler reads nothing for it and
// the board has nothing to show.
var ErrNoSettings = errors.New("this project has no Legion daemon configured")

// StateChannel is the Postgres notification channel one read of a daemon's state announces itself
// on, with the project as its payload, in the shape of delivery.SettingsChannel: nothing commits
// to announce a remote daemon's state, so the sampler's own write is what an open Legion board
// hears (api/delivery_live_hub.go). A read announces itself whether or not it changed an
// observation, since the board's header carries the read itself — when it was made, and why it
// failed.
const StateChannel = "dispatch_legion_state"

// NotifyRead announces a read of project's daemon on StateChannel.
func NotifyRead(ctx context.Context, pool *store.Pool, project string) error {
	if _, err := pool.Exec(ctx, `select pg_notify($1, $2)`, StateChannel, project); err != nil {
		return fmt.Errorf("announce the Legion state read of %s: %w", project, err)
	}
	return nil
}

// scanSettings decodes one settingsColumns row.
func scanSettings(row pgx.Row) (Settings, error) {
	var settings Settings
	var createdBy []byte
	if err := row.Scan(&settings.Project, &settings.StateURL, &createdBy, &settings.UpdatedAt); err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal(createdBy, &settings.CreatedBy); err != nil {
		return Settings{}, fmt.Errorf("decode the Legion setting's author: %w", err)
	}
	return settings, nil
}

// ListSettings is every project's Legion setting, by project key; a deployment with none reads an
// empty list.
func ListSettings(ctx context.Context, pool *store.Pool) ([]Settings, error) {
	rows, err := pool.Query(ctx, `select `+settingsColumns+` from legion_settings order by project`)
	if err != nil {
		return nil, fmt.Errorf("read the Legion settings: %w", err)
	}
	defer rows.Close()
	listed := []Settings{}
	for rows.Next() {
		settings, err := scanSettings(rows)
		if err != nil {
			return nil, err
		}
		listed = append(listed, settings)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the Legion settings: %w", err)
	}
	return listed, nil
}

// GetSettings is one project's setting, or ErrNoSettings when it has none.
func GetSettings(ctx context.Context, pool *store.Pool, project string) (Settings, error) {
	settings, err := scanSettings(pool.QueryRow(ctx, `select `+settingsColumns+` from legion_settings where project = $1`, project))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Settings{}, ErrNoSettings
		}
		return Settings{}, err
	}
	return settings, nil
}

// PutSettings sets the daemon a project is read from, and answers the row as it stands. created_by
// is the actor that first set the project's setting, as repo_projects.created_by is the actor that
// first mapped a repository: a later write changes the URL and the time and keeps it.
func PutSettings(ctx context.Context, pool *store.Pool, project, stateURL string, actor model.Actor) (Settings, error) {
	createdBy, err := json.Marshal(actor)
	if err != nil {
		return Settings{}, fmt.Errorf("encode the Legion setting's author: %w", err)
	}
	settings, err := scanSettings(pool.QueryRow(ctx, `
		insert into legion_settings (project, state_url, created_by, updated_at)
		values ($1, $2, $3, now())
		on conflict (project) do update set state_url = excluded.state_url, updated_at = now()
		returning `+settingsColumns, project, stateURL, createdBy))
	if err != nil {
		return Settings{}, fmt.Errorf("write the Legion setting of %s: %w", project, err)
	}
	return settings, nil
}

// DeleteSettings removes a project's setting and reports whether it had one. The project's reads
// and observations stay: they are what was observed, and nothing re-reads them.
func DeleteSettings(ctx context.Context, pool *store.Pool, project string) (bool, error) {
	tag, err := pool.Exec(ctx, `delete from legion_settings where project = $1`, project)
	if err != nil {
		return false, fmt.Errorf("delete the Legion setting of %s: %w", project, err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecordRead records a read of project's daemon that answered its state, in one transaction: every
// observation that moved (recordObservations), then the legion_reads row that says the read
// succeeded and how many bytes it sent. It answers how many observations it wrote.
//
// The board's header reads the newest legion_reads row as the last good read, and its columns
// read the newest observations, so the two commit together or not at all: a read whose
// observations Postgres refuses leaves no row saying it succeeded over data it did not store. The
// caller records such a read as failed (RecordFailedRead), and the last good read stands.
func RecordRead(ctx context.Context, pool *store.Pool, project string, at time.Time, bytes int, observations []Observation) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("open the transaction of the Legion state read of %s: %w", project, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	written, err := recordObservations(ctx, tx, project, at, observations)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, insertRead, project, at, true, nil, bytes); err != nil {
		return 0, fmt.Errorf("store the Legion state read of %s: %w", project, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit the Legion state read of %s: %w", project, err)
	}
	return written, nil
}

// RecordFailedRead appends the legion_reads row of a read of project's daemon that failed: how
// many bytes the daemon sent, and why the read failed, which the board's header names. It writes
// no observation, so the last good read's observations stand.
func RecordFailedRead(ctx context.Context, pool *store.Pool, project string, at time.Time, bytes int, failure error) error {
	if _, err := pool.Exec(ctx, insertRead, project, at, false, failure.Error(), bytes); err != nil {
		return fmt.Errorf("record the failed Legion state read of %s: %w", project, err)
	}
	return nil
}

// insertRead appends one legion_reads row: project, read_at, ok, error, bytes.
const insertRead = `
	insert into legion_reads (project, read_at, ok, error, bytes)
	values ($1, $2, $3, $4, $5)
`

// ReadRetention is how long legion_reads keeps a read of a daemon. The Legion board reads the
// newest row of a project and nothing reads an older one, so the rest are a week of history for
// whoever asks why a daemon stopped answering — the horizon delivery_changes keeps for the same
// reason, a day, measured against a table that gains a row a minute per project rather than one
// per change.
const ReadRetention = 7 * 24 * time.Hour

// PruneReads deletes every read older than ReadRetention that is not its project's newest, and
// answers how many it deleted: the table is append-only, one row per project per minute, and
// delivery.PruneChanges keeps delivery_changes the same way, from the loop that writes it.
//
// Each project keeps its newest read whatever its age, because that row is the freshness the
// board's header shows: a daemon nobody has read for a week reads as a stale read, never as a
// daemon that was never read. The cutoff is the database's own now(), as each row's read_at is
// the sampler's, so one clock decides whichever replica prunes.
func PruneReads(ctx context.Context, pool *store.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `
		delete from legion_reads old
		where old.read_at < now() - $1::interval
		  and exists (
		    select 1 from legion_reads newer
		    where newer.project = old.project and (newer.read_at, newer.id) > (old.read_at, old.id)
		  )
	`, ReadRetention)
	if err != nil {
		return 0, fmt.Errorf("prune the Legion state reads: %w", err)
	}
	return tag.RowsAffected(), nil
}

// recordObservations appends, inside the transaction of the read that found them, a row for each
// observation whose value differs from the newest row that project, issue and field already hold,
// and a `null` row for each issue and field whose newest row holds a value and that the read does
// not carry: an issue gone from the daemon's state, a role's claim gone from an issue, a tree the
// daemon stopped naming. It answers how many rows it wrote. An unchanged value writes nothing, and
// a first observation whose value is null writes nothing either: nothing moved from nothing.
//
// The `null` row is what makes the newest row of every field the newest good read's own: every
// reader that takes a field's newest row — the board's cards, the metrics' reported set — reads a
// field the read no longer carries as absent, and the record keeps when it left, which is where
// the metrics' observed timeline ends the issue's phase.
//
// One statement, so a pass of some hundreds of fields is one round trip, and Postgres's own jsonb
// comparison decides what changed: a Go encoding that ordered an object's keys differently from
// jsonb would otherwise write a row every minute for a value that never moved. A row the lease
// already let another replica write is therefore not written twice even if the leases overlapped.
func recordObservations(ctx context.Context, tx pgx.Tx, project string, at time.Time, observations []Observation) (int64, error) {
	issueKeys := make([]string, len(observations))
	fields := make([]string, len(observations))
	values := make([]string, len(observations))
	for index, observation := range observations {
		issueKeys[index], fields[index], values[index] = observation.IssueKey, observation.Field, string(observation.Value)
	}
	tag, err := tx.Exec(ctx, `
		with found as (
			select observed.issue_key, observed.field, observed.value::jsonb as value
			from unnest($2::text[], $3::text[], $4::text[]) as observed(issue_key, field, value)
		), newest as (
			select distinct on (issue_key, field) issue_key, field, value
			from legion_observations
			where project = $1
			order by issue_key, field, observed_at desc
		)
		insert into legion_observations (project, issue_key, field, value, observed_at)
		select $1, coalesce(found.issue_key, newest.issue_key), coalesce(found.field, newest.field),
			coalesce(found.value, 'null'::jsonb), $5::timestamptz
		from found
		full join newest on newest.issue_key = found.issue_key and newest.field = found.field
		where coalesce(newest.value, 'null'::jsonb) is distinct from coalesce(found.value, 'null'::jsonb)
	`, project, issueKeys, fields, values, at)
	if err != nil {
		return 0, fmt.Errorf("store the Legion observations of %s: %w", project, err)
	}
	return tag.RowsAffected(), nil
}
