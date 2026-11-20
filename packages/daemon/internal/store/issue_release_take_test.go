package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store/migrations"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// takenSandbox is the Sandbox the teardowns of these tests take, as the runtime names it.
var takenSandbox = "legion-" + strings.ToLower(rootIssue)

// storedReleasedArchitect is the root issue's architect claim an earlier release left
// `released`, stored: the suspended claim an operator's suspend takes over (supervise's
// suspendedAgain). It is storedReleasedArchitectOf for the root issue.
func storedReleasedArchitect(t *testing.T, st *Store) supervise.Claim {
	t.Helper()
	return storedReleasedArchitectOf(t, st, rootIssue)
}

// takenOver is c as the operator's take-over writes it: suspended by the operator.
func takenOver(c supervise.Claim) supervise.Claim {
	c.SuspendedBy = supervise.SuspendedByOperator
	return c
}

// markOfKind is the mark of a release of issue's pod by the rule kind names, whose one row is
// the teardown, row 10: the unowned release suspends no role, and the idle release has
// suspended the architect.
func markOfKind(t *testing.T, st *Store, issue, kind string) {
	t.Helper()
	role, suspended := "", []string{}
	if kind == releaseKindIdle {
		role, suspended = string(claim.RoleArchitect), []string{string(claim.RoleArchitect)}
	}
	if _, err := st.Pool().Exec(context.Background(), `insert into issue_releases
		(project, issue, generation, first_row, boundary_row, last_row, started_row, role, kind, suspended, marked_at)
		values ('legion', $1, 1, 10, 10, 10, 0, $2, $3, $4, now())`, issue, role, kind, suspended); err != nil {
		t.Fatalf("mark the %s release of %s: %v", kind, issue, err)
	}
}

// teardownRead is the teardown's read of the rig's release at row 10, the runtime's last read
// when last is set, made under the lease the teardown's run holds that row by (teardownLease).
func teardownRead(t *testing.T, st *Store, last bool) ReleaseDecision {
	t.Helper()
	release := IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1, Row: 10}
	if last {
		release.Takes, release.Sandbox, release.Lease = true, takenSandbox, teardownLease(t, st).Token
	}
	decision, err := st.IssueRelease(context.Background(), "legion", release, record.OutOfWorkflow)
	if err != nil {
		t.Fatalf("the teardown's read (last %t): %v", last, err)
	}
	return decision
}

// teardownLease is the lease a run of the rig's teardown row, row 10, holds it by: the row is
// queued once and leased to that run, so every write of the take made under it lands
// (RowLease).
func teardownLease(t *testing.T, st *Store) RowLease {
	t.Helper()
	const token = "the-teardown-run's-lease"
	if _, err := st.Pool().Exec(context.Background(), `insert into outbox (id, kind, issue, payload, attempts, next_at, last_error, lease_token, lease_until)
		values (10, 'issue_release', $1, '{}', 0, now(), '', $2, now() + interval '30 seconds')
		on conflict (id) do update set lease_token = excluded.lease_token, lease_until = excluded.lease_until`, rootIssue, token); err != nil {
		t.Fatalf("lease the teardown's row: %v", err)
	}
	return RowLease{Row: 10, Token: token}
}

// storedSource is the state and suspension source the store holds for c.
func storedSource(t *testing.T, st *Store, c supervise.Claim) (state, by string) {
	t.Helper()
	if err := st.Pool().QueryRow(context.Background(), `select state, suspended_by from claims where token = $1`,
		string(c.Token)).Scan(&state, &by); err != nil {
		t.Fatalf("read the stored %s claim: %v", c.Role, err)
	}
	return state, by
}

// refusedAsTaken requires err to be the refusal of a take-over while the release of
// the root issue's pod takes it, naming that release and the Sandbox, and the claim to be stored as
// it was.
func refusedAsTaken(t *testing.T, st *Store, err error, c supervise.Claim) {
	t.Helper()
	var taken *supervise.PodTakenError
	if !errors.As(err, &taken) {
		t.Fatalf("the operator's take-over after the last read = %v, want it refused: the pod is being deleted", err)
	}
	var release int64
	if err := st.Pool().QueryRow(context.Background(), `select id from issue_releases where issue = $1`, rootIssue).Scan(&release); err != nil {
		t.Fatalf("read the release: %v", err)
	}
	if taken.Issue != rootIssue || taken.Release != release || taken.Sandbox != takenSandbox {
		t.Fatalf("the refusal names %+v, want release %d of %s and the Sandbox %s", *taken, release, rootIssue, takenSandbox)
	}
	if state, by := storedSource(t, st, c); state != "suspended" || by != "released" {
		t.Fatalf("the stored %s claim is %s by %s, want it left suspended by released", c.Role, state, by)
	}
}

// launched is c as the start of its launch writes it: launching, at its next generation, with no
// suspension source (supervise's launchNow).
func launched(c supervise.Claim) supervise.Claim {
	c.State, c.SuspendedBy, c.Generation = supervise.StateLaunching, "", c.Generation+1
	return c
}

// takenRelease is the id of the root issue's release, the one whose take these tests read.
func takenRelease(t *testing.T, st *Store) int64 {
	t.Helper()
	var release int64
	if err := st.Pool().QueryRow(context.Background(), `select id from issue_releases where issue = $1`, rootIssue).Scan(&release); err != nil {
		t.Fatalf("read the release: %v", err)
	}
	return release
}

// refusedAsLaunching requires err to be the refusal of the start of c's launch while the release
// of the root issue's pod takes it: the named wait, naming that release and the Sandbox, with the
// claim stored as it was.
func refusedAsLaunching(t *testing.T, st *Store, err error, c supervise.Claim) {
	t.Helper()
	var taken *supervise.PodTakenLaunchError
	if !errors.As(err, &taken) || !errors.Is(err, wait.ErrWaiting) {
		t.Fatalf("the launch after the last read = %v, want the wait for the release: the pod is being deleted", err)
	}
	if release := takenRelease(t, st); taken.Issue != rootIssue || taken.Release != release || taken.Sandbox != takenSandbox {
		t.Fatalf("the refusal names %+v, want release %d of %s and the Sandbox %s", *taken, release, rootIssue, takenSandbox)
	}
	if state, by := storedSource(t, st, c); state != "suspended" || by != "released" {
		t.Fatalf("the stored %s claim is %s by %s, want it left suspended by released", c.Role, state, by)
	}
}

