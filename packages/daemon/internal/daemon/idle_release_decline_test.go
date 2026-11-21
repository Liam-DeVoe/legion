package daemon

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// idleRigUnpushed is what the rig's shim reads in a workspace that holds one commit no branch or
// tag on origin holds: the working copy sits on it and holds no change of its own.
var idleRigUnpushed = runtime.WorkspaceWork{Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", Count: 1,
	Commits: []string{"9b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c"}}

// idleRigPasses is how many idle passes a test runs where it asserts that a pass did nothing: each
// one a pass of the rule over the pod and a drain of the outbox after it, as the daemon's own idle
// tick and outbox runner make them.
const idleRigPasses = 6

// passes runs n idle passes over the rig's pod, each followed by a drain of the outbox.
func (r *idleRig) passes(t *testing.T, n int) {
	t.Helper()
	for range n {
		r.w.releaseIdlePods(context.Background())
		r.drain(t)
	}
}

// sequenceValue is the last id the sequence of table's id column handed out: an insert into that
// table takes the next one, so a value that does not move is a table nothing was inserted into,
// rows written and deleted again included.
func (r *idleRig) sequenceValue(t *testing.T, table string) int64 {
	t.Helper()
	var last *int64
	if err := r.pool.QueryRow(context.Background(), `select pg_sequence_last_value(pg_get_serial_sequence($1, 'id')::regclass)`,
		table).Scan(&last); err != nil {
		t.Fatalf("read the id sequence of %s: %v", table, err)
	}
	if last == nil {
		return 0
	}
	return *last
}

// roleMessages is every message the outbox published to the topic of the rig's role.
func roleMessages(publisher *outboxPublisher, token string) []string {
	var messages []string
	for _, published := range publisher.publishes() {
		if published.topic == notify.RoleTopicPrefix+token {
			messages = append(messages, published.message)
		}
	}
	return messages
}

