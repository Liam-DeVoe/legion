package daemon

// A delivery the project release held reaches its claim after the claim's start
// (dispatch://INFRA-3583 R8), and a row the release holds spends no attempt. A release ends with
// its process, so the tests of a delivery stand in the next process: the claim they read is one
// that process's boot found stored suspended by `project_release` (releaseStoppedClaims), and the
// set, the outbox and the supervisor's durable-state callback are wired as attach wires them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// releaseDeliveries is the three rows that publish to a role's own topic and that the outbox ends
// when nobody holds it: each names its recipient and builds its row for that recipient's claim.
var releaseDeliveries = []struct {
	name    string
	role    claim.Role
	payload func(claim.Token) record.OutboxPayload
}{
	{"a phase-ended message", claim.RoleTester, func(claim.Token) record.OutboxPayload {
		return record.PhaseEnded{Role: claim.RoleTester, From: phase.Testing, To: phase.Implementing, Reason: "the head conflicts with main"}
	}},
	{"a role message", claim.RoleImplementer, func(token claim.Token) record.OutboxPayload {
		return record.RoleMessage{Role: string(token), Body: "CI settled at the head"}
	}},
	{"a hook prompt", claim.RoleImplementer, func(claim.Token) record.OutboxPayload { return firedHook }},
}

// releaseDeliveryRig is an outbox over an isolated database holding LEGION-208 in implementing,
// with a listener that answers each role topic as held or unheld, and a clock the test moves.
type releaseDeliveryRig struct {
	pool      *store.Pool
	records   record.Store
	sup       *supervisor
	runner    *outbox
	publisher *holderPublisher
	at        time.Time
}

// newReleaseDeliveryRig wires the rig as attach wires the daemon: boot is the claims the boot read
// from the store, which give the set of those a project release stopped; the outbox reads that
// set, and the supervisor's durable-state callback, the daemon's own, takes a claim out of it.
func newReleaseDeliveryRig(t *testing.T, boot ...supervise.Claim) *releaseDeliveryRig {
	t.Helper()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	putOutboxIssue(t, pool, records, record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "Workflow", Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "U"})
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	stopped := newReleaseStoppedClaims(boot)
	sup.OnDurableState((&workflowRuntime{pool: pool, records: records, log: quietLogger(), releaseStopped: stopped}).claimDurableState)
	rig := &releaseDeliveryRig{pool: pool, records: records, sup: sup, publisher: &holderPublisher{}, at: time.Now().UTC()}
	rig.runner = &outbox{dispatchProject: "LEGION", pool: pool, records: records, supervisor: sup, project: "legion",
		notices: rig.publisher, log: quietLogger(), now: func() time.Time { return rig.at }, releaseStopped: stopped}
	return rig
}

// stoppedByRelease is role's claim on LEGION-208 as a project release leaves it in the store:
// suspended with the source `project_release`, on the session it ran in.
func stoppedByRelease(t *testing.T, role claim.Role) supervise.Claim {
	t.Helper()
	return supervise.Claim{Token: mustClaimToken(t, "LEGION-208", role), Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208",
		Role: role, Generation: 1, ServingGeneration: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByProjectRelease,
		Session: "ses-" + string(role), SessionFile: "/tmp/" + string(role) + ".jsonl"}
}

// supervise gives held its machine, as the boot's restore gives each stored claim one.
func (r *releaseDeliveryRig) supervise(t *testing.T, held supervise.Claim) *supervise.Machine {
	t.Helper()
	machine, _, err := r.sup.Create(context.Background(), held, "")
	if err != nil {
		t.Fatalf("supervise %s: %v", held.Token, err)
	}
	return machine
}

// enqueue writes payload's row for LEGION-208, due at the rig's clock.
func (r *releaseDeliveryRig) enqueue(t *testing.T, payload record.OutboxPayload) {
	t.Helper()
	enqueueOutbox(t, r.pool, r.records, mustOutboxRow(t, "LEGION-208", payload, r.at))
}

// run moves the clock past any backoff and runs every row due then, as a lane runs each.
func (r *releaseDeliveryRig) run(t *testing.T) {
	t.Helper()
	r.at = r.at.Add(5 * time.Minute)
	runDue(t, r.runner, r.at)
}

