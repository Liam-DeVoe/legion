package store

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// claimSessionsVersion is the version of the migration that made claim_sessions, so a test can
// migrate through everything before it whatever lands beside it.
const claimSessionsVersion = 20261116144907

// Each session id is held for the first claim of its project that records it (dispatch://INFRA-840,
// plan E's t-e-54), and nothing that happens to that claim after frees it: two claims racing for
// one new id are answered one holder, the hold outlives the claim's retirement and its row, and an
// id two claims recorded before the table existed is held for neither and named for an operator to
// settle. A hold is its project's, as a daemon serves one project: another project's claim of the
// same id is another daemon's, whose pods the broker keeps apart (exchange.PodLaunchPrincipal).
//
// The control: a conflict that hands the id to its latest caller (`on conflict (project, session)
// do update set claim = excluded.claim`) answers the second claim as the holder, and the first row
// fails naming it (the pull request records the run).
func TestTheFirstClaimHoldsASessionId(t *testing.T) {
	const first, second = claim.Token("legion-LEGION-209-implementer"), claim.Token("legion-LEGION-209-tester")
	const project = "legion"

	t.Run("the first claim to record an id holds it, and its own is answered again", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		for _, ask := range []struct {
			token, want claim.Token
		}{{first, first}, {second, first}, {first, first}} {
			holder, err := store.HoldSession(ctx, project, ask.token, "ses_new")
			if err != nil || holder != ask.want {
				t.Fatalf("%s holding ses_new answers %q, %v; want %s, the first claim that recorded it", ask.token, holder, err, ask.want)
			}
		}
		const elsewhere = claim.Token("other-OTHER-1-implementer")
		if holder, err := store.HoldSession(ctx, "other", elsewhere, "ses_new"); err != nil || holder != elsewhere {
			t.Fatalf("another project's claim holding ses_new answers %q, %v; want %s: a hold is its own project's", holder, err, elsewhere)
		}
	})

	t.Run("two claims racing for one new id are answered one holder", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		for race := range 25 {
			session := "ses_raced_" + string(rune('a'+race))
			var wg sync.WaitGroup
			start := make(chan struct{})
			holders := make([]claim.Token, 2)
			errs := make([]error, 2)
			for i, token := range []claim.Token{first, second} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					holders[i], errs[i] = store.HoldSession(ctx, project, token, session)
				}()
			}
			close(start)
			wg.Wait()
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("race %d: the holds failed: %v, %v", race, errs[0], errs[1])
			}
			if holders[0] != holders[1] || (holders[0] != first && holders[0] != second) {
				t.Fatalf("race %d: the two claims were answered %s and %s, want one of them, the same to both", race, holders[0], holders[1])
			}
		}
	})

	t.Run("a hold survives its claim's retirement and its row", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		retired := tmuxClaim(first)
		retired.Session = "ses_retired"
		if err := store.PutClaim(ctx, retired); err != nil {
			t.Fatalf("put the claim: %v", err)
		}
		if holder, err := store.HoldSession(ctx, project, first, "ses_retired"); err != nil || holder != first {
			t.Fatalf("the claim holding its session answers %q, %v", holder, err)
		}
		retired.State, retired.Locator, retired.CapabilityHash = supervise.StateRetired, nil, nil
		if err := store.PutClaim(ctx, retired); err != nil {
			t.Fatalf("write the retirement: %v", err)
		}
		if holder, err := store.HoldSession(ctx, project, second, "ses_retired"); err != nil || holder != first {
			t.Fatalf("another claim holding the retired claim's session answers %q, %v; want %s", holder, err, first)
		}
		if _, err := store.Pool().Exec(ctx, `delete from claims where token = $1`, string(first)); err != nil {
			t.Fatalf("delete the claim's row: %v", err)
		}
		if holder, err := store.HoldSession(ctx, project, second, "ses_retired"); err != nil || holder != first {
			t.Fatalf("another claim holding a deleted claim's session answers %q, %v; want %s", holder, err, first)
		}
	})

	t.Run("the migration holds each recorded id for its claim, and an id two claims record for none", func(t *testing.T) {
		ctx := context.Background()
		store := emptyStore(t)
		migrateThrough(t, store, claimSessionsVersion-1)
		for _, row := range []struct {
			token, project, session, abandoned string
		}{
			{"legion-LEGION-301-implementer", "legion", "ses_own", ""},
			{"legion-LEGION-301-tester", "legion", "ses_shared", ""},
			{"legion-LEGION-302-implementer", "legion", "ses_shared", ""},
			{"legion-LEGION-303-implementer", "legion", "", "ses_abandoned"},
			{"legion-LEGION-304-implementer", "legion", "ses_taken", ""},
			{"legion-LEGION-305-implementer", "legion", "", "ses_taken"},
			{"legion-LEGION-306-implementer", "legion", "", ""},
			{"other-OTHER-301-implementer", "other", "ses_own", ""},
		} {
			var abandoned any
			if row.abandoned != "" {
				abandoned = `{"session":"` + row.abandoned + `","why":"a retry asked for a new session","at":"2026-11-16T00:00:00Z"}`
			}
			if _, err := store.Pool().Exec(ctx, `insert into claims (token, project, tree, issue, role, generation,
				session, session_file, state, failure, launch_failures, prompt_failures, prompt_retires, uncertain_streak,
				abandoned_session)
				values ($1, $2, 'LEGION-301', 'LEGION-301', 'implementer', 1, $3, '', 'retired', '', 0, 0, 0, 0, $4::jsonb)`,
				row.token, row.project, row.session, abandoned); err != nil {
				t.Fatalf("record %s before the migration: %v", row.token, err)
			}
		}

		if _, err := store.Migrate(ctx); err != nil {
			t.Fatalf("migrate through the session holds: %v", err)
		}

		held := map[string]string{}
		rows, err := store.Pool().Query(ctx, `select project || '/' || session, claim from claim_sessions`)
		if err != nil {
			t.Fatalf("read the holds: %v", err)
		}
		for rows.Next() {
			var session, holder string
			if err := rows.Scan(&session, &holder); err != nil {
				t.Fatalf("scan a hold: %v", err)
			}
			held[session] = holder
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the holds: %v", err)
		}
		want := map[string]string{
			"legion/ses_own":       "legion-LEGION-301-implementer",
			"legion/ses_shared":    "",
			"legion/ses_abandoned": "legion-LEGION-303-implementer",
			"legion/ses_taken":     "",
			"other/ses_own":        "other-OTHER-301-implementer",
		}
		if len(held) != len(want) {
			t.Errorf("the migration holds %v, want %v", held, want)
		}
		for session, holder := range want {
			if got, ok := held[session]; !ok || got != holder {
				t.Errorf("the migration holds %s for %q (present %t), want %q", session, got, ok, holder)
			}
		}

		for _, token := range []claim.Token{"legion-LEGION-301-tester", "legion-LEGION-302-implementer"} {
			if holder, err := store.HoldSession(ctx, "legion", token, "ses_shared"); err != nil || holder != "" {
				t.Errorf("%s holding the id two claims record answers %q, %v; want the empty holder no claim matches", token, holder, err)
			}
		}

		none, err := store.SessionsHeldForNone(ctx, "legion")
		if err != nil {
			t.Fatalf("read the sessions held for none: %v", err)
		}
		wantNone := []HeldForNone{
			{Session: "ses_shared", Claims: []claim.Token{"legion-LEGION-301-tester", "legion-LEGION-302-implementer"}},
			{Session: "ses_taken", Claims: []claim.Token{"legion-LEGION-304-implementer", "legion-LEGION-305-implementer"}},
		}
		if !slices.EqualFunc(none, wantNone, func(a, b HeldForNone) bool {
			return a.Session == b.Session && slices.Equal(a.Claims, b.Claims)
		}) {
			t.Errorf("the sessions held for none read %+v, want %+v", none, wantNone)
		}
	})
}
