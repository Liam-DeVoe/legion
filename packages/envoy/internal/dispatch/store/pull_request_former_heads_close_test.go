package store

import (
	"context"
	"slices"
	"testing"
)

// Commits of the test below: a commit recorded at while #70 was closed, and a head #70 visits
// after it reopens.
const (
	closedPeriodH4 = "4444444444444444444444444444444444444444"
	reopenedH7     = "7777777777777777777777777777777777777777"
)

// TestEveryCloseEndsTheLifecycleItClosesWhereverDispatchFirstSawIt: CORE-1's #70 visits 1111…,
// where a tester records a result, and then closes, and the close reaches Dispatch in no update
// of an open row: the install runs over #70's row already closed, or Dispatch first writes #70's
// row closed. A result recorded at 4444… while #70 is closed belongs to no ended lifecycle. #70
// then reopens at 3333… and moves on to 7777…, and CORE-1's settle drains what is owed. The
// result at 1111… is of the lifecycle the close ended, so it is no candidate of the reopened
// pull request, however healthy a read of its commits would be; the closed period's 4444… is a
// candidate, and 3333…, a head the reopened #70 truly visited, its former head. The neighbours
// keep #70 open at the install: its result at 1111… from before the install is a candidate, as
// the install's seed has it, since no close of #70 is known, and so it is where a row of #70
// stored under its repository's old name reads closed, a stale copy the canonical row outdates.
func TestEveryCloseEndsTheLifecycleItClosesWhereverDispatchFirstSawIt(t *testing.T) {
	ctx := context.Background()
	for _, row := range []struct {
		name string
		// closedAtInstall closes #70 before the install; firstWrittenClosed deletes #70's row
		// before the install and writes it again, closed, after it, as Dispatch writes a pull
		// request it first reads closed; staleOldName stores a closed row of #70 under the
		// repository's old name beside its open canonical row before the install.
		closedAtInstall, firstWrittenClosed, staleOldName bool
		want                                              []string
	}{
		{name: "the pull request is open at the install", want: []string{
			"acme/widgets#70@1111 unverified", "acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "the pull request is open at the install and its old name's row reads closed", staleOldName: true, want: []string{
			"acme/widgets#70@1111 unverified", "acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "the install runs over the pull request closed", closedAtInstall: true, want: []string{
			"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "Dispatch first writes the pull request closed", firstWrittenClosed: true, want: []string{
			"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := openEmptyTestStore(t)
			documents := installFixture(t, store)
			exec := func(what, sql string, args ...any) {
				t.Helper()
				if _, err := store.Pool.Exec(ctx, sql, args...); err != nil {
					t.Fatalf("%s: %v", what, err)
				}
			}
			exec("record a result at 1111…", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], installH1)
			switch {
			case row.closedAtInstall:
				exec("close #70 at 2222…", `update pull_request_heads set state = 'closed', head_sha = $1 where number = 70`, installH2)
			case row.firstWrittenClosed:
				exec("forget #70's row", `delete from pull_request_heads where number = 70`)
			case row.staleOldName:
				exec("record the repository's old name", `
					insert into repo_projects (repo, project, created_by, is_default, renamed_to)
					values ('acme/old-widgets', 'CORE', '{"kind":"system","id":"store-test"}', false, 'acme/widgets')`)
				exec("store #70 closed under the old name", `
					insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ('acme/old-widgets', 70, $1, 'closed', 'CORE-1')`, installH2)
			}
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if row.firstWrittenClosed {
				exec("write #70's row closed at 2222…", `
					insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ('acme/widgets', 70, $1, 'closed', 'CORE-1')`, installH2)
			}
			if row.closedAtInstall || row.firstWrittenClosed {
				exec("record a result at 4444… while #70 is closed", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
				exec("reopen #70 at 3333…", `update pull_request_heads set state = 'open', head_sha = $1 where repo = 'acme/widgets' and number = 70`, installH3)
			} else {
				exec("record a result at 4444…", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
			}
			exec("move #70 on to 7777…", `update pull_request_heads set head_sha = $1 where repo = 'acme/widgets' and number = 70`, reopenedH7)
			drainCandidates(t, store, "CORE-1")
			heads, _ := formerHeadsAndOwed(t, store)
			if !slices.Equal(heads, row.want) {
				t.Fatalf("after the reopen #70 holds the former heads %v, want %v: a close ends the lifecycle it closes wherever Dispatch first saw it, and a result recorded before it is of that ended lifecycle", heads, row.want)
			}
			if row.closedAtInstall || row.firstWrittenClosed {
				var ended *int64
				var generation int
				if err := store.Pool.QueryRow(ctx, `
					select ended, generation from pull_request_former_heads_pulls where pull = pull_request_former_heads_pull_of('acme/widgets', 70)`).Scan(&ended, &generation); err != nil {
					t.Fatalf("read #70's lifecycle: %v", err)
				}
				if ended == nil || generation < 2 {
					t.Fatalf("#70's lifecycle reads ended=%v generation=%d after its close and reopen, want an end and a later generation", ended, generation)
				}
			}
		})
	}
}

// TestAResultRecordedWhileAPullRequestIsClosedCountsAfterItReopensOnEitherSideOfTheInstall:
// CORE-1's #70 stands at 4444… before the install, where its row is written, and a tester records
// a result at 1111… while #70 is open. #70 then closes at 4444…, its row written closed, which
// Dispatch dates on its own clock when the row first reads closed (pull_request_heads.closed_at),
// and a result is recorded at 4444… while #70 is closed: before the install, or after it. #70
// then reopens at 3333… and moves on to 7777…, and CORE-1's settle drains what is owed. Wherever
// the install falls, the result at 1111… is of the lifecycle the close ended and stays out, and
// the result at 4444… is of the closed period and makes 4444… a candidate of the reopened pull
// request, as a result recorded after the close does in Dispatch's own records: the install places
// a result it finds and the close of a pull request closed when it runs in the order of Dispatch's
// own times, not every result before the close. Each schedule records the closed-period result in
// either result table.
func TestAResultRecordedWhileAPullRequestIsClosedCountsAfterItReopensOnEitherSideOfTheInstall(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"check_results", "ci_check_results"} {
		for _, beforeInstall := range []bool{true, false} {
			name := table + ", the closed-period result recorded after the install"
			if beforeInstall {
				name = table + ", the closed-period result recorded before the install"
			}
			t.Run(name, func(t *testing.T) {
				store := openEmptyTestStore(t)
				documents := installFixture(t, store)
				exec := func(what, sql string, args ...any) {
					t.Helper()
					if _, err := store.Pool.Exec(ctx, sql, args...); err != nil {
						t.Fatalf("%s: %v", what, err)
					}
				}
				exec("write #70 at 4444…", `update pull_request_heads set head_sha = $1, event_at = clock_timestamp() where number = 70`, closedPeriodH4)
				exec("record a result at 1111… while #70 is open", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], installH1)
				exec("close #70 at 4444…", `update pull_request_heads set state = 'closed', event_at = clock_timestamp() where number = 70`)
				var head, state string
				if err := store.Pool.QueryRow(ctx, `select head_sha, state from pull_request_heads where number = 70`).Scan(&head, &state); err != nil {
					t.Fatalf("read #70's row: %v", err)
				}
				if head != closedPeriodH4 || state != "closed" {
					t.Fatalf("#70's row reads %s %s before the closed-period result, want 4444… closed: this test holds no closed period", head, state)
				}
				closedPeriod := func() {
					t.Helper()
					exec("record a result at 4444… while #70 is closed", installResult(table), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
				}
				if beforeInstall {
					closedPeriod()
				}
				if err := store.Migrate(ctx); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				if !beforeInstall {
					closedPeriod()
				}
				exec("reopen #70 at 3333…", `update pull_request_heads set state = 'open', head_sha = $1, event_at = clock_timestamp() where number = 70`, installH3)
				exec("move #70 on to 7777…", `update pull_request_heads set head_sha = $1, event_at = clock_timestamp() where number = 70`, reopenedH7)
				drainCandidates(t, store, "CORE-1")
				heads, _ := formerHeadsAndOwed(t, store)
				if want := []string{"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}; !slices.Equal(heads, want) {
					t.Fatalf("after the reopen #70 holds the former heads %v, want %v: a result recorded while #70 was closed is a candidate of the reopened pull request whichever side of the install it was recorded on, and the one recorded before the close stays out", heads, want)
				}
			})
		}
	}
}