// waits runs the row and requires that it waited: nothing was published or tried, and the row is
// still in the outbox with a wait as its last error.
func (r *releaseDeliveryRig) waits(t *testing.T, when string) {
	t.Helper()
	r.run(t)
	if attempts, delivered := r.publisher.snapshot(); attempts != 0 || len(delivered) != 0 {
		t.Fatalf("%s: publish attempts = %d with %d delivered, want none: the row waits for the claim's start", when, attempts, len(delivered))
	}
	if _, lastError, present := outboxRowState(t, r.pool, "LEGION-208"); !present || !strings.Contains(lastError, "waits for") {
		t.Fatalf("%s: the row is present=%t with %q, want it waiting in the outbox", when, present, lastError)
	}
}

// endsUndelivered runs the row and requires the outbox's rule for a role nobody holds: one
// refused publish, nothing delivered beyond what was, and the row finished.
func (r *releaseDeliveryRig) endsUndelivered(t *testing.T, when string, attemptsBefore, deliveredBefore int) {
	t.Helper()
	r.run(t)
	if attempts, delivered := r.publisher.snapshot(); attempts != attemptsBefore+1 || len(delivered) != deliveredBefore {
		t.Fatalf("%s: publish attempts = %d with %d delivered, want %d and %d: one refused publish to a role nobody holds",
			when, attempts, len(delivered), attemptsBefore+1, deliveredBefore)
	}
	if left := outboxRows(t, r.pool); left != 0 {
		t.Fatalf("%s: outbox rows left = %d, want the row finished undelivered", when, left)
	}
}

// A role message, a phase-ended message and a hook prompt for a claim the boot found stopped by
// the project release each wait while that claim has not come back: before the boot's start of it
// has run, and while the start's process is launching, connected and registered, when no session
// holds the role's topic and a publish would end the row undelivered. Once the claim is ready the
// row is published to the role. The claim is then out of the set, so when its process dies later
// and it is relaunching, a new row nobody holds ends undelivered, as it does for any other claim.
func TestADeliveryHeldThroughAProjectReleaseReachesItsClaimAfterItsStart(t *testing.T) {
	for _, tc := range releaseDeliveries {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			stored := stoppedByRelease(t, tc.role)
			rig := newReleaseDeliveryRig(t, stored)
			machine := rig.supervise(t, stored)
			topic := notify.RoleTopicPrefix + string(stored.Token)
			rig.publisher.setAbsent(topic)
			rig.enqueue(t, tc.payload(stored.Token))

			rig.waits(t, "before the boot's start of the claim has run")

			rig.sup.deps.Conns.(*fake.Conns).Register(stored.Token, fake.NewConn())
			if err := machine.Handle(ctx, supervise.RequestRuleResume{Claim: stored.Token, Reason: "the boot starts a claim the project release stopped"}); err != nil {
				t.Fatalf("start the claim: %v", err)
			}
			generation := machine.Claim().Generation
			for _, step := range []struct {
				state supervise.ClaimState
				event supervise.Event
			}{
				{supervise.StateLaunching, nil},
				{supervise.StateShimConnected, supervise.StreamHello{Claim: stored.Token, Generation: generation}},
				{supervise.StateRegistered, supervise.RequestRegister{Claim: stored.Token, Generation: generation, Session: stored.Session, SessionFile: stored.SessionFile}},
			} {
				if step.event != nil {
					if err := machine.Handle(ctx, step.event); err != nil {
						t.Fatalf("handle %T: %v", step.event, err)
					}
				}
				if got := machine.Claim().State; got != step.state {
					t.Fatalf("the claim is %s, want %s", got, step.state)
				}
				rig.waits(t, "while the claim is "+string(step.state))
			}

			if err := machine.Handle(ctx, supervise.RequestReady{Claim: stored.Token, Generation: generation, Session: stored.Session}); err != nil {
				t.Fatalf("make the claim ready: %v", err)
			}
			rig.publisher.setAbsent()
			rig.run(t)
			attempts, delivered := rig.publisher.snapshot()
			if attempts != 1 || len(delivered) != 1 || delivered[0].topic != topic {
				t.Fatalf("once the claim is ready: %d publish attempts delivered %+v, want the one publish to %s", attempts, delivered, topic)
			}
			if left := outboxRows(t, rig.pool); left != 0 {
				t.Fatalf("once the claim is ready: outbox rows left = %d, want the row finished", left)
			}

			if err := machine.Handle(ctx, supervise.RuntimeObservation{Observation: runtime.Observation{Locator: *machine.Claim().Locator, Kind: runtime.Gone}}); err != nil {
				t.Fatalf("report the claim's process gone: %v", err)
			}
			if got := machine.Claim().State; got != supervise.StateLaunching {
				t.Fatalf("after its process died the claim is %s, want it relaunching", got)
			}
			rig.publisher.setAbsent(topic)
			rig.enqueue(t, tc.payload(stored.Token))
			rig.endsUndelivered(t, "while the claim that came back relaunches", attempts, len(delivered))
		})
	}
}

