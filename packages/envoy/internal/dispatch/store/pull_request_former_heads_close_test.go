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
// stored under its repository's old name reads closed from before Dispatch recorded closes, a
// stale copy the canonical row outdates. A row of the old name written closed once Dispatch
// records closes is a close, as the settler takes one after the install: it ends the lifecycle
// the result at 1111… and the head 3333… the install found #70 at are of.
func TestEveryCloseEndsTheLifecycleItClosesWhereverDispatchFirstSawIt(t *testing.T) {
	ctx := context.Background()
	for _, row := range []struct {
		name string
		// closedAtInstall closes #70 before the install; firstWrittenClosed deletes #70's row
		// before the install and writes it again, closed, after it, as Dispatch writes a pull
		// request it first reads closed; staleOldName stores a closed row of #70 under the
		// repository's old name beside its open canonical row before Dispatch records closes, and
		// oldNameClosed after, before the install.
		closedAtInstall, firstWrittenClosed, staleOldName, oldNameClosed bool
		want                                                             []string
		ended                                                            bool
	}{
		{name: "the pull request is open at the install", want: []string{
			"acme/widgets#70@1111 unverified", "acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "the pull request is open at the install and its old name's row reads closed", staleOldName: true, want: []string{
			"acme/widgets#70@1111 unverified", "acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "the pull request is open at the install and its old name's row was written closed since Dispatch records closes",
			oldNameClosed: true, ended: true, want: []string{"acme/widgets#70@4444 unverified"}},
		{name: "the install runs over the pull request closed", closedAtInstall: true, ended: true, want: []string{
			"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "Dispatch first writes the pull request closed", firstWrittenClosed: true, ended: true, want: []string{
			"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := openEmptyTestStore(t)
			through := formerHeadsInstall(t) - 1
			if row.staleOldName {
				through = closesRecordedSince(t) - 1
			}
			documents := installFixtureThrough(t, store, through)
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
			case row.staleOldName, row.oldNameClosed:
				exec("record the repository's old name", `
					insert into repo_projects (repo, project, created_by, is_default, renamed_to)
					values ('acme/old-widgets', 'CORE', '{"kind":"system","id":"store-test"}', false, 'acme/widgets')`)
				exec("store #70 closed under the old name", `
					insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ('acme/old-widgets', 70, $1, 'closed', 'CORE-1')`, installH2)
			}
			if row.staleOldName {
				if err := store.migrate(ctx, migrationsThrough(t, formerHeadsInstall(t)-1)); err != nil {
					t.Fatalf("migrate through the version before the install: %v", err)
				}
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
			var ended *int64
			var generation int
			if err := store.Pool.QueryRow(ctx, `
				select ended, generation from pull_request_former_heads_pulls where pull = pull_request_former_heads_pull_of('acme/widgets', 70)`).Scan(&ended, &generation); err != nil {
				t.Fatalf("read #70's lifecycle: %v", err)
			}
			if row.ended && (ended == nil || generation < 2) {
				t.Fatalf("#70's lifecycle reads ended=%v generation=%d after its close, want an end and a later generation", ended, generation)
			}
			if !row.ended && ended != nil {
				t.Fatalf("#70's lifecycle reads ended=%d, want no end: no close of #70 is known", *ended)
			}
		})
	}
}

// TestAResultRecordedWhileAPullRequestIsClosedCountsAfterItReopensOnEitherSideOfTheInstall:
// CORE-1's #70 stands at 4444… before the install, where its row is written, and a tester records
// a result at 1111… while #70 is open. #70 then closes at 4444…, its row written closed, which
// Dispatch records on its own clock (pull_request_heads_closes), and a result is recorded at
// 4444… while #70 is closed: before the install, or after it. #70 then reopens at 3333… and moves
// on to 7777…, and CORE-1's settle drains what is owed. Wherever the install falls, the result at
// 1111… is of the lifecycle the close ended and stays out, and the result at 4444… is of the
// closed period and makes 4444… a candidate of the reopened pull request, as a result recorded
// after the close does in Dispatch's own records: the install places a result it finds and the
// close Dispatch recorded of a pull request closed when it runs in the order of Dispatch's own
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
// it recorded the close (pull_request_heads_closes), never by a time GitHub gave. Each close is
// tried with the result in either result table.
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
// that is closed already records no close: Dispatch's record of the close is of the write that
// closed the row (pull_request_heads_closes), so 4444… is a candidate of the reopened pull
// request. The neighbour writes the closed row no more. Each is tried with the result in either
// result table.
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

// TestAPullRequestClosedBeforeDispatchRecordedClosesKeepsOutEveryResultRecordedBeforeThen: the
// schedule of TestAClosedRowWrittenAgainKeepsTheTimeItFirstReadClosed, where #70 closed before
// Dispatch recorded closes (migration pull_request_heads_closes), so no record on Dispatch's clock
// says when it closed, only that it was closed when that migration ran, which records it then.
// The install takes every result recorded before that as one from before the close: a result
// taken from the lifecycle the close ended can carry a pass across the reopen, and one left out
// leaves its commit with no state, which a testing move waits on. So after the reopen the result
// at 4444… recorded before then is no candidate, while a result recorded since, before the
// install or after it, is. The neighbour closes #70 once Dispatch records closes, and the
// closed-period result counts.
func TestAPullRequestClosedBeforeDispatchRecordedClosesKeepsOutEveryResultRecordedBeforeThen(t *testing.T) {
	ctx := context.Background()
	const afterInstallH6 = "6666666666666666666666666666666666666666"
	for _, row := range []struct {
		name string
		// recorded closes #70 after Dispatch records closes; resultSince records the result at
		// 4444… after that migration, before the install.
		recorded, resultSince, afterInstall, counts bool
		want                                        []string
	}{
		{name: "the close and the result are from before Dispatch recorded closes", want: []string{"acme/widgets#70@3333"}},
		{name: "the close and the result are from before Dispatch recorded closes and a result is recorded after the install", afterInstall: true,
			want: []string{"acme/widgets#70@3333", "acme/widgets#70@6666 unverified"}},
		{name: "the close is from before Dispatch recorded closes and the result from since", resultSince: true, counts: true,
			want: []string{"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
		{name: "Dispatch recorded the close", recorded: true, counts: true, want: []string{"acme/widgets#70@3333", "acme/widgets#70@4444 unverified"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := openEmptyTestStore(t)
			through := closesRecordedSince(t) - 1
			if row.recorded {
				through = formerHeadsInstall(t) - 1
			}
			documents := installFixtureThrough(t, store, through)
			exec := installCloseStory(t, store, documents, "check_results", installH1, `update pull_request_heads set state = 'closed', event_at = clock_timestamp() where number = 70`)
			closedPeriod := func() {
				t.Helper()
				exec("record a result at 4444… while #70 is closed", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
			}
			if !row.resultSince {
				closedPeriod()
			}
			if err := store.migrate(ctx, migrationsThrough(t, formerHeadsInstall(t)-1)); err != nil {
				t.Fatalf("migrate through the version before the install: %v", err)
			}
			if row.resultSince {
				closedPeriod()
			}
			var records int
			var after bool
			if err := store.Pool.QueryRow(ctx, `
				select count(*), coalesce(bool_and(noted.closed_at > (select max(created_at) from check_results where commit_sha = $1)), false)
				from pull_request_heads_closes noted where noted.number = 70`, closedPeriodH4).Scan(&records, &after); err != nil || records != 1 || after == row.counts {
				t.Fatalf("Dispatch holds %d records of #70's close, after the result at 4444… %v (%v), want one, after it %v: this test holds no such close", records, after, err, !row.counts)
			}
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if row.afterInstall {
				exec("record a result at 6666… while #70 is closed, after the install", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], afterInstallH6)
			}
			if heads := reopenedFormerHeads(t, store, exec); !slices.Equal(heads, row.want) {
				t.Fatalf("after the reopen #70 holds the former heads %v, want %v: a result no record on Dispatch's clock places after the close is taken as before it, and one recorded since is after it", heads, row.want)
			}
		})
	}
}

// TestACloseAndAReopenBeforeTheInstallEndTheLifecycleTheyClosed: CORE-1's #70 stands at 1111…,
// where a result is recorded while #70 is open, and then a writer of the release before this one,
// which keeps no record of former heads, stores #70 closed and reopened at 3333… before the
// install runs, so the install finds #70 open. The close ended the lifecycle the result at 1111…
// is of, so after the install no settle may take 1111… as a former head of the reopened #70: the
// install reads every close Dispatch has recorded since it began recording them, which no reopen,
// re-key or delete erases, and a pull request closed when the recording began counts as closed
// then. The rows: the close and the reopen both after the recording began; the close before it
// and the reopen after it; the close of the row of the repository's old name, whose pull request
// the canonical-name writer then writes open under the canonical name and deletes under the old
// one; two closes and two reopens, with a result at 2222… in the lifecycle between them. The
// neighbours: a result recorded at 4444… while #70 was closed is of the lifecycle the reopen
// starts, and a pull request never closed keeps the result at 1111… as its candidate. Each row
// records the result at 1111… in either result table.
func TestACloseAndAReopenBeforeTheInstallEndTheLifecycleTheyClosed(t *testing.T) {
	ctx := context.Background()
	const between = "2222222222222222222222222222222222222222"
	for _, table := range []string{"check_results", "ci_check_results"} {
		for _, row := range []struct {
			name string
			// closedBeforeRecording closes #70 before the migration that records closes; oldName
			// stores #70 under the repository's old name first.
			closedBeforeRecording, oldName bool
			// story runs between the result at 1111… and the install, with #70 at 1111….
			story []string
			want  []string
			ended bool
		}{
			{name: "closed and reopened after closes are recorded", ended: true,
				story: []string{"close", "reopen at 3333"}},
			{name: "closed before closes are recorded and reopened after", closedBeforeRecording: true, ended: true,
				story: []string{"reopen at 3333"}},
			{name: "its old name's row closed and the pull request then written open under its canonical name", oldName: true, ended: true,
				story: []string{"close", "write open under the canonical name at 3333"}},
			{name: "closed and reopened twice, with a result in the lifecycle between", ended: true,
				story: []string{"close", "reopen at 2222", "record at 2222", "close", "reopen at 3333"}},
			{name: "a result recorded while it was closed", ended: true,
				story: []string{"close", "record at 4444", "reopen at 3333"}, want: []string{"acme/widgets#70@4444 unverified"}},
			{name: "never closed", story: []string{"move to 3333"}, want: []string{"acme/widgets#70@1111 unverified"}},
		} {
			t.Run(table+"/"+row.name, func(t *testing.T) {
				store := openEmptyTestStore(t)
				through := formerHeadsInstall(t) - 1
				if row.closedBeforeRecording {
					through = closesRecordedSince(t) - 1
				}
				documents := installFixtureThrough(t, store, through)
				exec := func(what, sql string, args ...any) {
					t.Helper()
					if _, err := store.Pool.Exec(ctx, sql, args...); err != nil {
						t.Fatalf("%s: %v", what, err)
					}
				}
				name := "acme/widgets"
				if row.oldName {
					name = "acme/old-widgets"
					exec("record the repository's old name", `
						insert into repo_projects (repo, project, created_by, is_default, renamed_to)
						values ('acme/old-widgets', 'CORE', '{"kind":"system","id":"store-test"}', false, 'acme/widgets')`)
					exec("store #70 under the old name instead", `update pull_request_heads set repo = 'acme/old-widgets' where number = 70`)
				}
				exec("write #70 at 1111…", `update pull_request_heads set head_sha = $1 where number = 70`, installH1)
				exec("record a result at 1111… while #70 is open", installResult(table), "CORE-1", "CORE-1", documents["CORE-1"], installH1)
				if row.closedBeforeRecording {
					exec("close #70 before closes are recorded", `update pull_request_heads set state = 'closed' where number = 70`)
					if err := store.migrate(ctx, migrationsThrough(t, formerHeadsInstall(t)-1)); err != nil {
						t.Fatalf("migrate through the version before the install: %v", err)
					}
				}
				for _, step := range row.story {
					switch step {
					case "close":
						exec("close #70", `update pull_request_heads set state = 'closed' where number = 70`)
					case "reopen at 2222":
						exec("reopen #70 at 2222…", `update pull_request_heads set state = 'open', head_sha = $1 where number = 70`, between)
					case "reopen at 3333":
						exec("reopen #70 at 3333…", `update pull_request_heads set state = 'open', head_sha = $1 where number = 70`, installH3)
					case "move to 3333":
						exec("move #70 on to 3333…", `update pull_request_heads set head_sha = $1 where number = 70`, installH3)
					case "record at 2222":
						exec("record a result at 2222…", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], between)
					case "record at 4444":
						exec("record a result at 4444… while #70 is closed", installResult("check_results"), "CORE-1", "CORE-1", documents["CORE-1"], closedPeriodH4)
					case "write open under the canonical name at 3333":
						exec("write #70 open under the canonical name at 3333…", `
							insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ('acme/widgets', 70, $1, 'open', 'CORE-1')`, installH3)
						exec("delete #70's row under the old name", `delete from pull_request_heads where repo = $1 and number = 70`, name)
					default:
						t.Fatalf("no step %q", step)
					}
				}
				var rows int
				var head string
				if err := store.Pool.QueryRow(ctx, `
					select count(*), max(head_sha) from pull_request_heads where number = 70 and repo = 'acme/widgets' and state = 'open'`).Scan(&rows, &head); err != nil {
					t.Fatalf("read #70's rows: %v", err)
				}
				if all := func() (n int) {
					if err := store.Pool.QueryRow(ctx, `select count(*) from pull_request_heads where number = 70`).Scan(&n); err != nil {
						t.Fatalf("count #70's rows: %v", err)
					}
					return n
				}(); rows != 1 || all != 1 || head != installH3 {
					t.Fatalf("#70 holds %d open canonical rows of %d, at %s, before the install, want one row, open at 3333…: this test holds no reopen before the install", rows, all, head)
				}
				if err := store.Migrate(ctx); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				drainCandidates(t, store, "CORE-1")
				if heads, _ := formerHeadsAndOwed(t, store); !slices.Equal(heads, row.want) {
					t.Fatalf("after the install #70 holds the former heads %v, want %v: a close before the install ends the lifecycle it closed, whatever reopen followed it", heads, row.want)
				}
				var ended bool
				if err := store.Pool.QueryRow(ctx, `
					select ended is not null from pull_request_former_heads_pulls where pull = pull_request_former_heads_pull_of('acme/widgets', 70)`).Scan(&ended); err != nil {
					t.Fatalf("read #70's lifecycle: %v", err)
				}
				if ended != row.ended {
					t.Fatalf("#70's lifecycle reads ended %v after the install, want %v", ended, row.ended)
				}
			})
		}
	}
}