// installCloseStory is the start of the tests below: CORE-1's #70 written at 4444…, open, a
// result recorded at openAt while #70 is open, in table, and then the statement closing, of
// pull_request_heads, closes #70. It answers exec, which runs a statement on the store or fails
// the test.
func installCloseStory(t *testing.T, store *Store, documents map[string]string, table, openAt, closing string) func(what, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	exec := func(what, sql string, args ...any) {
		t.Helper()
		if _, err := store.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	exec("write #70 at 4444…", `update pull_request_heads set head_sha = $1, event_at = clock_timestamp() where number = 70`, closedPeriodH4)
	exec("record a result while #70 is open", installResult(table), "CORE-1", "CORE-1", documents["CORE-1"], openAt)
	exec("close #70 at 4444…", closing)
	var state string
	if err := store.Pool.QueryRow(ctx, `select state from pull_request_heads where number = 70`).Scan(&state); err != nil || state != "closed" {
		t.Fatalf("#70's row reads %q (%v) after the close, want closed: this test holds no close", state, err)
	}
	return exec
}

// reopenedFormerHeads reopens #70 at 3333…, moves it on to 7777…, drains CORE-1, and answers
// #70's former heads.
func reopenedFormerHeads(t *testing.T, store *Store, exec func(what, sql string, args ...any)) []string {
	t.Helper()
	exec("reopen #70 at 3333…", `update pull_request_heads set state = 'open', head_sha = $1, event_at = clock_timestamp() where number = 70`, installH3)
	exec("move #70 on to 7777…", `update pull_request_heads set head_sha = $1, event_at = clock_timestamp() where number = 70`, reopenedH7)
	drainCandidates(t, store, "CORE-1")
	heads, _ := formerHeadsAndOwed(t, store)
	return heads
}

// TestAResultRecordedBeforeTheCloseStaysOutWhateverTimeGitHubGaveTheClose: CORE-1's tester
// records a result at 4444… while #70 is open there, and #70 then closes, its row written closed
// with a time GitHub gave that is earlier than the result's: GitHub's clock behind Dispatch's, or
// a closing write that names no time and keeps the earlier one its open row was dated with. The
// install then runs, and #70 reopens at 3333… and moves on to 7777…. The result at 4444… is of
// the lifecycle the close ended, so it is no candidate of the reopened pull request: the install
// orders a result and a close by Dispatch's own clock alone, when it recorded the result and when
// it first stored the row closed, never by a time GitHub gave. Each close is tried with the result
// in either result table.
func TestAResultRecordedBeforeTheCloseStaysOutWhateverTimeGitHubGaveTheClose(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"check_results", "ci_check_results"} {
		for _, close := range []struct{ name, closing string }{
			{"GitHub's time of the close is an hour before the result's",
				`update pull_request_heads set state = 'closed', event_at = clock_timestamp() - interval '1 hour' where number = 70`},
			{"the closing write names no time and keeps the open row's earlier one",
				`update pull_request_heads set state = 'closed' where number = 70`},
		} {
			t.Run(table+"/"+close.name, func(t *testing.T) {
				store := openEmptyTestStore(t)
				documents := installFixture(t, store)
				exec := installCloseStory(t, store, documents, table, closedPeriodH4, close.closing)
				var before bool
				if err := store.Pool.QueryRow(ctx, `
					select head.event_at < (select max(created_at) from (select created_at from check_results union all select created_at from ci_check_results) recorded)
					from pull_request_heads head where number = 70`).Scan(&before); err != nil || !before {
					t.Fatalf("#70's closed row is dated before the result: %v (%v), want true: this test holds no such close", before, err)
				}
				if err := store.Migrate(ctx); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				if heads, want := reopenedFormerHeads(t, store, exec), []string{"acme/widgets#70@3333"}; !slices.Equal(heads, want) {
					t.Fatalf("after the reopen #70 holds the former heads %v, want %v: the result at 4444… was recorded before the close, whatever time GitHub gave the close", heads, want)
				}
			})
		}
	}
}