// refusedByTheDatabase requires err to be the database's own refusal of a write past the take of
// the root issue's pod (the trigger claims_refused_past_a_release_take): the SQLSTATE code, with a
// detail naming the release and the Sandbox.
func refusedByTheDatabase(t *testing.T, st *Store, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("the write past the take = %v, want the database's refusal %s", err, code)
	}
	if want := fmt.Sprintf(`{"release" : %d, "sandbox" : %q}`, takenRelease(t, st), takenSandbox); pgErr.Detail != want {
		t.Fatalf("the refusal's detail = %q, want %q", pgErr.Detail, want)
	}
}

// The runtime takes the pod on the teardown's last read, the one it asks under its pod lock,
// and reads the store no more. That read records the take on the mark when it answers that the
// pod goes, and an operator's take-over of a suspended claim of the issue is refused from then
// until the release ends, naming the release and the Sandbox: a suspension that late would land
// on a claim whose pod is going. A take-over before that read is read by it, and keeps the pod.
func TestTheTeardownsLastReadTakesThePodFromAnOperatorsTakeOver(t *testing.T) {
	ctx := context.Background()

	t.Run("a take-over before the last read is read by it, and the release keeps the pod", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the take-over before the last read = %v, want it written", err)
		}
		if got := teardownRead(t, st, true); got.Act != ReleaseCancel || got.Cause != ReleaseCauseOperatorSuspended {
			t.Fatalf("the last read = %s (cause %s), want cancel (cause %s)", got.Act, got.Cause, ReleaseCauseOperatorSuspended)
		}
	})

	t.Run("the last read takes the pod, and a take-over after it is refused", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		refusedAsTaken(t, st, st.PutClaim(ctx, takenOver(architect)), architect)
	})

	t.Run("the teardown's first read takes nothing", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		if got := teardownRead(t, st, false); got.Act != ReleaseActs {
			t.Fatalf("the first read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the take-over between the two reads = %v, want it written: the runtime has not taken the pod", err)
		}
		if got := teardownRead(t, st, true); got.Act != ReleaseCancel || got.Cause != ReleaseCauseOperatorSuspended {
			t.Fatalf("the last read = %s (cause %s), want cancel (cause %s)", got.Act, got.Cause, ReleaseCauseOperatorSuspended)
		}
	})

	t.Run("an idle release takes the pod on its last read too", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindIdle)
		architect := storedReleasedArchitect(t, st)

		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the idle release's last read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		refusedAsTaken(t, st, st.PutClaim(ctx, takenOver(architect)), architect)
	})

	t.Run("the take of another issue's pod refuses nothing of this issue", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, childIssue, releaseKindUnowned)
		if _, err := st.Pool().Exec(ctx, `update issue_releases set taken_pod = $2 where issue = $1`, childIssue,
			"legion-"+strings.ToLower(childIssue)); err != nil {
			t.Fatalf("take the other issue's pod: %v", err)
		}
		architect := storedReleasedArchitect(t, st)

		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the take-over of %s's claim = %v, want it written: only %s's pod is going", rootIssue, err, childIssue)
		}
		if state, by := storedSource(t, st, architect); state != "suspended" || by != "operator" {
			t.Fatalf("the stored architect is %s by %s, want suspended by operator", state, by)
		}
	})

	t.Run("once the release has ended the take-over is written", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
		}

		if err := st.ClearIssueRelease(ctx, "legion", rootIssue, teardownLease(t, st)); err != nil {
			t.Fatalf("finish the release: %v", err)
		}
		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the take-over once the release ended = %v, want it written", err)
		}
		if state, by := storedSource(t, st, architect); state != "suspended" || by != "operator" {
			t.Fatalf("the stored architect is %s by %s, want suspended by operator", state, by)
		}
	})
}

// waitedOnTheSerializer polls until a backend of st's database waits on an advisory lock, which
// in these tests is only the shared serializer (treelifecycle.Serialize), and answers true; it
// answers false as soon as done is closed first, which is the call under test finishing without
// having waited.
func waitedOnTheSerializer(t *testing.T, st *Store, done <-chan struct{}) bool {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return false
		default:
		}
		var waiting bool
		if err := st.Pool().QueryRow(context.Background(), `select exists (select 1 from pg_locks
			where locktype = 'advisory' and not granted
			and database = (select oid from pg_database where datname = current_database()))`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		if waiting {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("after 60 s nothing waits on the serializer and the call has not finished")
	return false
}

// The take and the take-over are ordered by the shared serializer, which both transactions take
// first. Each case holds one of them open at the moment the other begins: the second must wait
// for the first to commit, then read what it wrote. Without that wait the two would pass each
// other, and an operator's suspension would land after the last read on a claim whose pod is
// going.
func TestTheLastReadAndATakeOverWaitForEachOther(t *testing.T) {
	ctx := context.Background()

	t.Run("a take-over in flight when the last read begins is read by it", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		takeOver, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = takeOver.Rollback(ctx) }()
		if err := treelifecycle.Serialize(ctx, takeOver); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutClaimTx(ctx, takeOver, takenOver(architect)); err != nil {
			t.Fatalf("write the take-over: %v", err)
		}

		lease := teardownLease(t, st)
		done := make(chan struct{})
		var decision ReleaseDecision
		var readErr error
		go func() {
			defer close(done)
			decision, readErr = st.IssueRelease(ctx, "legion", IssueReleased{Issue: rootIssue, Tree: rootIssue,
				IssueGeneration: 1, TreeGeneration: 1, Row: 10, Takes: true, Sandbox: takenSandbox, Lease: lease.Token}, record.OutOfWorkflow)
		}()
		if !waitedOnTheSerializer(t, st, done) {
			t.Fatalf("the last read finished (%s, cause %s, %v) while the take-over held the serializer", decision.Act, decision.Cause, readErr)
		}
		if err := takeOver.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		if readErr != nil || decision.Act != ReleaseCancel || decision.Cause != ReleaseCauseOperatorSuspended {
			t.Fatalf("the last read = %s (cause %s), %v; want cancel (cause %s): the take-over committed before it read",
				decision.Act, decision.Cause, readErr, ReleaseCauseOperatorSuspended)
		}
	})

	t.Run("a last read in flight when a take-over begins refuses it", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		take, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = take.Rollback(ctx) }()
		if err := treelifecycle.Serialize(ctx, take); err != nil {
			t.Fatal(err)
		}
		if _, err := take.Exec(ctx, `update issue_releases set taken_pod = $2 where issue = $1`, rootIssue, takenSandbox); err != nil {
			t.Fatalf("record the take: %v", err)
		}

		done := make(chan struct{})
		var tookOver error
		go func() {
			defer close(done)
			tookOver = st.PutClaim(ctx, takenOver(architect))
		}()
		if !waitedOnTheSerializer(t, st, done) {
			t.Fatalf("the take-over finished (%v) while the last read held the serializer", tookOver)
		}
		if err := take.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		refusedAsTaken(t, st, tookOver, architect)
	})

	t.Run("a launch in flight when the last read begins is read by it", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		launch, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = launch.Rollback(ctx) }()
		if err := treelifecycle.Serialize(ctx, launch); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutClaimTx(ctx, launch, launched(architect)); err != nil {
			t.Fatalf("write the launch: %v", err)
		}

		lease := teardownLease(t, st)
		done := make(chan struct{})
		var decision ReleaseDecision
		var readErr error
		go func() {
			defer close(done)
			decision, readErr = st.IssueRelease(ctx, "legion", IssueReleased{Issue: rootIssue, Tree: rootIssue,
				IssueGeneration: 1, TreeGeneration: 1, Row: 10, Takes: true, Sandbox: takenSandbox, Lease: lease.Token}, record.OutOfWorkflow)
		}()
		if !waitedOnTheSerializer(t, st, done) {
			t.Fatalf("the last read finished (%s, cause %s, %v) while the launch held the serializer", decision.Act, decision.Cause, readErr)
		}
		if err := launch.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		if readErr != nil || decision.Act != ReleaseCancel || decision.Cause != ReleaseCauseOvertaken {
			t.Fatalf("the last read = %s (cause %s), %v; want cancel (cause %s): the launch committed before it read",
				decision.Act, decision.Cause, readErr, ReleaseCauseOvertaken)
		}
	})

	t.Run("a last read in flight when a launch begins refuses it", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)

		take, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = take.Rollback(ctx) }()
		if err := treelifecycle.Serialize(ctx, take); err != nil {
			t.Fatal(err)
		}
		if _, err := take.Exec(ctx, `update issue_releases set taken_pod = $2 where issue = $1`, rootIssue, takenSandbox); err != nil {
			t.Fatalf("record the take: %v", err)
		}

		done := make(chan struct{})
		var launchErr error
		go func() {
			defer close(done)
			launchErr = st.PutClaim(ctx, launched(architect))
		}()
		if !waitedOnTheSerializer(t, st, done) {
			t.Fatalf("the launch finished (%v) while the last read held the serializer", launchErr)
		}
		if err := take.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		refusedAsLaunching(t, st, launchErr, architect)
	})
}

