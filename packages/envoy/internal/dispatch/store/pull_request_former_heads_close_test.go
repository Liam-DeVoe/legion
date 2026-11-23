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
// a result at 1111… while #70 is open. #70 then closes at 4444…, its row written closed and dated
// by GitHub's time of the close, as every writer of a row dates it (pull_request_heads.event_at),
// and a result is recorded at 4444… while #70 is closed: before the install, or after it. #70
// then reopens at 3333… and moves on to 7777…, and CORE-1's settle drains what is owed. Wherever
// the install falls, the result at 1111… is of the lifecycle the close ended and stays out, and
// the result at 4444… is of the closed period and makes 4444… a candidate of the reopened pull
// request, as a result recorded after the close does in Dispatch's own records: the install places
// a result it finds and the close of a pull request closed when it runs in the order of their
// times, not every result before the close. Each schedule records the closed-period result in
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

// TestAnUndatedCloseAtTheInstallKeepsOutEveryResultRecordedBeforeTheInstall: the schedule of
// TestAResultRecordedWhileAPullRequestIsClosedCountsAfterItReopensOnEitherSideOfTheInstall with
// both results recorded before the install, where #70's row is written closed with no date: no
// time Dispatch holds says whether the result at 4444… came after the close. The install takes
// such a result as one from before the close, the conservative side, so after the reopen neither
// result is a candidate, while a result recorded after the install, at a place after the close,
// still is. The neighbour dates the close as every writer dates a row, and the closed-period
// result counts.
func TestAnUndatedCloseAtTheInstallKeepsOutEveryResultRecordedBeforeTheInstall(t *testing.T) {
	ctx := context.Background()
	const afterInstallH6 = "6666666666666666666666666666666666666666"
	for _, row := range []struct {
		name                string
		dated, afterInstall bool
		want                []string
	}{
		{name: "the close is undated", want: []string{"acme/widgets#70@3333"}},
		{name: "the close is undated and a result is recorded after the install", afterInstall: true,
			want: []string{"acme/widgets#70@3333", "acme/widgets#70@6666 unverified"}},
		{name: "the close is dated", dated: true, want: []string{"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
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
			exec("write #70 at 4444…", `update pull_request_heads set head_sha = $1 where number = 70`, closedPeriodH4)
			exec("record a result at 1111… while #70 is open", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], installH1)
			closing := `update pull_request_heads set state = 'closed' where number = 70`
			if row.dated {
				closing = `update pull_request_heads set state = 'closed', event_at = clock_timestamp() where number = 70`
			}
			exec("close #70 at 4444…", closing)
			var dated bool
			if err := store.Pool.QueryRow(ctx, `select event_at is not null from pull_request_heads where number = 70 and state = 'closed'`).Scan(&dated); err != nil {
				t.Fatalf("read #70's closed row: %v", err)
			}
			if dated != row.dated {
				t.Fatalf("#70's closed row is dated %v, want %v: this test holds no such close", dated, row.dated)
			}
			exec("record a result at 4444… while #70 is closed", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if row.afterInstall {
				exec("record a result at 6666… while #70 is closed, after the install", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], afterInstallH6)
			}
			exec("reopen #70 at 3333…", `update pull_request_heads set state = 'open', head_sha = $1 where number = 70`, installH3)
			exec("move #70 on to 7777…", `update pull_request_heads set head_sha = $1 where number = 70`, reopenedH7)
			drainCandidates(t, store, "CORE-1")
			heads, _ := formerHeadsAndOwed(t, store)
			if !slices.Equal(heads, row.want) {
				t.Fatalf("after the reopen #70 holds the former heads %v, want %v: a result whose order against an undated close no time shows is taken as before it, and one after the install is after it", heads, row.want)
			}
		})
	}
}
