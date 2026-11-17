package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// These two tests are a check of their own (dispatch://INFRA-3589, check l2), and its negative
// control runs them at the commit before that change. So they name only what that commit has:
// the retry's delay and limit are literal durations here, and the log lines string literals.

// releaseStore is the store over the rig's pool, for the release's own writes: the mark
// (ReleaseIdleIssue), a role stop's record (ReleaseSuspendedClaim) and the cancel
// (CancelIssueRelease).
func releaseStore(t *testing.T, pool *pgxpool.Pool) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the store over the test pool: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// markRelease marks an idle release of LEGION-208's pod as the idle tick does, with the
// implementer's stop as its boundary and the teardown after it, and answers the boundary row: the
// row the stop that suspends the implementer runs as.
func markRelease(t *testing.T, st *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	project := wakeProjectToken(t)
	marked, err := st.ReleaseIdleIssue(ctx, project, "LEGION-208", func(pods.IdleView) (store.IdleReleaseMark, error) {
		return store.IdleReleaseMark{Generation: wakeIssueGeneration, Role: claim.RoleImplementer, Rows: []store.IdleReleaseRow{
			{Kind: "supervise", Payload: []byte(`{"op":"release","tree":"LEGION-208","role":"implementer","generation":7}`), Boundary: true},
			{Kind: "issue_release", Payload: []byte(`{"tree":"LEGION-208","generation":7,"treeGeneration":7}`)},
		}}, nil
	})
	if err != nil || !marked {
		t.Fatalf("ReleaseIdleIssue = %t, %v; want the release marked", marked, err)
	}
	var boundary int64
	if err := st.Pool().QueryRow(ctx, `select boundary_row from issue_releases where project = $1 and issue = 'LEGION-208'`,
		project).Scan(&boundary); err != nil {
		t.Fatalf("read the release's mark: %v", err)
	}
	return boundary
}

// setClaimState puts the claim of LEGION-208's role in state, suspended by source ("" for a
// claim that is not suspended).
func setClaimState(t *testing.T, pool *pgxpool.Pool, role claim.Role, state, source string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `update claims set state = $2, suspended_by = $3 where token = $1`,
		string(wakeToken(t, role)), state, source); err != nil {
		t.Fatalf("set the %s claim %s: %v", role, state, err)
	}
}

// applyWake applies one listener report as the intake does, and answers its error: the rig's own
// apply fails the test on an error, and a retry is an error.
func applyWake(rig wakeRig, id string, fact intake.Fact) error {
	_, err := intake.ApplyFact(context.Background(), rig.pool, "api", id, fact, rig.engine, admissionStub{})
	return err
}

// processedEvent is whether the intake recorded event id as applied.
func processedEvent(t *testing.T, pool *pgxpool.Pool, id string) bool {
	t.Helper()
	var processed bool
	if err := pool.QueryRow(context.Background(), `select exists (select 1 from processed_events where event_id = $1)`,
		id).Scan(&processed); err != nil {
		t.Fatalf("read processed_events: %v", err)
	}
	return processed
}

// loggedLines is every line the engine logged at level that holds message.
func loggedLines(rig wakeRig, level, message string) []string {
	var lines []string
	for line := range strings.SplitSeq(rig.logged.String(), "\n") {
		if strings.Contains(line, "level="+level) && strings.Contains(line, message) {
			lines = append(lines, line)
		}
	}
	return lines
}