// storedOperatorsPlanner is the root issue's planner claim, suspended by the operator, stored.
func storedOperatorsPlanner(t *testing.T, st *Store) supervise.Claim {
	t.Helper()
	token, err := claim.NewToken("legion", rootIssue, claim.RolePlanner)
	if err != nil {
		t.Fatal(err)
	}
	c := supervise.Claim{Token: token, Project: "legion", Tree: rootIssue, Issue: rootIssue, Role: claim.RolePlanner,
		TreeEpoch: 1, Generation: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByOperator}
	if err := st.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("store the operator's planner: %v", err)
	}
	return c
}

// The rule is the database's, so every write of a claim meets it, whichever store write and
// whichever decision makes it: once the last read has taken the pod, each exported write is refused, and
// writes nothing, when it makes a claim of the issue the operator's suspension or starts a claim's
// launch. A claim the store holds live meets the same refusal, since the rule holds whatever the
// claim was: none is live past that read, because no launch starts there. A claim already the
// operator's is rewritten, and a launch already under way is written again.
func TestEveryClaimWriteRefusesWhatTheTakeOfAPodForbids(t *testing.T) {
	ctx := context.Background()
	taken := func(t *testing.T) (*Store, supervise.Claim) {
		t.Helper()
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		return st, architect
	}
	delivery := supervise.Delivery{ID: "outbox:9", Task: "a task", QueuedAt: time.Now().UTC()}
	for _, tc := range []struct {
		name  string
		write func(st *Store, c supervise.Claim) error
	}{
		{name: "PutClaim", write: func(st *Store, c supervise.Claim) error { return st.PutClaim(ctx, c) }},
		{name: "PutClaimAndDelivery", write: func(st *Store, c supervise.Claim) error { return st.PutClaimAndDelivery(ctx, c, delivery) }},
		{name: "ReleaseHolds", write: func(st *Store, c supervise.Claim) error { _, err := st.ReleaseHolds(ctx, c, nil); return err }},
		{name: "PutClaimTx, inside a pause's own transaction", write: func(st *Store, c supervise.Claim) error {
			return st.Tx(ctx, func(tx pgx.Tx) error { _, err := st.PutClaimTx(ctx, tx, c); return err })
		}},
	} {
		t.Run(tc.name+", the operator's suspension", func(t *testing.T) {
			st, architect := taken(t)
			refusedAsTaken(t, st, tc.write(st, takenOver(architect)), architect)
		})
		t.Run(tc.name+", a launch", func(t *testing.T) {
			st, architect := taken(t)
			refusedAsLaunching(t, st, tc.write(st, launched(architect)), architect)
		})
	}

	t.Run("a claim the store holds live is refused too", func(t *testing.T) {
		st, architect := taken(t)
		if _, err := st.Pool().Exec(ctx, `update claims set state = 'ready', suspended_by = '' where token = $1`, string(architect.Token)); err != nil {
			t.Fatal(err)
		}
		err := st.PutClaim(ctx, takenOver(architect))
		var refused *supervise.PodTakenError
		if !errors.As(err, &refused) {
			t.Fatalf("the operator's suspension of a claim the store holds ready = %v, want it refused: the pod is being deleted", err)
		}
		if state, by := storedSource(t, st, architect); state != "ready" || by != "" {
			t.Fatalf("the stored architect is %s by %q, want it left ready", state, by)
		}
	})

	t.Run("a claim already the operator's is rewritten", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		// The idle release reads no operator's suspension (its rule never read one), so its last
		// read takes the pod beside the operator's planner.
		markOfKind(t, st, rootIssue, releaseKindIdle)
		storedReleasedArchitect(t, st)
		planner := storedOperatorsPlanner(t, st)
		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		again := planner
		again.Session = "ses-rewritten"
		if err := st.PutClaim(ctx, again); err != nil {
			t.Fatalf("a rewrite of the operator's suspension = %v, want it written: it takes nothing over", err)
		}
	})

	t.Run("a launch already under way is written again", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		launching := launched(architect)
		if err := st.PutClaim(ctx, launching); err != nil {
			t.Fatalf("the launch before the take = %v, want it written", err)
		}
		if _, err := st.Pool().Exec(ctx, `update issue_releases set taken_pod = $2 where issue = $1`, rootIssue, takenSandbox); err != nil {
			t.Fatalf("record the take: %v", err)
		}
		launching.Generation++
		if err := st.PutClaim(ctx, launching); err != nil {
			t.Fatalf("a launch's next try = %v, want it written: it starts no launch", err)
		}
	})

	t.Run("the store's launch check refuses the launch first", func(t *testing.T) {
		st, architect := taken(t)
		refusedAsLaunching(t, st, st.CheckLaunch(ctx, launched(architect)), architect)
		if err := st.CheckLaunch(ctx, launched(storedReleasedArchitectOf(t, st, childIssue))); err != nil {
			t.Fatalf("control: the launch check of a claim of another issue = %v, want it to pass: only %s's pod is going", err, rootIssue)
		}
	})
}