// A release whose check declines the pod asks the pod once. The decline holds the pod in the idle
// view (pods.IdleDecline.Stands) until a role of the issue starts a turn or a process again, so the
// passes after it mark no release, write no outbox row, ask no shim and tell no role. Before it,
// each pass marked the release again, its check read the workspace again, and its decline wrote a
// hold and a message every time.
//
// no_turn: the first pass's release reads the workspace, finds one commit origin does not hold,
// and declines: one hold of the pod under unpushed_work and one message to the role the workspace
// was read through, the open phase's worker. The passes after it do nothing at all.
//
// turn_after_hold: then the role starts a turn after the decline, which also makes the pod
// recorded-to and so not idle. Once the pod is idle again, the pass marks a release and its check
// reads the workspace again, once: the work is still there, the check declines again, and the
// passes after that ask nothing more.
func TestADeclinedIdleReleaseAsksThePodOnceUntilATurn(t *testing.T) {
	ctx := context.Background()
	// declined is a rig whose first pass has run and whose release the check has declined, with
	// what that pass did read back: the decline's own moment, and the publisher that holds the
	// message.
	declined := func(t *testing.T) (*idleRig, *outboxPublisher, time.Time) {
		t.Helper()
		rig := newIdleRig(t)
		publisher := &outboxPublisher{}
		rig.runner.notices = publisher
		rig.rt.ScriptWorkspaceWork(rig.issue.Key, idleRigUnpushed)

		rig.passes(t, 1)

		if read, suspended := rig.rt.reads(); len(read) != 1 || suspended != 0 {
			t.Fatalf("the first pass read the workspace %d times and took %d pods, want one read and no pod", len(read), suspended)
		}
		if rig.marked(t) {
			t.Fatal("the declined release left its mark behind")
		}
		var at time.Time
		var reason string
		if err := rig.pool.QueryRow(ctx, `select declined_at, reason from idle_release_declines where issue = $1`,
			rig.issue.Key).Scan(&at, &reason); err != nil || reason != pods.DeclineUnpushedWork {
			t.Fatalf("the recorded decline = %q, %v; want %s", reason, err, pods.DeclineUnpushedWork)
		}
		holds := rig.records(t).Holds
		if len(holds) != 1 || holds[0].Reason != workflow.IdleHoldUnpushedWork.String() {
			t.Fatalf("holds = %+v, want the one %s", holds, workflow.IdleHoldUnpushedWork)
		}
		messages := roleMessages(publisher, string(rig.token))
		if len(messages) != 1 || !strings.Contains(messages[0], idleRigUnpushed.Commits[0]) {
			t.Fatalf("messages to %s = %q, want one naming the commit %s", rig.token, messages, idleRigUnpushed.Commits[0])
		}
		if got := rig.machine.Claim(); got.State == supervise.StateSuspended {
			t.Fatalf("the claim = %s by %s, want it left running in the pod the decline kept", got.State, got.SuspendedBy)
		}
		return rig, publisher, at
	}

	t.Run("no_turn", func(t *testing.T) {
		rig, publisher, _ := declined(t)
		releases, rows := rig.sequenceValue(t, "issue_releases"), rig.sequenceValue(t, "outbox")
		left := rig.rows(t)

		rig.passes(t, idleRigPasses)

		if read, _ := rig.rt.reads(); len(read) != 1 {
			t.Fatalf("the workspace was read %d times across %d passes after the decline, want only the first pass's read", len(read), idleRigPasses+1)
		}
		if after := rig.sequenceValue(t, "issue_releases"); after != releases || rig.marked(t) {
			t.Fatalf("the passes after the decline marked a release (the mark sequence moved from %d to %d, marked now %t), want none",
				releases, after, rig.marked(t))
		}
		if after := rig.sequenceValue(t, "outbox"); after != rows {
			t.Fatalf("the passes after the decline queued outbox rows (the sequence moved from %d to %d), want none", rows, after)
		}
		if now := rig.rows(t); strings.Join(now, ",") != strings.Join(left, ",") {
			t.Fatalf("outbox rows after the passes = %v, want the %v the decline's pass left", now, left)
		}
		if messages := roleMessages(publisher, string(rig.token)); len(messages) != 1 {
			t.Fatalf("messages to %s after the passes = %d, want the decline's one", rig.token, len(messages))
		}
		holds := rig.records(t).Holds
		if len(holds) != 1 || holds[0].Reason != workflow.IdleHoldUnpushedWork.String() || holds[0].Passes < 2 {
			t.Fatalf("holds = %+v, want one %s streak that the passes after the decline counted on", holds, workflow.IdleHoldUnpushedWork)
		}
	})

	t.Run("turn_after_hold", func(t *testing.T) {
		rig, _, at := declined(t)
		rig.passes(t, 2)
		if read, _ := rig.rt.reads(); len(read) != 1 {
			t.Fatalf("the workspace was read %d times before the turn, want only the first pass's read", len(read))
		}

		// The role starts a turn a moment after the decline, as the message it was sent asks it
		// to. The claim's write is what makes the pod recorded-to, so the pod is not idle again
		// until the idle period has passed from there.
		turn := at.Add(time.Microsecond)
		if _, err := rig.pool.Exec(ctx, `update claims set last_turn_start = $2, updated_at = $2 where token = $1`,
			string(rig.token), turn); err != nil {
			t.Fatalf("start the role's turn: %v", err)
		}
		rig.passes(t, 2)
		if read, _ := rig.rt.reads(); len(read) != 1 {
			t.Fatalf("the workspace was read %d times while the pod was not idle, want only the first pass's read", len(read))
		}

		// The idle period passes from the turn, with the listener still reporting.
		later := time.Now().Add(rig.w.idlePeriod + 5*time.Minute)
		rig.w.now = func() time.Time { return later }
		if _, err := rig.pool.Exec(ctx, `update listener_reports set ready_at = $2 where project = $1`, "legion", later); err != nil {
			t.Fatalf("move the listener's report on: %v", err)
		}
		rig.passes(t, idleRigPasses)

		if read, _ := rig.rt.reads(); len(read) != 2 {
			t.Fatalf("the workspace was read %d times in all, want the first pass's read and exactly one more after the turn", len(read))
		}
		if rig.marked(t) {
			t.Fatal("the second release's decline left its mark behind")
		}
		var again time.Time
		if err := rig.pool.QueryRow(ctx, `select declined_at from idle_release_declines where issue = $1`, rig.issue.Key).Scan(&again); err != nil ||
			!again.After(turn) {
			t.Fatalf("the decline after the turn reads %s (%v), want a decline later than the turn at %s", again, err, turn)
		}
	})
}

