package store

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// claimSessionsVersion is the version of the migration that made claim_sessions, so a test can
// migrate through everything before it whatever lands beside it.
const claimSessionsVersion = 20261116144907

// sessionHold is the hold of a session id.
func sessionHold(id string) supervise.Hold {
	return supervise.Hold{Kind: supervise.HoldSessionID, Name: id}
}

// holdOne holds the one name h for token, and answers the claim that holds it.
func holdOne(ctx context.Context, t *testing.T, s *Store, project string, token claim.Token, h supervise.Hold) claim.Token {
	t.Helper()
	holders, err := s.HoldSessions(ctx, project, token, []supervise.Hold{h})
	if err != nil || len(holders) != 1 {
		t.Fatalf("%s holding %s %s answers %v, %v", token, h.Kind, h.Name, holders, err)
	}
	return holders[0]
}

// Each name of a session is held for the first claim of its project that records it
// (dispatch://INFRA-840, plan E's t-e-54), and nothing that happens to that claim after frees it:
// two claims racing for the same new names are answered one holder, a call records all of its
// names or none, the hold outlives the claim's retirement and its row, and a name two claims
// recorded before the table existed is held for neither and named for an operator to settle. A hold
// is its project's, as a daemon serves one project: another project's claim of the same name is
// another daemon's, whose pods the broker keeps apart (exchange.PodLaunchPrincipal).
//
// The controls (the pull request records each run): a conflict that hands the name to its latest
// caller (`on conflict (project, kind, name) do update set claim = excluded.claim`) answers the
// second claim as the holder; a call that commits the names it could take answers a third claim
// as refused a name the second never held.
func TestTheFirstClaimHoldsASessionId(t *testing.T) {
	const first, second, third = claim.Token("legion-LEGION-209-implementer"), claim.Token("legion-LEGION-209-tester"), claim.Token("legion-LEGION-209-reviewer")
	const project = "legion"

	t.Run("the first claim to record a name holds it, and its own is answered again", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		for _, ask := range []struct {
			token, want claim.Token
		}{{first, first}, {second, first}, {first, first}} {
			if holder := holdOne(ctx, t, store, project, ask.token, sessionHold("ses_new")); holder != ask.want {
				t.Fatalf("%s holding ses_new answers %q; want %s, the first claim that recorded it", ask.token, holder, ask.want)
			}
		}
		file := supervise.Hold{Kind: supervise.HoldFileName, Name: "ses_new"}
		if holder := holdOne(ctx, t, store, project, second, file); holder != second {
			t.Fatalf("%s holding a file named like first's id answers %q; want %s: each kind of name is held apart", second, holder, second)
		}
		const elsewhere = claim.Token("other-OTHER-1-implementer")
		if holder := holdOne(ctx, t, store, "other", elsewhere, sessionHold("ses_new")); holder != elsewhere {
			t.Fatalf("another project's claim holding ses_new answers %q; want %s: a hold is its own project's", holder, elsewhere)
		}
	})

	t.Run("two claims racing for the same new names are answered one holder", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		for race := range 25 {
			names := []supervise.Hold{
				sessionHold(fmt.Sprintf("ses_raced_%d", race)),
				{Kind: supervise.HoldFileName, Name: fmt.Sprintf("2026_ses_raced_%d.jsonl", race)},
				{Kind: supervise.HoldFileID, Name: fmt.Sprintf("ses_raced_%d", race)},
			}
			// The two ask for the names in opposite orders: the store takes them in one order, so
			// the two wait on each other one way and neither deadlocks.
			reversed := slices.Clone(names)
			slices.Reverse(reversed)
			var wg sync.WaitGroup
			start := make(chan struct{})
			holders := make([][]claim.Token, 2)
			errs := make([]error, 2)
			for i, ask := range []struct {
				token claim.Token
				names []supervise.Hold
			}{{first, names}, {second, reversed}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					holders[i], errs[i] = store.HoldSessions(ctx, project, ask.token, ask.names)
				}()
			}
			close(start)
			wg.Wait()
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("race %d: the holds failed: %v, %v", race, errs[0], errs[1])
			}
			winner := holders[0][0]
			if winner != first && winner != second {
				t.Fatalf("race %d: %s was answered %s, want one of the two", race, names[0].Name, winner)
			}
			for i, h := range names {
				if holders[0][i] != winner || holders[1][len(names)-1-i] != winner {
					t.Fatalf("race %d: %s %s was answered %s and %s, want %s to both, the holder of every name",
						race, h.Kind, h.Name, holders[0][i], holders[1][len(names)-1-i], winner)
				}
			}
		}
	})

	t.Run("a call records all of its names or none", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		holdOne(ctx, t, store, project, first, sessionHold("ses_a"))
		holders, err := store.HoldSessions(ctx, project, second, []supervise.Hold{sessionHold("ses_b"), sessionHold("ses_a")})
		if err != nil || len(holders) != 2 || holders[1] != first {
			t.Fatalf("%s holding ses_b and first's ses_a answers %v, %v; want ses_a answered as %s's", second, holders, err, first)
		}
		if holder := holdOne(ctx, t, store, project, third, sessionHold("ses_b")); holder != third {
			t.Fatalf("ses_b is held for %q after a refused call named it; want it free, so %s takes it", holder, third)
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
		if holder := holdOne(ctx, t, store, project, first, sessionHold("ses_retired")); holder != first {
			t.Fatalf("the claim holding its session answers %q", holder)
		}
		retired.State, retired.Locator, retired.CapabilityHash = supervise.StateRetired, nil, nil
		if err := store.PutClaim(ctx, retired); err != nil {
			t.Fatalf("write the retirement: %v", err)
		}
		if holder := holdOne(ctx, t, store, project, second, sessionHold("ses_retired")); holder != first {
			t.Fatalf("another claim holding the retired claim's session answers %q; want %s", holder, first)
		}
		if _, err := store.Pool().Exec(ctx, `delete from claims where token = $1`, string(first)); err != nil {
			t.Fatalf("delete the claim's row: %v", err)
		}
		if holder := holdOne(ctx, t, store, project, second, sessionHold("ses_retired")); holder != first {
			t.Fatalf("another claim holding a deleted claim's session answers %q; want %s", holder, first)
		}
	})

	// The migration's backfill is SQL, and the rule a registration holds by is Go
	// (supervise.RecordedHolds): the rows below are what each answers for the same claims, every
	// kind of name and its edges among them, and the two must be one answer.
	t.Run("the migration holds each recorded name as RecordedHolds does, and one two claims record for none", func(t *testing.T) {
		ctx := context.Background()
		store := emptyStore(t)
		migrateThrough(t, store, claimSessionsVersion-1)
		type recorded struct{ session, file string }
		rows := []struct {
			token, project string
			now, abandoned recorded
		}{
			{"legion-LEGION-301-implementer", "legion", recorded{"ses_own", "/state/sessions/--w--/2026-11-16T00-00-00-000Z_ses_own.jsonl"}, recorded{}},
			{"legion-LEGION-301-tester", "legion", recorded{"ses_shared", "/state/sessions/a/2026-11-16T01_ses_shared.jsonl"}, recorded{}},
			{"legion-LEGION-302-implementer", "legion", recorded{"ses_shared", "/state/sessions/b/2026-11-16T02_ses_shared.jsonl"}, recorded{}},
			{"legion-LEGION-303-implementer", "legion", recorded{}, recorded{"ses_abandoned", "/claude/projects/-w-/11111111-2222-4333-8444-555555555555.jsonl"}},
			{"legion-LEGION-304-implementer", "legion", recorded{"ses_taken", "/p/one/same.jsonl"}, recorded{}},
			{"legion-LEGION-305-implementer", "legion", recorded{}, recorded{"ses_taken", "/p/two/same.jsonl"}},
			{"legion-LEGION-306-implementer", "legion", recorded{}, recorded{}},
			{"legion-LEGION-307-implementer", "legion", recorded{"ses_307", "/sessions/w/stamp_.jsonl"}, recorded{}},
			{"legion-LEGION-308-implementer", "legion", recorded{"ses_308", "/sessions/w/a_b_c.jsonl"}, recorded{"", "/trailing/dir/"}},
			{"legion-LEGION-309-implementer", "legion", recorded{"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "/c/projects/-w-/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee.jsonl"}, recorded{}},
			{"other-OTHER-301-implementer", "other", recorded{"ses_own", "/state/sessions/--w--/2026-11-16T00-00-00-000Z_ses_own.jsonl"}, recorded{}},
		}
		type key struct {
			project string
			hold    supervise.Hold
		}
		recorders := map[key][]claim.Token{}
		for _, row := range rows {
			var abandoned any
			if row.abandoned != (recorded{}) {
				encoded, err := json.Marshal(map[string]string{
					"session": row.abandoned.session, "sessionFile": row.abandoned.file,
					"why": "a retry asked for a new session", "at": "2026-11-16T00:00:00Z",
				})
				if err != nil {
					t.Fatal(err)
				}
				abandoned = string(encoded)
			}
			if _, err := store.Pool().Exec(ctx, `insert into claims (token, project, tree, issue, role, generation,
				session, session_file, state, failure, launch_failures, prompt_failures, prompt_retires, uncertain_streak,
				abandoned_session)
				values ($1, $2, 'LEGION-301', 'LEGION-301', 'implementer', 1, $3, $4, 'retired', '', 0, 0, 0, 0, $5::jsonb)`,
				row.token, row.project, row.now.session, row.now.file, abandoned); err != nil {
				t.Fatalf("record %s before the migration: %v", row.token, err)
			}
			for _, h := range append(supervise.RecordedHolds(row.now.session, row.now.file), supervise.RecordedHolds(row.abandoned.session, row.abandoned.file)...) {
				k := key{row.project, h}
				if !slices.Contains(recorders[k], claim.Token(row.token)) {
					recorders[k] = append(recorders[k], claim.Token(row.token))
				}
			}
		}
		want := map[key]claim.Token{}
		for k, tokens := range recorders {
			if len(tokens) == 1 {
				want[k] = tokens[0]
			} else {
				want[k] = ""
			}
		}

		if _, err := store.Migrate(ctx); err != nil {
			t.Fatalf("migrate through the session holds: %v", err)
		}

		held := map[key]claim.Token{}
		read, err := store.Pool().Query(ctx, `select project, kind, name, claim from claim_sessions`)
		if err != nil {
			t.Fatalf("read the holds: %v", err)
		}
		for read.Next() {
			var project, kind, name, holder string
			if err := read.Scan(&project, &kind, &name, &holder); err != nil {
				t.Fatalf("scan a hold: %v", err)
			}
			held[key{project, supervise.Hold{Kind: supervise.HoldKind(kind), Name: name}}] = claim.Token(holder)
		}
		if err := read.Err(); err != nil {
			t.Fatalf("read the holds: %v", err)
		}
		for k, holder := range want {
			if got, ok := held[k]; !ok || got != holder {
				t.Errorf("the migration holds %s %s %s for %q (present %t); RecordedHolds says %q", k.project, k.hold.Kind, k.hold.Name, got, ok, holder)
			}
		}
		for k, holder := range held {
			if _, ok := want[k]; !ok {
				t.Errorf("the migration holds %s %s %s for %q, which RecordedHolds names for no claim", k.project, k.hold.Kind, k.hold.Name, holder)
			}
		}
		// The edges RecordedHolds and the SQL must agree on, named so a change to both that drops
		// one is still caught.
		for _, k := range []key{
			{"legion", supervise.Hold{Kind: supervise.HoldFileID, Name: "ses_shared"}},
			{"legion", supervise.Hold{Kind: supervise.HoldFileName, Name: "same.jsonl"}},
			{"legion", supervise.Hold{Kind: supervise.HoldFileID, Name: "b_c"}},
			{"legion", supervise.Hold{Kind: supervise.HoldFileName, Name: "dir"}},
			{"legion", sessionHold("11111111-2222-4333-8444-555555555555")},
		} {
			if _, ok := want[k]; !ok {
				t.Errorf("RecordedHolds names no %s %s, which the rows above record", k.hold.Kind, k.hold.Name)
			}
		}

		for _, token := range []claim.Token{"legion-LEGION-301-tester", "legion-LEGION-302-implementer"} {
			if holder := holdOne(ctx, t, store, "legion", token, sessionHold("ses_shared")); holder != "" {
				t.Errorf("%s holding the id two claims record answers %q; want the empty holder no claim matches", token, holder)
			}
		}

		conflicts, err := store.SessionConflicts(ctx, "legion")
		if err != nil {
			t.Fatalf("read the session conflicts: %v", err)
		}
		var wantConflicts []SessionConflict
		for k, holder := range want {
			if k.project == "legion" && holder == "" {
				tokens := slices.Clone(recorders[k])
				slices.Sort(tokens)
				wantConflicts = append(wantConflicts, SessionConflict{Hold: k.hold, Claims: tokens})
			}
		}
		slices.SortFunc(wantConflicts, func(a, b SessionConflict) int {
			return cmp.Or(cmp.Compare(a.Hold.Kind, b.Hold.Kind), cmp.Compare(a.Hold.Name, b.Hold.Name))
		})
		if len(wantConflicts) != 4 {
			t.Fatalf("the rows above give %d names held for none, want 4: %+v", len(wantConflicts), wantConflicts)
		}
		sameConflicts := func(a, b SessionConflict) bool {
			return a.Hold == b.Hold && a.Holder == b.Holder && slices.Equal(a.Claims, b.Claims)
		}
		if !slices.EqualFunc(conflicts, wantConflicts, sameConflicts) {
			t.Errorf("the session conflicts read %+v, want %+v", conflicts, wantConflicts)
		}

		// A daemon from before the hold, as one rolled back is, records names no row holds: one
		// another claim holds already, and one no row holds. The boot's backfill holds the second
		// for its claim and leaves the first as it is, and the conflict read names the claim that
		// took the first.
		if _, err := store.Pool().Exec(ctx, `update claims set session = 'ses_own', session_file = '/rolled/back/took.jsonl'
			where token = 'legion-LEGION-306-implementer'`); err != nil {
			t.Fatalf("record what a rolled-back daemon let a claim register: %v", err)
		}
		added, err := store.HoldRecordedSessions(ctx, "legion")
		if err != nil {
			t.Fatalf("hold the recorded names: %v", err)
		}
		if added != 1 {
			t.Errorf("the boot's backfill held %d names, want 1: the rolled-back claim's file name", added)
		}
		if holder := holdOne(ctx, t, store, "legion", "legion-LEGION-399-implementer", supervise.Hold{Kind: supervise.HoldFileName, Name: "took.jsonl"}); holder != "legion-LEGION-306-implementer" {
			t.Errorf("the rolled-back claim's file is held for %q after the backfill, want its own claim", holder)
		}
		if again, err := store.HoldRecordedSessions(ctx, "legion"); err != nil || again != 0 {
			t.Errorf("the backfill run again held %d names, %v; want none: every recorded name is held", again, err)
		}
		conflicts, err = store.SessionConflicts(ctx, "legion")
		if err != nil {
			t.Fatalf("read the session conflicts: %v", err)
		}
		took := SessionConflict{Hold: sessionHold("ses_own"), Holder: "legion-LEGION-301-implementer", Claims: []claim.Token{"legion-LEGION-306-implementer"}}
		if !slices.ContainsFunc(conflicts, func(c SessionConflict) bool { return sameConflicts(c, took) }) || len(conflicts) != len(wantConflicts)+1 {
			t.Errorf("the session conflicts read %+v, want the four held for none and %+v", conflicts, took)
		}
	})
}