// R3: a wake report that comes while a release has stopped the role's process and has not yet
// recorded the claim as released is applied again every 30 seconds until the release ends, for
// at most 15 minutes after the report. A cancelled release's queued start brings the role back
// with no wake of its own, and a wake for a claim in no release is applied once. A wake taken
// minutes after its report records the take's time as the resume's time, so the stuck rule does
// not count the wait.
func TestAWakeDuringAReleaseIsAppliedAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		// fact is the report, and name what the woken role's task must name.
		fact func(t *testing.T) (intake.ListenerWake, string)
	}{
		{name: "(a) an agent wake before the release records the claim", kind: pods.WakeAgentMessage,
			fact: func(*testing.T) (intake.ListenerWake, string) {
				return intake.ListenerWake{Kind: intake.ListenerWakeAgent, Session: "session-implementer", At: wakeAt, Report: "report-agent"}, "session-implementer"
			}},
		{name: "(b) a role wake before the release records the claim", kind: pods.WakeRoleMessage,
			fact: func(t *testing.T) (intake.ListenerWake, string) {
				token := string(wakeToken(t, claim.RoleImplementer))
				return intake.ListenerWake{Kind: intake.ListenerWakeRole, Role: token, At: wakeAt, Report: "report-role"}, token
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWakeRig(t)
			st := releaseStore(t, rig.pool)
			fact, name := tc.fact(t)
			// The claim stands live while its stop runs: the release has stopped the process and
			// has not recorded the claim as released.
			setClaimState(t, rig.pool, claim.RoleImplementer, "idle", "")
			boundary := markRelease(t, st)

			err := applyWake(rig, "wake", fact)
			var later *intake.RetryLater
			if !errors.As(err, &later) || later.After != 30*time.Second {
				t.Fatalf("applying the wake during the release = %v, want a retry after 30s: the wake finds no released claim yet, and dropping it leaves the pod released", err)
			}
			if processedEvent(t, rig.pool, "wake") {
				t.Fatal("the retried wake is recorded as applied, want it unrecorded so its redelivery applies")
			}
			if starts := wakeStarts(t, rig.pool); len(starts) != 0 {
				t.Fatalf("supervise start rows = %+v, want none while the wake waits", starts)
			}
			if resumes := wakeResumes(t, rig.pool); len(resumes) != 0 {
				t.Fatalf("pod resumes = %v, want none while the wake waits", resumes)
			}

			// The release records the claim as released, and the wake is delivered again.
			setClaimState(t, rig.pool, claim.RoleImplementer, "suspended", "released")
			if err := st.ReleaseSuspendedClaim(context.Background(), wakeProjectToken(t), "LEGION-208", claim.RoleImplementer, boundary); err != nil {
				t.Fatalf("record the claim the release suspended: %v", err)
			}
			if err := applyWake(rig, "wake", fact); err != nil {
				t.Fatalf("applying the redelivered wake = %v, want it applied", err)
			}
			oneWake(t, rig.pool, claim.RoleImplementer, tc.kind, name)
		})
	}

	t.Run("(c) a wake after a cancelled release takes nothing", func(t *testing.T) {
		rig := newWakeRig(t)
		st := releaseStore(t, rig.pool)
		ctx := context.Background()
		project := wakeProjectToken(t)
		// The release has recorded the implementer as released, and is then cancelled before the
		// start its cancel queued runs.
		boundary := markRelease(t, st)
		if err := st.ReleaseSuspendedClaim(ctx, project, "LEGION-208", claim.RoleImplementer, boundary); err != nil {
			t.Fatalf("record the claim the release suspended: %v", err)
		}
		resume := func(role claim.Role, generation uint64) (store.IdleReleaseRow, error) {
			row, err := record.NewOutboxRow("LEGION-208", record.SuperviseRequest{Op: "start", Tree: "LEGION-208",
				Role: role, Generation: generation}, time.Now())
			if err != nil {
				return store.IdleReleaseRow{}, err
			}
			return store.IdleReleaseRow{Kind: string(row.Kind), Payload: row.Payload}, nil
		}
		resumed, _, err := st.CancelIssueRelease(ctx, project, "LEGION-208", resume, record.OutOfWorkflow)
		if err != nil || !slices.Equal(resumed, []claim.Role{claim.RoleImplementer}) {
			t.Fatalf("CancelIssueRelease = %v, %v; want the implementer's resume queued", resumed, err)
		}
		var state, source string
		if err := rig.pool.QueryRow(ctx, `select state, suspended_by from claims where token = $1`,
			string(wakeToken(t, claim.RoleImplementer))).Scan(&state, &source); err != nil {
			t.Fatalf("read the implementer's claim: %v", err)
		}
		if state != "suspended" || source != "released" {
			t.Fatalf("the implementer's claim after the cancel = %s by %q, want suspended by released until its start runs", state, source)
		}

		fact := intake.ListenerWake{Kind: intake.ListenerWakeAgent, Session: "session-implementer", At: wakeAt, Report: "report-agent"}
		if err := applyWake(rig, "wake", fact); err != nil {
			t.Fatalf("applying the wake after the cancel = %v, want it applied", err)
		}
		starts := wakeStarts(t, rig.pool)
		if len(starts) != 1 || starts[0] != (wakeStart{Role: claim.RoleImplementer}) {
			t.Fatalf("supervise start rows = %+v, want the cancel's one start of the implementer with no task", starts)
		}
		if resumes := wakeResumes(t, rig.pool); len(resumes) != 0 {
			t.Fatalf("pod resumes = %v, want none: the pod never went away", resumes)
		}
		const coming = "wake: the role is coming back on a start already queued; this wake takes nothing"
		if lines := loggedLines(rig, "INFO", coming); len(lines) != 1 || !strings.Contains(lines[0], "issue=LEGION-208") {
			t.Fatalf("INFO lines %q = %q, want one naming LEGION-208; log:\n%s", coming, lines, rig.logged.String())
		}
	})

	t.Run("(d) a release that has not ended after 15 minutes drops the wake", func(t *testing.T) {
		rig := newWakeRig(t)
		st := releaseStore(t, rig.pool)
		setClaimState(t, rig.pool, claim.RoleImplementer, "idle", "")
		markRelease(t, st)

		at := wakeAt.Add(-(15*time.Minute + time.Second))
		fact := intake.ListenerWake{Kind: intake.ListenerWakeAgent, Session: "session-implementer", At: at, Report: "report-agent"}
		if err := applyWake(rig, "wake", fact); err != nil {
			t.Fatalf("applying the wake 15 minutes and 1 second after its report = %v, want it dropped with no retry", err)
		}
		const dropped = "wake: a wake that came during a release was dropped: the release did not end within 15 minutes of the report"
		if lines := loggedLines(rig, "WARN", dropped); len(lines) != 1 || !strings.Contains(lines[0], "issue=LEGION-208") {
			t.Fatalf("WARN lines %q = %q, want one naming LEGION-208; log:\n%s", dropped, lines, rig.logged.String())
		}
		if starts := wakeStarts(t, rig.pool); len(starts) != 0 {
			t.Fatalf("supervise start rows = %+v, want none for a dropped wake", starts)
		}
		if resumes := wakeResumes(t, rig.pool); len(resumes) != 0 {
			t.Fatalf("pod resumes = %v, want none for a dropped wake", resumes)
		}
	})

	t.Run("(e) a wake for a claim in no release does nothing once", func(t *testing.T) {
		rig := newWakeRig(t)
		setClaimState(t, rig.pool, claim.RoleImplementer, "idle", "")

		fact := intake.ListenerWake{Kind: intake.ListenerWakeAgent, Session: "session-implementer", At: wakeAt, Report: "report-agent"}
		if err := applyWake(rig, "wake", fact); err != nil {
			t.Fatalf("applying a wake for a live claim in no release = %v, want it applied once with no retry", err)
		}
		if !processedEvent(t, rig.pool, "wake") {
			t.Fatal("the wake is not recorded as applied, want it applied once")
		}
		if starts := wakeStarts(t, rig.pool); len(starts) != 0 {
			t.Fatalf("supervise start rows = %+v, want none for a live claim", starts)
		}
		if resumes := wakeResumes(t, rig.pool); len(resumes) != 0 {
			t.Fatalf("pod resumes = %v, want none for a live claim", resumes)
		}
	})

	t.Run("(j) a wake taken minutes after its report counts its resume from the take", func(t *testing.T) {
		rig := newWakeRig(t)
		st := releaseStore(t, rig.pool)
		ctx := context.Background()
		project := wakeProjectToken(t)
		setClaimState(t, rig.pool, claim.RoleImplementer, "idle", "")
		boundary := markRelease(t, st)

		// The report was made 9 minutes and 30 seconds before the engine's clock (wakeAt), and
		// it waited for the release in that time.
		report := wakeAt.Add(-(9*time.Minute + 30*time.Second))
		fact := intake.ListenerWake{Kind: intake.ListenerWakeAgent, Session: "session-implementer", At: report, Report: "report-agent"}
		var later *intake.RetryLater
		if err := applyWake(rig, "wake", fact); !errors.As(err, &later) {
			t.Fatalf("applying the wake during the release = %v, want a retry: the release has not recorded the claim", err)
		}
		setClaimState(t, rig.pool, claim.RoleImplementer, "suspended", "released")
		if err := st.ReleaseSuspendedClaim(ctx, project, "LEGION-208", claim.RoleImplementer, boundary); err != nil {
			t.Fatalf("record the claim the release suspended: %v", err)
		}
		if err := applyWake(rig, "wake", fact); err != nil {
			t.Fatalf("applying the redelivered wake = %v, want it applied", err)
		}
		oneWake(t, rig.pool, claim.RoleImplementer, pods.WakeAgentMessage, "session-implementer")

		var at time.Time
		if err := rig.pool.QueryRow(ctx, `select at from pod_resumes where issue = 'LEGION-208'`).Scan(&at); err != nil {
			t.Fatalf("read the resume's time: %v", err)
		}
		if !at.Equal(wakeAt) {
			t.Fatalf("the resume's time = %s, want the take's time %s, not the report's time %s",
				at.UTC().Format(time.RFC3339), wakeAt.Format(time.RFC3339), report.Format(time.RFC3339))
		}
		// A stuck pass at a moment reads each resume taken StrandedReleaseWake or more before that
		// moment (internal/daemon's warnStuckResumes). One minute after the take, the pod is still
		// being built, so the resume is not stuck. Counted from the report, it would be stuck.
		if stuck, err := st.StuckPodResumes(ctx, project, wakeAt.Add(time.Minute-StrandedReleaseWake)); err != nil || len(stuck) != 0 {
			t.Fatalf("the stuck resumes one minute after the take = %+v, %v; want none: the wait for the release is not the pod's", stuck, err)
		}
		if stuck, err := st.StuckPodResumes(ctx, project, wakeAt); err != nil || len(stuck) != 1 {
			t.Fatalf("the stuck resumes %s after the take = %+v, %v; want the one resume", StrandedReleaseWake, stuck, err)
		}
	})
}