// storedReleasedArchitectOf is the architect claim of issue an earlier release left `released`,
// stored: the one released-architect fixture, for the root issue (storedReleasedArchitect) and for
// a child of the root issue's tree.
func storedReleasedArchitectOf(t *testing.T, st *Store, issue string) supervise.Claim {
	t.Helper()
	token, err := claim.NewToken("legion", issue, claim.RoleArchitect)
	if err != nil {
		t.Fatal(err)
	}
	c := supervise.Claim{Token: token, Project: "legion", Tree: rootIssue, Issue: issue, Role: claim.RoleArchitect, TreeEpoch: 1,
		Generation: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByReleased}
	if err := st.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("store the released architect of %s: %v", issue, err)
	}
	return c
}

// publications are the two stores a claim write runs on: the role-holder publication off, where
// the store's write is one upsert, and on, as every daemon with a workflow runs it
// (st.PublishRoleHolders, daemon.go), where the store reads the stored holder under the claim's
// row lock first (putClaim).
var publications = []struct {
	name string
	on   bool
}{{"the role-holder publication off", false}, {"the role-holder publication on, as production runs", true}}

// publishing is a migrated store with the role-holder publication on where on is set.
func publishing(t *testing.T, on bool) *Store {
	t.Helper()
	st := migratedStore(t)
	if on {
		st.PublishRoleHolders(nil)
	}
	return st
}

// A transaction that takes both the shared serializer and a claim's row takes the serializer
// first, as a fact does, so a fact that holds the serializer and then writes the same claim runs
// to its end while the launch waits, and neither is aborted as a deadlock: the store's launch
// meets the database's rule as an insert before its upsert locks the row, and where the
// role-holder publication is on the store takes the serializer before it reads the row under its
// lock (putClaim). The control is a write planted in the reverse order — the claim's row locked,
// then the serializer — which meets the same fact in a deadlock (40P01), so the test can see one.
func TestAClaimWriteTakesTheSerializerBeforeTheClaimsRow(t *testing.T) {
	ctx := context.Background()
	for _, p := range publications {
		t.Run(p.name, func(t *testing.T) {
			st := publishing(t, p.on)
			openRunningTree(t, st)
			architect := storedReleasedArchitect(t, st)
			if st.queuesRoleHolders(launched(architect)) != p.on {
				t.Fatalf("setup: the launch queues a publication %t, want %t", !p.on, p.on)
			}
			fact, err := st.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fact.Rollback(ctx) }()
			if err := treelifecycle.Serialize(ctx, fact); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var launchErr error
			go func() {
				defer close(done)
				launchErr = st.PutClaim(ctx, launched(architect))
			}()
			if !waitedOnTheSerializer(t, st, done) {
				t.Fatalf("the launch finished (%v) while the fact held the serializer", launchErr)
			}
			factCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			_, factErr := fact.Exec(factCtx, `update claims set updated_at = now() where token = $1`, string(architect.Token))
			if factErr == nil {
				factErr = fact.Commit(ctx)
			}
			<-done
			if factErr != nil || launchErr != nil {
				t.Fatalf("a fact holding the serializer and writing the claim, and the launch of that claim: fact %v, launch %v; want both written, neither aborted",
					factErr, launchErr)
			}
		})
	}
	t.Run("control: a write that locks the claim's row before the serializer meets the fact in a deadlock", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		architect := storedReleasedArchitect(t, st)
		planted, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = planted.Rollback(ctx) }()
		if _, err := planted.Exec(ctx, `select 1 from claims where token = $1 for update`, string(architect.Token)); err != nil {
			t.Fatal(err)
		}
		fact, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fact.Rollback(ctx) }()
		if err := treelifecycle.Serialize(ctx, fact); err != nil {
			t.Fatal(err)
		}
		factDone := make(chan error, 1)
		go func() {
			_, err := fact.Exec(ctx, `update claims set updated_at = now() where token = $1`, string(architect.Token))
			factDone <- err
		}()
		time.Sleep(200 * time.Millisecond)
		plantedErr := treelifecycle.Serialize(ctx, planted)
		if plantedErr != nil {
			_ = planted.Rollback(ctx)
		}
		factErr := <-factDone
		var pgErr *pgconn.PgError
		if !(errors.As(plantedErr, &pgErr) && pgErr.Code == "40P01") && !(errors.As(factErr, &pgErr) && pgErr.Code == "40P01") {
			t.Fatalf("the reverse order against the fact: planted %v, fact %v; want one of them aborted as a deadlock (40P01)", plantedErr, factErr)
		}
	})
}

