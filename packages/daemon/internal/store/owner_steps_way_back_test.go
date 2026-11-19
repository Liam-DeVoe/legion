package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ownerStepsMigration is the migration whose header carries the way back of the release whose
// owner's plan picks the steps. docs/kubernetes.md carries the same block under "Rolling back the
// release whose owner's plan picks the steps", and internal/textcheck's
// TestTheOwnerStepsWayBackIsOneTextInTheHeaderAndTheGuide holds the two to one text. This file
// reads the header alone, for the reason planRatingsMigration's test does.
const ownerStepsMigration = "20261117034837_issue_plans.up.sql"

// The heredoc an operator runs the way back in, and its terminator, as the header writes them.
const (
	ownerStepsHeredoc    = "psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1 <<'SQL'"
	ownerStepsTerminator = "SQL"
)

// ownerStepsProjectPredicate is how each queue delete of the way back names the project whose daemon
// rolls back: the project of a row is the part of its issue column before the first hyphen, since a
// project key holds none.
const ownerStepsProjectPredicate = " and split_part(issue, '-', 1) = '<PROJECT>'"

// ownerStepsWayBack is the transaction the header's heredoc sends (indentedWayBack): the lines
// between the heredoc's opener and its terminator, which name the project as <PROJECT>. A block of
// another shape stops the test by name.
func ownerStepsWayBack(t *testing.T) string {
	t.Helper()
	lines := strings.Split(indentedWayBack(t, ownerStepsMigration), "\n")
	if len(lines) < 2 || lines[0] != ownerStepsHeredoc || lines[len(lines)-1] != ownerStepsTerminator {
		t.Fatalf("the way back of %s is %q, want a heredoc opened by %q and closed by %q", ownerStepsMigration, lines, ownerStepsHeredoc,
			ownerStepsTerminator)
	}
	return strings.Join(lines[1:len(lines)-1], "\n")
}

// noticeKindsBeforeOwnerSteps is the list of the notice kinds an architect is told in the release
// this rollback goes back to (validNoticeKinds at commit 407c7a4162): every kind of this release's
// list but the three it adds. superviseOpsBeforeOwnerSteps is that release's list of supervise ops,
// which has no step_stop. Both are copies because that release's code is not in the tree. Its list
// of the kinds for the controller alone is this release's (controllerOnlyNoticeKind), unchanged.
var (
	noticeKindsBeforeOwnerSteps = []record.NoticeKind{
		"phase-finished", "worker-died", "worker-stalled", "worker-silent", "worker-background-stalled", "held",
		"pr-blocked", "pr-merged", "pr-closed-unmerged", "pull-request-missing", "message-refused",
		"design-approved", "design-changes-requested", "carried-gate-opened",
		"child-closed", "child-status", "children-first", "child-release-stalled", "child-not-recorded",
		"catch-up", "checks-red", "review-stuck", "status-reasserted",
		"session-lost", "session-store-fault",
	}
	controllerKindsBeforeOwnerSteps = []record.NoticeKind{
		record.TriageNotice, record.SlotFreeNotice, record.TodoNotice, record.TickNotice, record.UnslottedNotice,
		record.AwaitingPersonNotice, record.ParkedDecisionNotice, record.UpdateStalledNotice, record.ProviderErrorsNotice,
		record.TreeStalledNotice, record.ArchitectStalledNotice,
	}
	superviseOpsBeforeOwnerSteps = []record.SuperviseOp{"start", "suspend", "release", "tree_close", "issue_close"}
)