// R5 of the release that keeps no saved workspace, its first test: a workspace with one commit
// that origin does not hold keeps its pod. The release's check reads the commit and declines
// before any role stops, so the pod, its disk and the workspace on it stay as they were: the
// runtime takes no pod down and stops no process, the claim keeps the process it was running in
// that pod, and the next turn of the role is a turn in that same pod, whose workspace still holds
// the commit. The role is told which commit holds the release.
func TestAnIdleReleaseDeclinesAWorkspaceWithOneUnpushedCommitAndKeepsThePod(t *testing.T) {
	rig := newIdleRig(t)
	publisher := &outboxPublisher{}
	rig.runner.notices = publisher
	rig.rt.ScriptWorkspaceWork(rig.issue.Key, idleRigUnpushed)
	before := rig.machine.Claim()
	if before.Locator == nil {
		t.Fatal("the rig's implementer runs no process to keep")
	}
	sandbox, present := rig.rt.IssueSandbox(rig.issue.Key)
	if !present {
		t.Fatal("the rig's runtime holds no Sandbox for the issue")
	}
	launches := len(rig.rt.CallsOf("Spawn")) + len(rig.rt.CallsOf("Resume"))

	rig.passes(t, idleRigPasses)

	if asked := rig.rt.teardownsAsked(); len(asked) != 0 {
		t.Fatalf("teardowns asked = %v, want none: the pod holds a commit origin does not", asked)
	}
	if stops := rig.rt.CallsOf("Release"); len(stops) != 0 {
		t.Fatalf("the runtime stopped %d role processes, want none: the check declines before any role stops", len(stops))
	}
	if read, _ := rig.rt.reads(); len(read) != 1 {
		t.Fatalf("the workspace was read %d times, want once", len(read))
	}
	after := rig.machine.Claim()
	if after.State == supervise.StateSuspended {
		t.Fatalf("the claim = %s by %s, want it live in the pod it was in", after.State, after.SuspendedBy)
	}
	if after.Locator == nil || !reflect.DeepEqual(*after.Locator, *before.Locator) {
		t.Fatalf("the claim's process = %+v, want the %+v it had before the release: the role's next turn is in the same pod",
			after.Locator, before.Locator)
	}
	if now, present := rig.rt.IssueSandbox(rig.issue.Key); !present || now != sandbox {
		t.Fatalf("the issue's Sandbox = %q (present %t), want the %q it had: the pod and its disk stay", now, present, sandbox)
	}
	if relaunched := len(rig.rt.CallsOf("Spawn")) + len(rig.rt.CallsOf("Resume")); relaunched != launches {
		t.Fatalf("launches = %d, want the %d before the release: the role's process was never stopped, so nothing is launched again",
			relaunched, launches)
	}
	if records := rig.records(t); len(records.Releases) != 0 {
		t.Fatalf("releases recorded = %+v, want none", records.Releases)
	}
	messages := roleMessages(publisher, string(rig.token))
	if len(messages) != 1 || !strings.Contains(messages[0], idleRigUnpushed.Commits[0]) || !strings.Contains(messages[0], "legion/"+rig.issue.Key) {
		t.Fatalf("messages to %s = %q, want one naming the commit %s and the issue's branch", rig.token, messages, idleRigUnpushed.Commits[0])
	}
}

