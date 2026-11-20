package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

// takenSandbox is the Sandbox the teardowns of these tests take, as the runtime names it.
var takenSandbox = "legion-" + strings.ToLower(rootIssue)

// storedReleasedArchitect is the root issue's architect claim an earlier release left
// `released`, stored: the suspended claim an operator's suspend takes over (supervise's
// suspendedAgain).
func storedReleasedArchitect(t *testing.T, st *Store) supervise.Claim {
	t.Helper()
	issue, role := rootIssue, claim.RoleArchitect
	token, err := claim.NewToken("legion", issue, role)
	if err != nil {
		t.Fatal(err)
	}
	c := supervise.Claim{Token: token, Project: "legion", Tree: rootIssue, Issue: issue, Role: role, TreeEpoch: 1,
		Generation: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByReleased}
	if err := st.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("store the released %s claim of %s: %v", role, issue, err)
	}
	return c
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
// when last is set.
func teardownRead(t *testing.T, st *Store, last bool) ReleaseDecision {
	t.Helper()
	release := IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1, Row: 10}
	if last {
		release.Takes, release.Sandbox = true, takenSandbox
	}
	decision, err := st.IssueRelease(context.Background(), "legion", release, record.OutOfWorkflow)
	if err != nil {
		t.Fatalf("the teardown's read (last %t): %v", last, err)
	}
	return decision
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

		if err := st.ClearIssueRelease(ctx, "legion", rootIssue); err != nil {
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

		done := make(chan struct{})
		var decision ReleaseDecision
		var readErr error
		go func() {
			defer close(done)
			decision, readErr = st.IssueRelease(ctx, "legion", IssueReleased{Issue: rootIssue, Tree: rootIssue,
				IssueGeneration: 1, TreeGeneration: 1, Row: 10, Takes: true, Sandbox: takenSandbox}, record.OutOfWorkflow)
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
}

// The guard is in the claim write itself (putClaim), so every write of a claim meets it, whichever
// decision makes it: each exported write that can carry an operator's suspension of a stopped
// claim is refused once the last read has taken the pod, and writes nothing. A live claim the
// operator suspends passes, and so does a claim already the operator's, rewritten.
func TestEveryClaimWriteRefusesAnOperatorsTakeOverOfATakenPod(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			st, architect := taken(t)
			refusedAsTaken(t, st, tc.write(st, takenOver(architect)), architect)
		})
	}

	t.Run("a live claim the operator suspends passes", func(t *testing.T) {
		st, architect := taken(t)
		if _, err := st.Pool().Exec(ctx, `update claims set state = 'ready', suspended_by = '' where token = $1`, string(architect.Token)); err != nil {
			t.Fatal(err)
		}
		if err := st.PutClaim(ctx, takenOver(architect)); err != nil {
			t.Fatalf("the operator's suspension of a live claim = %v, want it written: the guard is for a stopped claim's take-over", err)
		}
	})

	t.Run("a claim already the operator's is rewritten", func(t *testing.T) {
		st, architect := taken(t)
		if _, err := st.Pool().Exec(ctx, `update claims set suspended_by = 'operator' where token = $1`, string(architect.Token)); err != nil {
			t.Fatal(err)
		}
		again := takenOver(architect)
		again.Session = "ses-rewritten"
		if err := st.PutClaim(ctx, again); err != nil {
			t.Fatalf("a rewrite of the operator's suspension = %v, want it written: it takes nothing over", err)
		}
	})
}

// suspensionWrites are the SQL literals of src, a Go file, that write claims.suspended_by: an
// update of claims whose set list names it, or an insert into claims whose column list does.
func suspensionWrites(src string) []string {
	var found []string
	for _, literal := range regexp.MustCompile("(?s)`[^`]*`").FindAllString(src, -1) {
		sql := strings.ToLower(literal)
		if m := regexp.MustCompile(`(?s)\bupdate\s+claims(\s+\w+)?\s+set\b(.*?)(\bwhere\b|$)`).FindStringSubmatch(sql); m != nil &&
			strings.Contains(m[2], "suspended_by") {
			found = append(found, literal)
			continue
		}
		if m := regexp.MustCompile(`(?s)\binsert\s+into\s+claims\s*\((.*?)\)`).FindStringSubmatch(sql); m != nil &&
			strings.Contains(m[1], "suspended_by") {
			found = append(found, literal)
		}
	}
	return found
}

// The guard holds for every path only while upsertClaim is the one statement of the daemon that
// writes a claim's suspension source: a statement of another path that wrote claims.suspended_by
// would pass it by. So every SQL literal of the daemon's own code is read for such a write, and
// the one found must be upsertClaim's. The control plants a raw update of the source and a read of
// it: the update must be found and the read not, and the scan must find upsertClaim's, so a scan
// that read no file reads as a failure, not as a pass.
func TestOnlyTheGuardedClaimWriteWritesASuspensionSource(t *testing.T) {
	if got := suspensionWrites("x := `update claims set state = 'suspended', suspended_by = 'operator' where token = $1`"); len(got) != 1 {
		t.Fatalf("control: the planted update of claims.suspended_by found %d times, want once", len(got))
	}
	if got := suspensionWrites("x := `select 1 from claims where project = $1 and suspended_by = $4`"); len(got) != 0 {
		t.Fatalf("control: a read of claims.suspended_by found as a write: %v", got)
	}
	var writes []string
	files := 0
	err := filepath.WalkDir(filepath.Join("..", ".."), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		for range suspensionWrites(string(src)) {
			writes = append(writes, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the daemon's sources: %v", err)
	}
	if files == 0 || len(writes) != 1 || filepath.Base(writes[0]) != "claims.go" {
		t.Fatalf("read %d files; statements that write claims.suspended_by are in %v, want the one in claims.go (upsertClaim), which putClaim's guard (refuseOperatorTakeOver) runs before",
			files, writes)
	}
	t.Logf("read %d Go files of the daemon; the one write of claims.suspended_by is in %s", files, writes[0])
}