// refusedBeforeOwnerSteps is why the release before refuses row, "" where it takes it. That
// release decodes a notice, a controller notice and a supervise row strictly (record's
// DecodeOutboxPayload, DisallowUnknownFields), so it refuses a kind not on its list, a notice naming
// the architect that stopped a task (stoppedBy, a field it does not hold) and an op not on its list,
// and it is no permanent refusal of those kinds, so it fails such a row at every attempt.
func refusedBeforeOwnerSteps(t *testing.T, row record.OutboxRow) string {
	t.Helper()
	payload, err := record.DecodeOutboxPayload(row)
	if err != nil {
		t.Fatalf("this release's decode of row %d: %v", row.ID, err)
	}
	switch value := payload.(type) {
	case record.Notice:
		if !slices.Contains(noticeKindsBeforeOwnerSteps, value.Kind) {
			return fmt.Sprintf("unknown notice kind %q", value.Kind)
		}
		if value.StoppedBy != "" {
			return `unknown field "stoppedBy"`
		}
	case record.ControllerNotice:
		if !slices.Contains(noticeKindsBeforeOwnerSteps, value.Kind) && !slices.Contains(controllerKindsBeforeOwnerSteps, value.Kind) {
			return fmt.Sprintf("unknown controller notice kind %q", value.Kind)
		}
		if value.StoppedBy != "" {
			return `unknown field "stoppedBy"`
		}
	case record.SuperviseRequest:
		if !slices.Contains(superviseOpsBeforeOwnerSteps, value.Op) {
			return fmt.Sprintf("unknown supervise operation %q", value.Op)
		}
	}
	return ""
}