// R5 of the release that keeps no saved workspace, its second test: the wake task of a role whose
// clean pod was released says what the role's plugin notice says. The release's check reads no
// work, its role stop and its teardown take the pod, and a message published to the role's topic
// wakes it. The wake's start row carries the task, which tells the role of its workspace in the
// words workflow.WorkspaceAfterRelease holds. That text is the data file the plugin's test reads
// (workflow/workspace-after-release.txt) byte for byte, and the plugin's own notice
// (packages/pi-shared/src/workspace-recreated.ts) carries its sentence about what the release
// takes. Neither says the workspace's unpushed work was kept.
func TestAReleasedCleanPodsWakeTaskSaysWhatThePluginNoticeSays(t *testing.T) {
	rig := newIdleRig(t)
	ctx := context.Background()

	rig.passes(t, 1)

	if read, suspended := rig.rt.reads(); len(read) != 1 || suspended != 1 {
		t.Fatalf("the release read the workspace %d times and took %d pods, want one read and the pod", len(read), suspended)
	}
	if got := rig.machine.Claim(); !got.Released() {
		t.Fatalf("the claim after the release = %s by %s, want it released", got.State, got.SuspendedBy)
	}
	tables, err := rig.st.PodRecordsOf(rig.issue.Project)
	if err != nil {
		t.Fatalf("read the pod tables: %v", err)
	}
	engine := workflow.New(rig.issues, workflow.Config{Project: rig.issue.Project, Repositories: testRepositories,
		Steps: config.DesignGateRootIssues.Steps(), Pods: tables}, quietLogger())
	if result, err := intake.ApplyFact(ctx, rig.pool, "wake", "role-message-1", intake.ListenerWake{Kind: intake.ListenerWakeRole,
		Role: string(rig.token), At: time.Now().UTC(), Report: "role-message-1"}, engine); err != nil || result.Refusal != nil {
		t.Fatalf("apply the listener's report of a role message = %#v, %v", result, err)
	}
	var task string
	if err := rig.pool.QueryRow(ctx, `select payload->>'task' from outbox where issue = $1 and kind = 'supervise'
		and payload->>'op' = 'start' and payload->>'role' = 'implementer' order by id desc limit 1`, rig.issue.Key).Scan(&task); err != nil {
		t.Fatalf("read the wake's start row: %v", err)
	}
	preamble := "Your issue's pod had been released while it was idle, and this wake brought it back. " +
		workflow.WorkspaceAfterRelease + " Your session is as you left it. "
	if !strings.HasPrefix(task, preamble) {
		t.Fatalf("the wake's task = %q, want it to open with %q", task, preamble)
	}
	for _, kept := range []string{"any work you had not pushed", "saved", "restored", "put back"} {
		if strings.Contains(task, kept) {
			t.Fatalf("the wake's task = %q, which says %q: the daemon keeps none of the workspace", task, kept)
		}
	}

	data, err := os.ReadFile("../workflow/workspace-after-release.txt")
	if err != nil {
		t.Fatalf("read the shared text: %v", err)
	}
	if string(data) != workflow.WorkspaceAfterRelease+"\n" {
		t.Fatalf("workspace-after-release.txt = %q, want the wake task's text %q and one newline", data, workflow.WorkspaceAfterRelease)
	}
	notice, err := os.ReadFile("../../../pi-shared/src/workspace-recreated.ts")
	if err != nil {
		t.Fatalf("read the plugin's notice: %v", err)
	}
	const takes = "The idle release takes a pod only when its workspace holds nothing that was not pushed."
	if !strings.HasPrefix(workflow.WorkspaceAfterRelease, takes) || !strings.Contains(string(notice), takes) {
		t.Fatalf("the wake task's text and the plugin's notice do not both say %q", takes)
	}
}