// TestAClosedRowWrittenAgainKeepsTheTimeItFirstReadClosed: CORE-1's #70 closes at 4444…, and a
// result is then recorded at 4444… while #70 is closed. GitHub then updates the closed pull
// request again, its title, or a read of it finds it closed still, and Dispatch writes the closed
// row again with GitHub's later time, before the install. #70 reopens at 3333… and moves on to
// 7777…. The result at 4444… came after the close in Dispatch's own records, and a write of a row
// that is closed already moves no close: Dispatch keeps when the row first read closed
// (pull_request_heads.closed_at), so 4444… is a candidate of the reopened pull request. The
// neighbour writes the closed row no more. Each is tried with the result in either result table.
func TestAClosedRowWrittenAgainKeepsTheTimeItFirstReadClosed(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"check_results", "ci_check_results"} {
		for _, again := range []struct{ name, write string }{
			{"the closed row is not written again", ""},
			{"the closed row's title is written again with a later time",
				`update pull_request_heads set title = 'Edited while closed', event_at = clock_timestamp() where number = 70`},
			{"a read finds the pull request closed still, with a later time",
				`update pull_request_heads set state = 'closed', event_at = clock_timestamp(), checked_at = clock_timestamp() where number = 70`},
		} {
			t.Run(table+"/"+again.name, func(t *testing.T) {
				store := openEmptyTestStore(t)
				documents := installFixture(t, store)
				exec := installCloseStory(t, store, documents, "check_results", installH1, `update pull_request_heads set state = 'closed', event_at = clock_timestamp() where number = 70`)
				exec("record a result at 4444… while #70 is closed", installResult(table), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
				if again.write != "" {
					exec("write #70's closed row again", again.write)
				}
				if err := store.Migrate(ctx); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				if heads, want := reopenedFormerHeads(t, store, exec), []string{"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}; !slices.Equal(heads, want) {
					t.Fatalf("after the reopen #70 holds the former heads %v, want %v: the result at 4444… was recorded after the close, and a later write of the closed row moves no close", heads, want)
				}
			})
		}
	}
}