// The database's rule takes no serializer for a write that starts nothing — a launch's next try,
// a write of any other state, a rewrite of a claim the operator's already — so on a store whose
// role-holder publication is off it is written while another transaction holds the serializer,
// and the claim writes a daemon makes all day wait on no fact. Where the publication is on, as
// production runs, the store takes the serializer before the claim's row for every write the
// rule's condition names (putClaim, takesTheTakeRule): a write of any other state still takes
// none, and a launch's next try and a rewrite of the operator's suspension wait for it, as a
// launch's start does. The control is a launch's start, which waits for the serializer on both.
func TestAClaimWriteThatStartsNothingTakesNoSerializer(t *testing.T) {
	ctx := context.Background()
	for _, p := range publications {
		t.Run(p.name, func(t *testing.T) {
			st := publishing(t, p.on)
			openRunningTree(t, st)
			architect := storedReleasedArchitect(t, st)
			launching := launched(architect)
			if err := st.PutClaim(ctx, launching); err != nil {
				t.Fatal(err)
			}
			holding := func(t *testing.T, write func() error) (waited bool, err error) {
				t.Helper()
				held, beginErr := st.BeginTx(ctx, pgx.TxOptions{})
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer func() { _ = held.Rollback(ctx) }()
				if err := treelifecycle.Serialize(ctx, held); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					err = write()
				}()
				waited = waitedOnTheSerializer(t, st, done)
				_ = held.Rollback(ctx)
				<-done
				return waited, err
			}
			next := launching
			next.Generation++
			if waited, err := holding(t, func() error { return st.PutClaim(ctx, next) }); waited != p.on || err != nil {
				t.Fatalf("a launch's next try waited on the serializer %t, %v; want %t, then written", waited, err, p.on)
			}
			idle := next
			idle.State = supervise.StateIdle
			if waited, err := holding(t, func() error { return st.PutClaim(ctx, idle) }); waited || err != nil {
				t.Fatalf("a write of an idle claim waited on the serializer %t, %v; want it written at once", waited, err)
			}
			planner := storedOperatorsPlanner(t, st)
			planner.Session = "ses-rewritten"
			if waited, err := holding(t, func() error { return st.PutClaim(ctx, planner) }); waited != p.on || err != nil {
				t.Fatalf("a rewrite of the operator's suspension waited on the serializer %t, %v; want %t, then written", waited, err, p.on)
			}
			if waited, err := holding(t, func() error { return st.PutClaim(ctx, launched(planner)) }); !waited || err != nil {
				t.Fatalf("control: a launch's start waited on the serializer %t, %v; want it to wait, then be written", waited, err)
			}
		})
	}
}

// The database holds the take's two rules for every statement that writes claims, not only the
// store's own write: an update of the suspension source written in any form, a copy of a new
// claim, and a launch written by hand are each refused past the last read with the trigger's own
// code and the release and the Sandbox it names, and the issue then holds no claim the operator's
// and none launching. A write past the store's own guard is answered by name too, as the store
// answers its guard's refusals. The controls are the same update before the last read, which
// lands and which that read then reads, and a daemon's own suspension past it, which lands.
func TestTheDatabaseRefusesEveryWritePastATake(t *testing.T) {
	ctx := context.Background()
	copyClaim := func(st *Store, state, by string) error {
		token, err := claim.NewToken("legion", rootIssue, claim.RolePlanner)
		if err != nil {
			return err
		}
		return st.Tx(ctx, func(tx pgx.Tx) error {
			_, err := tx.CopyFrom(ctx, pgx.Identifier{"claims"},
				[]string{"token", "project", "tree", "issue", "role", "generation", "tree_epoch", "session", "session_file", "state",
					"launch_failures", "prompt_failures", "prompt_retires", "uncertain_streak", "suspended_by"},
				pgx.CopyFromRows([][]any{{string(token), "legion", rootIssue, rootIssue, string(claim.RolePlanner), int64(1), int64(1),
					"", "", state, int32(0), int32(0), int32(0), int32(0), by}}))
			return err
		})
	}
	for _, tc := range []struct {
		name  string
		code  string
		write func(st *Store, token string) error
	}{
		{"a double-quoted update of the source", takenPodSuspensionCode, func(st *Store, token string) error {
			_, err := st.Pool().Exec(ctx, "update claims set suspended_by = 'operator' where token = $1", token)
			return err
		}},
		{"an update through an alias", takenPodSuspensionCode, func(st *Store, token string) error {
			_, err := st.Pool().Exec(ctx, `update claims as c set suspended_by = 'operator' where c.token = $1`, token)
			return err
		}},
		{"an update of the schema's table", takenPodSuspensionCode, func(st *Store, token string) error {
			_, err := st.Pool().Exec(ctx, `update public.claims set suspended_by = 'operator' where token = $1`, token)
			return err
		}},
		{"a copy of a new claim suspended by the operator", takenPodSuspensionCode, func(st *Store, _ string) error {
			return copyClaim(st, "suspended", "operator")
		}},
		{"a launch of the stopped claim written by hand", takenPodLaunchCode, func(st *Store, token string) error {
			_, err := st.Pool().Exec(ctx, `update claims set state = 'launching', suspended_by = '' where token = $1`, token)
			return err
		}},
		{"a copy of a new claim launching", takenPodLaunchCode, func(st *Store, _ string) error {
			return copyClaim(st, "launching", "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			openRunningTree(t, st)
			markOfKind(t, st, rootIssue, releaseKindUnowned)
			architect := storedReleasedArchitect(t, st)
			if got := teardownRead(t, st, true); got.Act != ReleaseActs {
				t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
			}
			refusedByTheDatabase(t, st, tc.write(st, string(architect.Token)), tc.code)
			var landed int
			if err := st.Pool().QueryRow(ctx, `select count(*) from claims where issue = $1 and (suspended_by = 'operator' or state = 'launching')`,
				rootIssue).Scan(&landed); err != nil {
				t.Fatal(err)
			}
			if state, by := storedSource(t, st, architect); landed != 0 || state != "suspended" || by != "released" {
				t.Fatalf("the issue holds %d claims the operator's or launching, and the architect is %s by %s; want none, and the architect released",
					landed, state, by)
			}
		})
	}

	t.Run("a raw launch in flight while the last read holds the serializer waits and is refused", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		take, err := st.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = take.Rollback(ctx) }()
		if err := treelifecycle.Serialize(ctx, take); err != nil {
			t.Fatal(err)
		}
		if _, err := take.Exec(ctx, `update issue_releases set taken_pod = $2 where issue = $1`, rootIssue, takenSandbox); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var raw error
		go func() {
			defer close(done)
			_, raw = st.Pool().Exec(ctx, `update claims set state = 'launching', suspended_by = '' where token = $1`, string(architect.Token))
		}()
		if !waitedOnTheSerializer(t, st, done) {
			t.Fatalf("the raw launch finished (%v) while the last read held the serializer", raw)
		}
		if err := take.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		refusedByTheDatabase(t, st, raw, takenPodLaunchCode)
	})

	t.Run("the store's statement on the pool is answered by name", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		refusedAsTaken(t, st, upsertClaim(ctx, st.Pool(), takenOver(architect)), architect)
		refusedAsLaunching(t, st, upsertClaim(ctx, st.Pool(), launched(architect)), architect)
	})

	t.Run("control: the same update before the last read lands, and that read reads it", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		if _, err := st.Pool().Exec(ctx, "update claims set suspended_by = 'operator' where token = $1", string(architect.Token)); err != nil {
			t.Fatalf("the update before the last read = %v, want it written", err)
		}
		if got := teardownRead(t, st, true); got.Act != ReleaseCancel || got.Cause != ReleaseCauseOperatorSuspended {
			t.Fatalf("the last read = %s (cause %s), want cancel (cause %s)", got.Act, got.Cause, ReleaseCauseOperatorSuspended)
		}
	})

	t.Run("control: a daemon's own suspension past the last read lands", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		if got := teardownRead(t, st, true); got.Act != ReleaseActs {
			t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
		}
		if _, err := st.Pool().Exec(ctx, `update claims set suspended_by = 'daemon' where token = $1`, string(architect.Token)); err != nil {
			t.Fatalf("a daemon's suspension past the last read = %v, want it written: it holds no pod", err)
		}
		daemons := architect
		daemons.SuspendedBy, daemons.Session = supervise.SuspendedByDaemon, "ses-daemon"
		if err := st.PutClaim(ctx, daemons); err != nil {
			t.Fatalf("the store's write of a daemon's suspension past the last read = %v, want it written", err)
		}
	})
}

