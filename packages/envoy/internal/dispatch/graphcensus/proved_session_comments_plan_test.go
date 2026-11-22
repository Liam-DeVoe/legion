package graphcensus

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// provedSessionPlanRows is how many comments of other sessions the server proved, and as many of
// a person writing as herself, the plan test seeds: enough that a read of comments whole, or of
// the whole of an index over every proved session's comments, costs the planner more than a probe
// of an index's key.
const provedSessionPlanRows = 5000

// TestTheProvedSessionCommentsReadProbesThePersonsComments explains ProvedSessionComments, the read
// a person's erase makes under its locks of every comment that names her as the owner of the
// session the server proved that wrote it (grapherase's readBlockers), for dana. comments holds
// provedSessionPlanRows comments of other proved sessions, each owned by one of 500 other people,
// as many that alice wrote as herself, three of dana's proved session, one of them naming her in
// another case and with white space around her login, and one of a session whose act names
// another session, which the server did not prove, naming dana as its owner, all written and
// analyzed before the read. The read is held to answering dana's three. With hash and merge
// joins off, under each plan-cache mode the plan must read comments through
// comments_proved_session_owner, by its key (explaintest.RequireEachRowProbedOnce): the erase
// reads no whole large table while it holds its locks. The control is the same read with that
// index dropped inside the explaining transaction, as the database stood before 20261122010300,
// which must plan, run and fail the check under both modes: it reads comments whole.
func TestTheProvedSessionCommentsReadProbesThePersonsComments(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	if _, err := pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('PSC', 'Proved sessions', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank) values
			('PSC-1', 'PSC', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("seed issue PSC-1: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into comments (issue_key, author, body)
		select 'PSC-1', jsonb_build_object('kind', 'session', 'id', 'ses_' || i, 'owner', 'owner-' || (i % 500),
		                                   'act', jsonb_build_object('session', 'ses_' || i)),
		       'A proved session''s comment ' || i
		from generate_series(1, $1::int) i
	`, provedSessionPlanRows); err != nil {
		t.Fatalf("seed the comments of other proved sessions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into comments (issue_key, author, body)
		select 'PSC-1', '{"kind":"user","id":"alice"}'::jsonb, 'Alice''s comment ' || i
		from generate_series(1, $1::int) i
	`, provedSessionPlanRows); err != nil {
		t.Fatalf("seed alice's comments: %v", err)
	}
	var want []string
	for _, author := range []string{
		`{"kind":"session","id":"ses_dana","owner":"dana","act":{"session":"ses_dana"}}`,
		`{"kind":"session","id":"ses_dana","owner":"dana","act":{"session":"ses_dana"}}`,
		`{"kind":"session","id":"ses_dana","owner":" Dana\t","act":{"session":"ses_dana"}}`,
		`{"kind":"session","id":"ses_unproved","owner":"dana","act":{"session":"ses_other"}}`,
	} {
		var id string
		if err := pool.QueryRow(ctx, `
			insert into comments (issue_key, author, body) values ('PSC-1', $1::jsonb, 'A comment naming dana') returning id::text
		`, author).Scan(&id); err != nil {
			t.Fatalf("write a comment of %s: %v", author, err)
		}
		if len(want) < 3 {
			want = append(want, id)
		}
	}
	if _, err := pool.Exec(ctx, `analyze comments`); err != nil {
		t.Fatalf("analyze comments: %v", err)
	}
	rows, err := pool.Query(ctx, ProvedSessionComments, "dana")
	if err != nil {
		t.Fatalf("read the comments naming dana as their session's owner: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the comments naming dana as their session's owner: %v", err)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the read answers %q, want dana's proved session's three comments in id order, %q", got, want)
	}

	keys := explaintest.NewIndexKeys(pool)
	keyed := map[string]string{"comments": "comments_proved_session_owner"}
	read := explaintest.Statement{SQL: ProvedSessionComments, Args: []any{"dana"}}
	explaintest.RequireEachRowProbedOnce(t, ctx, pool, keys, keyed, "the read of the comments naming dana as their session's owner", nil, read)
	explaintest.RequireTheCheckFails(t, ctx, pool, keys, keyed, "the read of the comments naming dana with comments_proved_session_owner dropped",
		explaintest.PlanCacheModes, []explaintest.Statement{{SQL: "set local lock_timeout = '10s'"}, {SQL: "drop index comments_proved_session_owner"}}, read)
}