// A claim no project release stopped keeps the outbox's rule for a role nobody holds while it
// comes up: the boot read it launching or registered, so it is not in the set, and its row is
// published once, refused, and finished undelivered.
func TestADeliveryForAClaimNoReleaseStoppedEndsWhenNobodyHoldsItsRole(t *testing.T) {
	for _, tc := range releaseDeliveries {
		for _, state := range []supervise.ClaimState{supervise.StateLaunching, supervise.StateRegistered} {
			t.Run(tc.name+" for a claim that is "+string(state), func(t *testing.T) {
				// The claim as the boot read it: coming up, and stopped by no release.
				held := stoppedByRelease(t, tc.role)
				held.State, held.SuspendedBy = state, ""
				rig := newReleaseDeliveryRig(t, held)
				rig.supervise(t, held)
				rig.publisher.setAbsent(notify.RoleTopicPrefix + string(held.Token))
				rig.enqueue(t, tc.payload(held.Token))

				rig.endsUndelivered(t, "for a claim no release stopped", 0, 0)
			})
		}
	}
}

// A claim the boot found stopped by the project release that will not come back holds no row: one
// that has failed, one that has retired, and one an operator or the daemon has suspended since
// each leave the row to the rule for a role nobody holds, which ends it.
func TestADeliveryStopsWaitingForAStoppedClaimThatWillNotComeBack(t *testing.T) {
	for _, tc := range releaseDeliveries {
		for _, now := range []struct {
			name  string
			state supervise.ClaimState
			by    supervise.SuspendSource
		}{
			{"has failed", supervise.StateFailed, ""},
			{"has retired", supervise.StateRetired, ""},
			{"an operator has suspended", supervise.StateSuspended, supervise.SuspendedByOperator},
			{"the daemon has suspended", supervise.StateSuspended, supervise.SuspendedByDaemon},
		} {
			t.Run(tc.name+" for a stopped claim that "+now.name, func(t *testing.T) {
				stored := stoppedByRelease(t, tc.role)
				rig := newReleaseDeliveryRig(t, stored)
				held := stored
				held.State, held.SuspendedBy = now.state, now.by
				rig.supervise(t, held)
				rig.publisher.setAbsent(notify.RoleTopicPrefix + string(held.Token))
				rig.enqueue(t, tc.payload(held.Token))

				rig.endsUndelivered(t, "for a stopped claim that "+now.name, 0, 0)
			})
		}
	}
}

// heldRow is the one outbox row of issue as the store holds it.
type heldRow struct {
	attempts  int
	nextAt    time.Time
	lastError string
	leased    bool
}

func (r heldRow) String() string {
	return fmt.Sprintf("%d attempts, leased=%t, due at %s, last error %q", r.attempts, r.leased, r.nextAt.UTC().Format(time.RFC3339Nano), r.lastError)
}

func readHeldRow(t *testing.T, pool *store.Pool, issue string) (heldRow, bool) {
	t.Helper()
	var row heldRow
	err := pool.QueryRow(context.Background(), `select attempts, next_at, last_error, lease_token <> '' or lease_until is not null
		from outbox where issue = $1`, issue).Scan(&row.attempts, &row.nextAt, &row.lastError, &row.leased)
	if errors.Is(err, pgx.ErrNoRows) {
		return heldRow{}, false
	}
	if err != nil {
		t.Fatalf("read the outbox row of %s: %v", issue, err)
	}
	return row, true
}