// The way back of the release whose owner's plan picks the steps deletes the queued rows the release
// before cannot read, for the project whose daemon rolls back alone: its notices and controller
// notices of the three kinds this release adds (task-stopped, retro-held, plan-not-taken), and its
// stops of a stopped task's step (step_stop). The outbox is shared by every project's daemon on the
// database, so another project's rows of the same kinds stand, and so does every row of a kind the
// release before reads, of either project. The rows are queued for two projects, LEGION, which rolls
// back, and OTHER, which does not, and the block runs as the migration's header carries it, as one
// exec, with <PROJECT> LEGION.
//
// Two controls. Before the block, the release before's decode (refusedBeforeOwnerSteps) refuses each
// row of a new kind and takes each other row, so the test sees the rows the way back exists for.
// And the same block with its project predicate taken off deletes OTHER's rows of the new kinds too:
// that predicate, and nothing else, is what keeps them.
func TestTheOwnerStepsWayBackDeletesOnlyItsProjectsRowsTheReleaseBeforeCannotRead(t *testing.T) {
	ctx := context.Background()
	queuedAt := time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)
	rowsOf := func(project string) []struct {
		name    string
		payload record.OutboxPayload
		newKind bool
	} {
		key, tree := project+"-208", project+"-208"
		stopper := claim.Token(strings.ToLower(project) + "-" + strings.ToLower(project) + "-" + strings.ToLower(key) + "-architect")
		return []struct {
			name    string
			payload record.OutboxPayload
			newKind bool
		}{
			{name: "the notice of an owner's stop", newKind: true, payload: record.Notice{Kind: "task-stopped", Role: claim.RoleTester, Phase: phase.Testing,
				StoppedBy: stopper, Reason: "task a of " + key + " was stopped by its owner"}},
			{name: "the notice of a stop no step could return from", newKind: true, payload: record.Notice{Kind: "task-stopped", Role: claim.RoleTester,
				Phase: phase.Testing, Reason: "task a of " + key + " was stopped: step 1 (tester) failed"}},
			{name: "the notice of a retro held for the issues beneath", newKind: true, payload: record.Notice{Kind: "retro-held",
				Role: claim.RoleImplementer, Phase: phase.Retro, Reason: key + " passed its last step"}},
			{name: "the notice of a plan not taken", newKind: true, payload: record.Notice{Kind: "plan-not-taken", Role: claim.RoleArchitect,
				Reason: "The first step of " + key + " took no plan of an earlier owner"}},
			{name: "the controller's copy of a stop's notice", newKind: true, payload: record.ControllerNotice{Kind: "task-stopped",
				Role: claim.RoleTester, Phase: phase.Testing, Reason: "task a of " + key + " was stopped"}},
			{name: "the stop of a stopped task's step", newKind: true, payload: record.SuperviseRequest{Op: "step_stop", Tree: tree,
				Role: claim.RoleTester, Generation: 1, Reason: "task a of " + key + " was stopped by its owner"}},
			{name: "a phase-finished notice", payload: record.Notice{Kind: "phase-finished", Role: claim.RoleImplementer, Phase: phase.Implementing}},
			{name: "a held notice for the controller", payload: record.ControllerNotice{Kind: "held", Role: claim.RoleImplementer, Phase: phase.Implementing}},
			{name: "a suspend", payload: record.SuperviseRequest{Op: "suspend", Tree: tree, Role: claim.RoleImplementer, Generation: 1}},
		}
	}
	scoped := ownerStepsWayBack(t)
	unscoped := strings.ReplaceAll(scoped, ownerStepsProjectPredicate, "")
	for _, tc := range []struct {
		name  string
		block string
		// otherKept says whether OTHER's rows of the new kinds stand after the block.
		otherKept bool
	}{
		{name: "control: the block without its project predicate deletes the other project's rows too", block: unscoped},
		{name: "the block deletes the rows of the project that rolls back alone", block: scoped, otherKept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.otherKept && unscoped == scoped {
				t.Fatalf("the way back of %s holds no %q to take off: no delete of it names the project that rolls back", ownerStepsMigration,
					ownerStepsProjectPredicate)
			}
			store := emptyStore(t)
			if _, err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			type queuedRow struct {
				project, name string
				newKind       bool
				id            int64
			}
			var queued []queuedRow
			for _, project := range []string{"LEGION", "OTHER"} {
				for _, q := range rowsOf(project) {
					row, err := record.NewOutboxRow(project+"-208", q.payload, queuedAt)
					if err != nil {
						t.Fatalf("queue %s of %s: %v", q.name, project, err)
					}
					var id int64
					if err := store.Pool().QueryRow(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
						values ($1, $2, $3, 0, $4, '') returning id`, string(row.Kind), row.Issue, row.Payload, row.NextAt).Scan(&id); err != nil {
						t.Fatalf("insert %s of %s: %v", q.name, project, err)
					}
					row.ID = id
					if refused := refusedBeforeOwnerSteps(t, row); (refused != "") != q.newKind {
						t.Fatalf("the release before's decode of %s of %s refuses it with %q; the control wants it to refuse the rows of the new kinds alone, "+
							"or this test cannot see the rows the way back exists for", q.name, project, refused)
					}
					queued = append(queued, queuedRow{project: project, name: q.name, newKind: q.newKind, id: id})
				}
			}

			if _, err := store.Pool().Exec(ctx, strings.ReplaceAll(tc.block, "<PROJECT>", "LEGION"), pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatalf("run the way back %s's header carries: %v", ownerStepsMigration, err)
			}

			for _, q := range queued {
				var stands bool
				if err := store.Pool().QueryRow(ctx, `select exists (select 1 from outbox where id = $1)`, q.id).Scan(&stands); err != nil {
					t.Fatalf("read row %d: %v", q.id, err)
				}
				kept := !q.newKind || (q.project == "OTHER" && tc.otherKept)
				switch {
				case kept && !stands:
					t.Errorf("the way back deleted %s of %s (row %d), want it kept", q.name, q.project, q.id)
				case !kept && stands:
					t.Errorf("the way back left %s of %s (row %d), want it deleted", q.name, q.project, q.id)
				}
			}
			var tables, version bool
			if err := store.Pool().QueryRow(ctx, `select to_regclass('issue_plans') is not null or to_regclass('plan_tasks') is not null
				or to_regclass('plan_steps') is not null, exists (select 1 from schema_version where version = 20261117034837)`).Scan(&tables, &version); err != nil {
				t.Fatalf("read the schema after the way back: %v", err)
			}
			if tables || version {
				t.Errorf("after the way back a plan table stands (%v) or the migration's version row does (%v), want neither", tables, version)
			}
		})
	}
}