// A last read records the take only when it answers that the pod goes, the one answer the runtime
// acts on. A last read that cancels the release, or that waits for the release's own save, leaves
// no take on the mark, and an operator's take-over of a claim of the issue is then written: the
// pod is not going on that read.
func TestALastReadThatWaitsOrCancelsRecordsNoTake(t *testing.T) {
	ctx := context.Background()
	noTake := func(t *testing.T, st *Store, c supervise.Claim) {
		t.Helper()
		var recorded bool
		if err := st.Pool().QueryRow(ctx, `select taken_pod is not null from issue_releases where issue = $1`, rootIssue).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		if err := st.PutClaim(ctx, takenOver(c)); recorded || err != nil {
			t.Fatalf("after the last read the mark records a take %t, and the operator's take-over = %v; want no take and the take-over written",
				recorded, err)
		}
	}

	t.Run("a last read that cancels the release", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		storedOperatorsPlanner(t, st)
		if got := teardownRead(t, st, true); got.Act != ReleaseCancel || got.Cause != ReleaseCauseOperatorSuspended {
			t.Fatalf("the last read = %s (cause %s), want cancel (cause %s)", got.Act, got.Cause, ReleaseCauseOperatorSuspended)
		}
		noTake(t, st, architect)
	})

	t.Run("a last read that waits for the release's save", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindIdle)
		architect := storedReleasedArchitect(t, st)
		if _, err := st.Pool().Exec(ctx, `insert into outbox (id, kind, issue, payload, attempts, next_at, last_error)
			values (10, 'issue_bundle', $1, '{}', 0, now(), '')`, rootIssue); err != nil {
			t.Fatalf("queue the release's save: %v", err)
		}
		release := IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1, Row: 10,
			Takes: true, Sandbox: takenSandbox}
		if _, err := st.IssueRelease(ctx, "legion", release, record.OutOfWorkflow); !errors.Is(err, wait.ErrWaiting) {
			t.Fatalf("the last read with the save queued = %v, want a wait for the save", err)
		}
		noTake(t, st, architect)
	})
}

// A teardown that fails after its last read and before the runtime touched the pod ends the take
// that read recorded (ClearIssuePodTakeTx, in the transaction that records the row's failure): the
// pod stands, the operator's take-over is written again, and the row's next last read reads it. A
// clear that names another release leaves this release's take as it stands.
func TestATakeEndsWithATeardownThatDidNotTouchThePod(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		other bool
	}{{name: "the release's own take ends"}, {name: "control: a clear naming another release ends nothing", other: true}} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			openRunningTree(t, st)
			markOfKind(t, st, rootIssue, releaseKindUnowned)
			architect := storedReleasedArchitect(t, st)
			if got := teardownRead(t, st, true); got.Act != ReleaseActs {
				t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
			}
			release := takenRelease(t, st)
			if tc.other {
				release++
			}
			if err := st.Tx(ctx, func(tx pgx.Tx) error {
				return st.ClearIssuePodTakeTx(ctx, tx, "legion", rootIssue, release, teardownLease(t, st))
			}); err != nil {
				t.Fatalf("end the take: %v", err)
			}
			err := st.PutClaim(ctx, takenOver(architect))
			if tc.other {
				refusedAsTaken(t, st, err, architect)
				return
			}
			if err != nil {
				t.Fatalf("the take-over once the take ended = %v, want it written: the pod stands", err)
			}
			if got := teardownRead(t, st, true); got.Act != ReleaseCancel || got.Cause != ReleaseCauseOperatorSuspended {
				t.Fatalf("the row's next last read = %s (cause %s), want cancel (cause %s): it reads the suspension",
					got.Act, got.Cause, ReleaseCauseOperatorSuspended)
			}
		})
	}
}

// takenPodVersion is the version of the migration that adds the take and its database rule.
const takenPodVersion = 20261120084333

