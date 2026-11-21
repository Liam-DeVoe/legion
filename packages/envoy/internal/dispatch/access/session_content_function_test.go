package access

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestTheContentFunctionIsTheBodyGoRenders: session_content_visible, the function a migrated
// database holds, is sessionContentFunctionBody as it renders now, word for word (whitespace aside).
// The rule is written once, in Go beside PersonSeesProjectSQL, which it reads; a change of either
// that ships without a migration replacing the function fails here rather than leaving the
// database answering the old rule. The function sets nothing for its calls (no SET clause), as
// session_clause_visible does not, and is declared at the cost that keeps statements on their
// indexes.
//
// Control: a word of the rendered body changed (the open viewer's tie test dropped), and the test
// names the difference.
func TestTheContentFunctionIsTheBodyGoRenders(t *testing.T) {
	database := storetest.Open(t)
	var source string
	var settings []string
	var cost float32 // pg_proc.procost is a float4
	if err := database.Pool.QueryRow(context.Background(), `
		select prosrc, coalesce(proconfig, '{}'), procost from pg_proc where proname = 'session_content_visible'`).Scan(&source, &settings, &cost); err != nil {
		t.Fatalf("read session_content_visible: %v", err)
	}
	words := func(text string) string { return strings.Join(strings.Fields(text), " ") }
	if held, rendered := words(source), words(sessionContentFunctionBody()); held != rendered {
		t.Errorf("session_content_visible's body differs from sessionContentFunctionBody: a migration must replace the function\nheld:     %s\nrendered: %s", held, rendered)
	}
	if len(settings) > 0 {
		t.Errorf("session_content_visible sets %v on each of its calls, want none", settings)
	}
	if cost != float32(0.0001) {
		t.Errorf("session_content_visible is declared at cost %v, want 0.0001", cost)
	}
}

// TestTheRuleFunctionsReadTheirTablesWhateverTheCallersSearchPath: session_content_visible and
// session_clause_visible set no search_path, so each runs under its caller's, and each names every
// table it reads by its schema, so a caller whose search_path puts another schema first, holding
// tables of the same names, is answered from public's rows all the same. Schema shadow holds a
// session_projects that places ses_shadowed in PRIV and a session_owners that ties it to bob alone;
// public holds neither row. bob, a restricted member PRIV names, reads ses_shadowed by neither rule
// under the default search_path, and by neither under `shadow, public`.
//
// Control: session_content_visible's tables named without their schema (the migration's first
// text), and under `shadow, public` bob reads ses_shadowed's content.
func TestTheRuleFunctionsReadTheirTablesWhateverTheCallersSearchPath(t *testing.T) {
	ctx := context.Background()
	database := seedAccess(t)
	if _, err := database.Pool.Exec(ctx, `
		insert into session_projects_filled default values;
		create schema shadow;
		create table shadow.session_projects (session_id text, project_key text);
		create table shadow.session_owners (session_id text, login text);
		insert into shadow.session_projects values ('ses_shadowed', 'PRIV');
		insert into shadow.session_owners values ('ses_shadowed', '`+bob+`');
	`); err != nil {
		t.Fatalf("seed the shadow schema: %v", err)
	}
	viewer, err := Resolve(ctx, database.Pool, cookie(bob))
	if err != nil {
		t.Fatalf("bob: Resolve: %v", err)
	}
	if viewer.Open || !viewer.AnyPrivate {
		t.Fatalf("bob resolves as %+v, want a restricted member of a deployment that holds a private project", viewer)
	}
	connection, err := pgx.ConnectConfig(ctx, database.Pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatalf("open a connection of its own: %v", err)
	}
	defer connection.Close(ctx)
	for _, path := range []string{"public", "shadow, public"} {
		if _, err := connection.Exec(ctx, `set search_path = `+path); err != nil {
			t.Fatalf("set search_path = %s: %v", path, err)
		}
		for name, rule := range map[string]string{
			"SessionClause":        SessionClause("$2", "$1"),
			"SessionContentClause": SessionContentClause("$2", "$1"),
		} {
			var passes bool
			if err := connection.QueryRow(ctx, `select `+rule, viewerArg(t, viewer), "ses_shadowed").Scan(&passes); err != nil {
				t.Fatalf("search_path %s: %s: %v", path, name, err)
			}
			if passes {
				t.Errorf("search_path %s: %s passes ses_shadowed for bob, want false: public holds no row placing or tying it", path, name)
			}
		}
	}
}

// TestTheContentRuleAsksItsFunctionOncePerSessionAndNeverUnderR9: SessionContentClause asks
// session_content_visible about every row its own SQL does not answer. For a read over fifty
// sessions, by an open viewer and by one that is not open, of a deployment that holds a private
// project, it calls the function once a session (functionCalls, auto_explain's plans of nested
// statements). With no private project an open viewer's read calls it not at all and passes every
// session: SessionContentClause's own `open and not private` arm answers it (R9), as it answers
// every party of every conversation PartyClause reads.
//
// Control: that arm taken out of SessionContentClause's own SQL, and the read with no private
// project makes fifty calls, which the test names.
func TestTheContentRuleAsksItsFunctionOncePerSessionAndNeverUnderR9(t *testing.T) {
	ctx := context.Background()
	database := seedAccess(t)
	if _, err := database.Pool.Exec(ctx, `insert into session_projects_filled default values`); err != nil {
		t.Fatalf("fill session_projects: %v", err)
	}
	read := `select count(*) from generate_series(1, 50) as n where ` + SessionContentClause("'ses_' || n", "$1")
	resolve := func(person string) model.Viewer {
		t.Helper()
		viewer, err := Resolve(ctx, database.Pool, cookie(person))
		if err != nil {
			t.Fatalf("%s: Resolve: %v", person, err)
		}
		return viewer
	}
	for _, person := range []string{alice, bob} {
		viewer := resolve(person)
		if !viewer.AnyPrivate {
			t.Fatalf("%s resolves as %+v, want a deployment that holds a private project", person, viewer)
		}
		if calls := functionCalls(t, database, "session_owners", read, viewerArg(t, viewer)); len(calls) != 50 {
			t.Errorf("%s's read over fifty sessions made %d calls of session_content_visible, want one a session", person, len(calls))
		}
	}
	if _, err := database.Pool.Exec(ctx, `update projects set visibility = 'public'`); err != nil {
		t.Fatalf("make every project public: %v", err)
	}
	viewer := resolve(alice)
	if viewer.AnyPrivate || !viewer.Open {
		t.Fatalf("alice resolves as %+v with every project public, want an open viewer of no private project", viewer)
	}
	if calls := functionCalls(t, database, "session_owners", read, viewerArg(t, viewer)); len(calls) != 0 {
		t.Errorf("with no private project, alice's read over fifty sessions made %d calls of session_content_visible, want none (R9)", len(calls))
	}
	var passed int
	if err := database.Pool.QueryRow(ctx, read, viewerArg(t, viewer)).Scan(&passed); err != nil {
		t.Fatalf("count the sessions alice reads: %v", err)
	}
	if passed != 50 {
		t.Errorf("with no private project alice reads the content of %d of fifty sessions nobody tied or listed, want every one (R9)", passed)
	}
}