// A row the project release holds is put back unrun as often as its lane reads it, and none of
// those reads is an attempt: the row keeps the attempts it had, its lease is cleared, and it is
// due again one fixed spacing later. A merge-queue read shows what that keeps: it bounds its reads
// of GitHub by the row's attempts (mergeStateLadderReads), so after a release that held it more
// often than that bound, the next process's first failed read is the first rung of the ladder and
// the row reads again, where a counted hold would have ended the ladder and told the merger that
// no read succeeded. The hold is logged once at info and at debug from then on.
func TestARowTheProjectReleaseHoldsSpendsNoAttempt(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
		Tree: "LEGION-208", Title: "Workflow", Phase: phase.Merging, Generation: 1, Status: "in_progress", Rank: "U"}
	putOutboxIssue(t, pool, records, issue)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return records.PutPullRequest(ctx, tx, record.PullRequest{State: record.PullRequestOpen, Issue: issue.Key, Repo: "acme/widgets",
			Number: 86, Branch: "legion/LEGION-208", HeadSHA: "sha-1", HeadUpdatedAt: time.Now(), Failing: []string{},
			CheckRuns: []record.AttemptRun{}, Base: "main"})
	}); err != nil {
		t.Fatalf("record the pull request: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	reads := 0
	var logged bytes.Buffer
	runner := func(release *projectRelease) *outbox {
		return &outbox{pool: pool, dispatchProject: "LEGION", records: records, project: "legion", release: release,
			log: slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
			now: func() time.Time { return at },
			readPullRequest: func(context.Context, record.PullRequest) (githubPullRequest, error) {
				reads++
				return githubPullRequest{}, errors.New("GitHub answered 502")
			},
			mergeStateWait: func() time.Duration { return time.Second }}
	}
	enqueueOutbox(t, pool, records, mustOutboxRow(t, issue.Key, record.MergeQueueRead{Number: 86, Head: "sha-1"}, at))

	released := runner(releasedMark(t))
	const holds = mergeStateLadderReads + 1
	for hold := 1; hold <= holds; hold++ {
		if ran := runDue(t, released, at); ran != 1 {
			t.Fatalf("hold %d: rows due = %d, want the held row due again", hold, ran)
		}
		row, present := readHeldRow(t, pool, issue.Key)
		if !present || row.attempts != 0 || row.leased || !row.nextAt.Equal(at.Add(releaseHoldSpacing)) ||
			!strings.Contains(row.lastError, api.ProjectReleaseHeldSentence) {
			t.Fatalf("hold %d: the row is present=%t with %s; want it unleased with no attempt, due at %s on the release's wait",
				hold, present, row, at.Add(releaseHoldSpacing).Format(time.RFC3339Nano))
		}
		at = row.nextAt
	}
	if reads != 0 {
		t.Fatalf("reads of GitHub while released = %d, want none", reads)
	}
	var levels []string
	for _, line := range strings.Split(strings.TrimSpace(logged.String()), "\n") {
		var entry struct{ Level, Msg string }
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode the log line %q: %v", line, err)
		}
		if entry.Msg == "outbox row waits" {
			levels = append(levels, entry.Level)
		}
	}
	if want := append([]string{"INFO"}, slices.Repeat([]string{"DEBUG"}, holds-1)...); !slices.Equal(levels, want) {
		t.Fatalf("the held row's wait was logged at %v, want %v", levels, want)
	}

	if ran := runDue(t, runner(&projectRelease{}), at); ran != 1 {
		t.Fatalf("rows run by the next process = %d, want the read", ran)
	}
	row, present := readHeldRow(t, pool, issue.Key)
	if reads != 1 || !present || row.attempts != 1 ||
		!strings.Contains(row.lastError, fmt.Sprintf("after the read of attempt 1 of %d failed", mergeStateLadderReads)) {
		t.Fatalf("after the next process's first failed read: %d reads, the row present=%t with %s; want one read and the row waiting to read again as attempt 1 of %d",
			reads, present, row, mergeStateLadderReads)
	}
}