// The migration of the take runs up and down: up, the database refuses a raw take-over past the
// last read; the way back its header carries (migrations.WayBack), run as one transaction with the
// daemon stopped, drops the rule, its function and the column and deletes its version row, so the
// same raw write lands as the release before this one writes it; and the roll forward applies it
// once more, after which the database refuses that write again.
func TestTheTakesMigrationRunsUpAndDown(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	openRunningTree(t, st)
	markOfKind(t, st, rootIssue, releaseKindUnowned)
	architect := storedReleasedArchitect(t, st)
	takeOver := func() error {
		_, err := st.Pool().Exec(ctx, "update claims set suspended_by = 'operator' where token = $1", string(architect.Token))
		return err
	}
	// schema reads each object the migration adds in pairs: the two columns (issue_releases.taken_pod
	// and claims.resume_owed_release), the two triggers and their two functions; each answer is
	// true only where both of its pair stand, and false only where neither does (mixed answers
	// fail the test as a way back that left half of a pair).
	schema := func() (recorded, column, rule, function bool) {
		t.Helper()
		var columns, rules, functions int
		if err := st.Pool().QueryRow(ctx, `select
			exists (select 1 from schema_version where version = $1),
			(select count(*) from information_schema.columns where (table_name, column_name) in
				(('issue_releases', 'taken_pod'), ('claims', 'resume_owed_release'))),
			(select count(*) from pg_trigger where tgname in ('claims_refused_past_a_release_take', 'claims_owed_resume_ends')),
			(select count(*) from pg_proc where proname in ('claims_refused_past_a_release_take', 'claims_owed_resume_ends'))`,
			takenPodVersion).Scan(&recorded, &columns, &rules, &functions); err != nil {
			t.Fatal(err)
		}
		for _, n := range []int{columns, rules, functions} {
			if n == 1 {
				t.Fatalf("the migration's objects stand in halves: columns %d, triggers %d, functions %d of 2 each", columns, rules, functions)
			}
		}
		return recorded, columns == 2, rules == 2, functions == 2
	}

	if recorded, column, rule, function := schema(); !recorded || !column || !rule || !function {
		t.Fatalf("up: version recorded %t, column %t, trigger %t, function %t; want all four", recorded, column, rule, function)
	}
	if got := teardownRead(t, st, true); got.Act != ReleaseActs {
		t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
	}
	refusedByTheDatabase(t, st, takeOver(), takenPodSuspensionCode)

	block, err := migrations.WayBack(takenPodVersion)
	if err != nil {
		t.Fatalf("read the way back of %d: %v", takenPodVersion, err)
	}
	if _, err := st.Pool().Exec(ctx, block, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("down: run the way back as its one transaction: %v\n%s", err, block)
	}
	if recorded, column, rule, function := schema(); recorded || column || rule || function {
		t.Fatalf("down: version recorded %t, column %t, trigger %t, function %t; want none of them", recorded, column, rule, function)
	}
	if err := takeOver(); err != nil {
		t.Fatalf("down: the earlier release's take-over = %v, want it written: nothing of the take is left", err)
	}

	applied, err := st.Migrate(ctx)
	if err != nil || applied != 1 {
		t.Fatalf("the roll forward applied %d migrations, %v; want this one again", applied, err)
	}
	if recorded, column, rule, function := schema(); !recorded || !column || !rule || !function {
		t.Fatalf("up again: version recorded %t, column %t, trigger %t, function %t; want all four", recorded, column, rule, function)
	}
	if _, err := st.Pool().Exec(ctx, `update claims set suspended_by = 'released' where token = $1`, string(architect.Token)); err != nil {
		t.Fatal(err)
	}
	if got := teardownRead(t, st, true); got.Act != ReleaseActs {
		t.Fatalf("the last read after the roll forward = %s (cause %s), want acts", got.Act, got.Cause)
	}
	refusedByTheDatabase(t, st, takeOver(), takenPodSuspensionCode)
}

// Every write that records a release's take of its pod or ends it names the lease the run that
// makes it holds the teardown's row by (RowLease). Daemons of a rolling restart meet on one row
// where a run's lease lapsed and another run claimed the row: the run that lost its lease records
// no take, ends none — the clear after a teardown that left the pod standing, the finish's delete
// of the mark, the cancel — and answers ErrRowLeaseLost, so the take the run holding the row
// recorded stands and an operator's suspension stays refused. The control is the run that holds
// the lease, whose own clear and finish end its take.
func TestEveryWriteOfATakeNamesTheLeaseOfTheRunThatHoldsIt(t *testing.T) {
	ctx := context.Background()
	// lapsed is the rig with the take recorded by the run that now holds row 10, and the lease of
	// the run before it, which lapsed.
	lapsed := func(t *testing.T) (*Store, supervise.Claim, RowLease) {
		t.Helper()
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		stale := teardownLease(t, st)
		if _, err := st.Pool().Exec(ctx, `update outbox set lease_token = 'the-run-that-claimed-the-row' where id = 10`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.IssueRelease(ctx, "legion", IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1,
			Row: 10, Takes: true, Sandbox: takenSandbox, Lease: "the-run-that-claimed-the-row"}, record.OutOfWorkflow); err != nil {
			t.Fatalf("the holding run's last read: %v", err)
		}
		return st, architect, stale
	}
	stands := func(t *testing.T, st *Store, architect supervise.Claim, after string) {
		t.Helper()
		if !releaseMarked(t, st) {
			t.Fatalf("after %s the mark is gone, want it standing with the holding run's take", after)
		}
		refusedAsTaken(t, st, st.PutClaim(ctx, takenOver(architect)), architect)
	}

	t.Run("a last read under a lapsed lease records no take", func(t *testing.T) {
		st := migratedStore(t)
		openRunningTree(t, st)
		markOfKind(t, st, rootIssue, releaseKindUnowned)
		architect := storedReleasedArchitect(t, st)
		stale := teardownLease(t, st)
		if _, err := st.Pool().Exec(ctx, `update outbox set lease_token = 'the-run-that-claimed-the-row' where id = 10`); err != nil {
			t.Fatal(err)
		}
		_, err := st.IssueRelease(ctx, "legion", IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1,
			Row: 10, Takes: true, Sandbox: takenSandbox, Lease: stale.Token}, record.OutOfWorkflow)
		if !errors.Is(err, ErrRowLeaseLost) {
			t.Fatalf("the lapsed run's last read = %v, want ErrRowLeaseLost", err)
		}
		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the take-over after the lapsed run's last read = %v, want it written: that read recorded no take", err)
		}
	})
	t.Run("the lapsed run's clear ends nothing", func(t *testing.T) {
		st, architect, stale := lapsed(t)
		release := takenRelease(t, st)
		err := st.Tx(ctx, func(tx pgx.Tx) error { return st.ClearIssuePodTakeTx(ctx, tx, "legion", rootIssue, release, stale) })
		if !errors.Is(err, ErrRowLeaseLost) {
			t.Fatalf("the lapsed run's clear = %v, want ErrRowLeaseLost", err)
		}
		stands(t, st, architect, "the lapsed run's clear")
	})
	t.Run("the lapsed run's finish deletes no mark", func(t *testing.T) {
		st, architect, stale := lapsed(t)
		if err := st.ClearIssueRelease(ctx, "legion", rootIssue, stale); !errors.Is(err, ErrRowLeaseLost) {
			t.Fatalf("the lapsed run's finish = %v, want ErrRowLeaseLost", err)
		}
		stands(t, st, architect, "the lapsed run's finish")
	})
	t.Run("the lapsed run's cancel deletes no mark", func(t *testing.T) {
		st, architect, stale := lapsed(t)
		_, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()}, stale)
		if !errors.Is(err, ErrRowLeaseLost) {
			t.Fatalf("the lapsed run's cancel = %v, want ErrRowLeaseLost", err)
		}
		stands(t, st, architect, "the lapsed run's cancel")
	})
	t.Run("control: the run that holds the lease ends its own take", func(t *testing.T) {
		st, architect, _ := lapsed(t)
		holder := RowLease{Row: 10, Token: "the-run-that-claimed-the-row"}
		release := takenRelease(t, st)
		if err := st.Tx(ctx, func(tx pgx.Tx) error { return st.ClearIssuePodTakeTx(ctx, tx, "legion", rootIssue, release, holder) }); err != nil {
			t.Fatalf("the holding run's clear = %v, want it to end the take", err)
		}
		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the take-over after the holding run's clear = %v, want it written", err)
		}
		if err := st.ClearIssueRelease(ctx, "legion", rootIssue, holder); err != nil || releaseMarked(t, st) {
			t.Fatalf("the holding run's finish = %v, marked %t; want the mark gone", err, releaseMarked(t, st))
		}
	})
}