// R4: a woken role reads a text that is true whether or not Envoy stored the message that woke
// it. The CI settlement and backstop texts do not change.
func TestAWokenRoleReadsATrueWakeText(t *testing.T) {
	const preamble = "Your issue's pod had been released while it was idle, and this wake brought it back. " +
		"Your session, your workspace and any work you had not pushed are as you left them. "

	wokenTask := func(t *testing.T, fact intake.ListenerWake) string {
		t.Helper()
		rig := newWakeRig(t)
		rig.apply("wake", fact)
		starts := wakeStarts(t, rig.pool)
		if len(starts) != 1 {
			t.Fatalf("supervise start rows = %+v, want the one wake", starts)
		}
		return starts[0].Task
	}
	holds := func(t *testing.T, task string, sentences ...string) {
		t.Helper()
		for _, sentence := range sentences {
			if !strings.Contains(task, sentence) {
				t.Errorf("the woken role's task = %q, want it to say %q", task, sentence)
			}
		}
	}

	t.Run("(f) an agent message wake", func(t *testing.T) {
		task := wokenTask(t, intake.ListenerWake{Kind: intake.ListenerWakeAgent, Session: "session-implementer", At: wakeAt, Report: "report-agent"})
		holds(t, task,
			"The wake is a message somebody sent to your session (session-implementer) while your pod was gone.",
			"A message that Envoy stored for your session reaches this session, for 72 hours after Envoy stored it.",
			"A message that Envoy did not store, such as a direct send that Envoy refused, does not reach you, and its sender must send it again.")
	})

	t.Run("(g) a role message wake", func(t *testing.T) {
		token := string(wakeToken(t, claim.RoleImplementer))
		task := wokenTask(t, intake.ListenerWake{Kind: intake.ListenerWakeRole, Role: token, At: wakeAt, Report: "report-role"})
		holds(t, task,
			"The wake is a message somebody published to your role topic ("+token+") while your pod was gone.",
			"A message that Envoy kept for your role reaches this session, for one hour after Envoy kept it.",
			"When that hour ends before a session takes the message, Envoy drops it and tells its sender.",
			"A message that Envoy did not keep, such as one sent with keep: false, does not reach you, and its sender must send it again.")
	})

	t.Run("(h) neither says nothing kept the message", func(t *testing.T) {
		for _, kind := range []string{pods.WakeAgentMessage, pods.WakeRoleMessage} {
			if task := WakeTask(kind, "detail"); strings.Contains(task, "nothing kept") {
				t.Errorf("WakeTask(%s) = %q, want no claim that nothing kept the message", kind, task)
			}
		}
	})

	t.Run("(i) the CI settlement and backstop texts are unchanged", func(t *testing.T) {
		for _, tc := range []struct{ kind, detail, want string }{
			{kind: pods.WakeChecks, detail: "head1 failed build",
				want: preamble + "The wake is a CI settlement on your pull request's head: head1 failed build. Read the checks before you decide anything."},
			{kind: pods.WakeBackstop, detail: "the backstop armed for LEGION-208's implementer came due",
				want: preamble + "The wake is the release's backstop running out: the backstop armed for LEGION-208's implementer came due. " +
					"Nothing else asked for you, so take up your phase where you left it."},
		} {
			if got := WakeTask(tc.kind, tc.detail); got != tc.want {
				t.Errorf("WakeTask(%s) = %q, want %q", tc.kind, got, tc.want)
			}
		}
	})
}