// TestAPullRequestClosedBeforeDispatchDatedClosesKeepsOutEveryResultRecordedBeforeTheInstall: the
// schedule of TestAClosedRowWrittenAgainKeepsTheTimeItFirstReadClosed, where #70 closed before
// Dispatch stored when a row first reads closed (migration pull_request_heads_closed_at), so no
// record on Dispatch's clock says whether the result at 4444… came after the close. The install
// takes every result it finds of such a pull request as one from before the close: a result taken
// from the lifecycle the close ended can carry a pass across the reopen, and one left out leaves
// its commit with no state, which a testing move waits on. So after the reopen the result at
// 4444… is no candidate, while a result recorded after the install, at a place after the close,
// is. The neighbour closes #70 once Dispatch dates closes, and the closed-period result counts.
func TestAPullRequestClosedBeforeDispatchDatedClosesKeepsOutEveryResultRecordedBeforeTheInstall(t *testing.T) {
	ctx := context.Background()
	const afterInstallH6 = "6666666666666666666666666666666666666666"
	for _, row := range []struct {
		name                string
		dated, afterInstall bool
		want                []string
	}{
		{name: "the close is from before Dispatch dated closes", want: []string{"acme/widgets#70@3333"}},
		{name: "the close is from before Dispatch dated closes and a result is recorded after the install", afterInstall: true,
			want: []string{"acme/widgets#70@3333", "acme/widgets#70@6666 unverified"}},
		{name: "Dispatch dated the close", dated: true, want: []string{"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := openEmptyTestStore(t)
			through := closesDatedSince(t) - 1
			if row.dated {
				through = formerHeadsInstall(t) - 1
			}
			documents := installFixtureThrough(t, store, through)
			exec := installCloseStory(t, store, documents, "check_results", installH1, `update pull_request_heads set state = 'closed', event_at = clock_timestamp() where number = 70`)
			exec("record a result at 4444… while #70 is closed", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
			if err := store.migrate(ctx, migrationsThrough(t, formerHeadsInstall(t)-1)); err != nil {
				t.Fatalf("migrate through the version before the install: %v", err)
			}
			var dated bool
			if err := store.Pool.QueryRow(ctx, `select closed_at is not null from pull_request_heads where number = 70`).Scan(&dated); err != nil || dated != row.dated {
				t.Fatalf("#70's closed row holds a time Dispatch dated its close: %v (%v), want %v: this test holds no such close", dated, err, row.dated)
			}
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if row.afterInstall {
				exec("record a result at 6666… while #70 is closed, after the install", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], afterInstallH6)
			}
			if heads := reopenedFormerHeads(t, store, exec); !slices.Equal(heads, row.want) {
				t.Fatalf("after the reopen #70 holds the former heads %v, want %v: a result no record on Dispatch's clock places after the close is taken as before it, and one after the install is after it", heads, row.want)
			}
		})
	}
}