// A take is of one project's issue pod: a mark of another project's issue of the same key that
// carries a take refuses no write of this project's claim, the operator's suspension and the
// launch's start among them, and the launch check passes it.
func TestATakeOfAnotherProjectsIssueOfTheSameKeyRefusesNothing(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	openRunningTree(t, st)
	architect := storedReleasedArchitect(t, st)
	if _, err := st.Pool().Exec(ctx, `insert into issue_releases
		(project, issue, generation, first_row, boundary_row, last_row, started_row, role, kind, suspended, marked_at, taken_pod)
		values ('another-project', $1, 1, 10, 10, 10, 0, '', $2, '{}', now(), $3)`, rootIssue, releaseKindUnowned, takenSandbox); err != nil {
		t.Fatalf("mark another project's release of %s with a take: %v", rootIssue, err)
	}
	if err := st.CheckLaunch(ctx, launched(architect)); err != nil {
		t.Fatalf("the launch check beside another project's take = %v, want it passed", err)
	}
	if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
		t.Fatalf("the operator's take-over beside another project's take = %v, want it written", err)
	}
	if err := st.PutClaim(ctx, launched(architect)); err != nil {
		t.Fatalf("the launch beside another project's take = %v, want it written", err)
	}
	if state, by := storedSource(t, st, architect); state != "launching" {
		t.Fatalf("the stored architect is %s by %s, want launching", state, by)
	}
}

// The operator's resume of a claim a release's take refused is owed while the take stands
// (OweResume), naming the release and the Sandbox, and is read as owed only once the take has
// ended (OwedResumes). A second owe changes nothing; the operator's later word ends it
// (ForgetResume), and so does the write that changes the claim's state or its suspension's source
// (the trigger claims_owed_resume_ends), the owed launch's own write among them. The control is an
// owe with no take standing, which records nothing and leaves the caller to launch.
func TestAnOwedResumeIsReadOnceTheTakeEndsAndEndsWithTheClaimsNextWrite(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	openRunningTree(t, st)
	markOfKind(t, st, rootIssue, releaseKindUnowned)
	architect := storedReleasedArchitect(t, st)
	owed := func() int64 {
		t.Helper()
		var release *int64
		if err := st.Pool().QueryRow(ctx, `select resume_owed_release from claims where token = $1`, string(architect.Token)).Scan(&release); err != nil {
			t.Fatal(err)
		}
		if release == nil {
			return 0
		}
		return *release
	}
	if release, _, err := st.OweResume(ctx, architect); err != nil || release != 0 || owed() != 0 {
		t.Fatalf("control: the owe with no take = release %d, %v, stored %d; want nothing owed", release, err, owed())
	}
	if got := teardownRead(t, st, true); got.Act != ReleaseActs {
		t.Fatalf("the last read = %s (cause %s), want acts", got.Act, got.Cause)
	}
	taken := takenRelease(t, st)
	for range 2 {
		if release, sandbox, err := st.OweResume(ctx, architect); err != nil || release != taken || sandbox != takenSandbox || owed() != taken {
			t.Fatalf("the owe past the take = release %d, Sandbox %q, %v, stored %d; want release %d and %s", release, sandbox, err, owed(), taken, takenSandbox)
		}
	}
	if tokens, err := st.OwedResumes(ctx, "legion", rootIssue); err != nil || len(tokens) != 0 {
		t.Fatalf("the owed resumes while the take stands = %v, %v; want none", tokens, err)
	}
	if forgot, err := st.ForgetResume(ctx, architect.Token); err != nil || !forgot || owed() != 0 {
		t.Fatalf("the forget = %t, %v, stored %d; want it forgotten", forgot, err, owed())
	}
	if forgot, err := st.ForgetResume(ctx, architect.Token); err != nil || forgot {
		t.Fatalf("a second forget = %t, %v; want nothing forgotten", forgot, err)
	}
	if _, _, err := st.OweResume(ctx, architect); err != nil {
		t.Fatal(err)
	}
	daemons := architect
	daemons.SuspendedBy = supervise.SuspendedByDaemon
	if err := st.PutClaim(ctx, daemons); err != nil || owed() != 0 {
		t.Fatalf("the daemon's suspension = %v, stored owe %d; want it written and the owe ended", err, owed())
	}
	if err := st.PutClaim(ctx, architect); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.OweResume(ctx, architect); err != nil || owed() != taken {
		t.Fatalf("the owe again = %v, stored %d", err, owed())
	}
	if err := st.ClearIssueRelease(ctx, "legion", rootIssue, teardownLease(t, st)); err != nil {
		t.Fatalf("finish the release: %v", err)
	}
	if tokens, err := st.OwedResumes(ctx, "legion", rootIssue); err != nil || len(tokens) != 1 || tokens[0] != architect.Token {
		t.Fatalf("the owed resumes once the release ended = %v, %v; want the architect", tokens, err)
	}
	if err := st.PutClaim(ctx, launched(architect)); err != nil || owed() != 0 {
		t.Fatalf("the owed launch's write = %v, stored owe %d; want it written and the owe ended", err, owed())
	}
	if tokens, err := st.OwedResumes(ctx, "legion", rootIssue); err != nil || len(tokens) != 0 {
		t.Fatalf("the owed resumes once the launch was written = %v, %v; want none", tokens, err)
	}
}
