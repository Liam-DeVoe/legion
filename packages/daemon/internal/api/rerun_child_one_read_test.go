package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// countingRecords counts the issues a route reads one at a time (Store.Issue).
type countingRecords struct {
	record.Store
	issues atomic.Int64
}

func (c *countingRecords) Issue(ctx context.Context, tx pgx.Tx, key string) (*record.Issue, error) {
	c.issues.Add(1)
	return c.Store.Issue(ctx, tx, key)
}

// rerun_child judges the child from one read of its own record, which authorizes the request, and
// the Dispatch chain it reads beside it (record.Chains), never the tree a record stores: the chain
// carries the accepted issue above the child the refusal names (record.AcceptedAncestorIn). The
// copy of Dispatch's issues holds the root's row, so the chain reaches it. The child is out of
// Legion's work by its owning architect's own backlog in Dispatch, which the daemon takes from
// that write's event (dispatch://INFRA-3178).
func TestRerunChildJudgesTheTreeFromOneRead(t *testing.T) {
	h, handlers, statuses := statusHarness(t)
	counting := &countingRecords{Store: record.NewStore()}
	h.handler = NewServer("127.0.0.1", 8437, Options{
		Supervisor: h.supervisor, BootTokens: h.tokens, Project: testProject, DispatchProject: testDispatchProject,
		OperatorToken: testOperatorToken, Controller: h.store, Grants: credential.New(nil), Pool: h.store.Pool(),
		Record: counting, Handlers: handlers, Dispatch: statuses,
	}).Handler
	seedMirrorRow(t, h, statusRoot, "in_progress")
	architect := newLiveClaim(t, h, statusRoot, claim.RoleArchitect)
	ownersBacklog(t, h, handlers, statuses, architect, statusChild)
	acceptPhase(t, h, statusRoot, phase.Merging)

	issues := counting.issues.Load()
	recorder := h.request(http.MethodPost, "/legion/v1/children/rerun", map[string]any{"grantId": architect.grant(t), "issue": statusChild}, nil)
	refused(t, recorder, http.StatusConflict, "CHILD_UNDER_ACCEPTED_PARENT")
	if read := counting.issues.Load() - issues; read != 1 {
		t.Fatalf("the refused rerun read %d issues one at a time, want 1: the child's own record, which authorizes the request", read)
	}
}

// rerun_child on a child the record still holds in Legion's work is refused CHILD_RUNNING, and the
// refusal says what the architect does next (dispatch://INFRA-3178/spec R1). The restart of a
// running child is the owning architect's backlog, then rerun_child, and the daemon takes that
// backlog from its Dispatch event, which the event consumer can deliver late. So the refusal reads
// the child from Dispatch once. Where Dispatch already shows a status that takes the child out of
// the workflow, the architect wrote its move and the daemon has not taken it yet, so the architect
// calls again once read_record shows the child at that status, its phase kept (R20 of
// dispatch://INFRA-3320). That reason also says which writes the daemon sets back, and that the
// architect writes backlog first when dispatch read no longer shows its move. Where Dispatch shows
// the child running, the architect writes backlog first. A failed read gives that second reason,
// and the refusal stands rather than failing 502. A rerun the record takes reads nothing from
// Dispatch. No reason names an operation the daemon no longer serves.
func TestRerunChildBeforeTheDaemonTookTheBacklogSaysToWait(t *testing.T) {
	wait := func(status string) string {
		return "Dispatch shows " + status + " for LEGION-209, but the daemon has not taken that move yet. The record still shows LEGION-209 in phase planning at status in_progress. " +
			"Call rerun_child again when read_record shows LEGION-209 at status " + status + ". " +
			"If dispatch read --issue LEGION-209 no longer shows " + status + ", the daemon set the move back. " +
			"In that case, write backlog on LEGION-209 first (dispatch issue-update --issue LEGION-209 --status backlog). " +
			"The daemon sets back a phase worker's write and a write by an architect that does not own LEGION-209. " +
			"It also sets back a write of the status that Dispatch already showed."
	}
	const restart = "LEGION-209 is in phase planning at status in_progress, so rerun_child cannot run it again now. The restart of a running child is backlog, then rerun_child. " +
		"Write backlog on LEGION-209 first (dispatch issue-update --issue LEGION-209 --status backlog). Then call rerun_child again when read_record shows LEGION-209 at status backlog."
	for _, tc := range []struct {
		name string
		// shows is the status Dispatch shows for the child, "" for a read that fails.
		shows string
		want  string
	}{
		{name: "Dispatch shows backlog", shows: "backlog", want: wait("backlog")},
		{name: "Dispatch shows icebox", shows: "icebox", want: wait("icebox")},
		{name: "Dispatch shows triage", shows: "triage", want: wait("triage")},
		{name: "Dispatch shows done", shows: "done", want: wait("done")},
		{name: "Dispatch shows the child running", shows: "in_progress", want: restart},
		{name: "the Dispatch read fails", want: restart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, statuses := statusHarness(t)
			architect := newLiveClaim(t, h, statusRoot, claim.RoleArchitect)
			if tc.shows == "" {
				statuses.readFails = map[string]error{statusChild: errors.New("Dispatch answered 503")}
			} else {
				shown := statuses.issues[statusChild]
				shown.Status, shown.LastSeq = tc.shows, 5
				statuses.issues[statusChild] = shown
			}
			statuses.reads = nil
			recorder := h.request(http.MethodPost, "/legion/v1/children/rerun", map[string]any{"grantId": architect.grant(t), "issue": statusChild}, nil)
			reason := refused(t, recorder, http.StatusConflict, "CHILD_RUNNING")
			if reason != tc.want {
				t.Fatalf("the CHILD_RUNNING refusal says %q, want %q", reason, tc.want)
			}
			if want := []string{"issue:" + statusChild}; !slices.Equal(statuses.reads, want) {
				t.Fatalf("the refusal read %v from Dispatch, want the child once, %v", statuses.reads, want)
			}
			if len(statuses.writes) != 0 {
				t.Fatalf("the refused rerun wrote %v to Dispatch, want nothing", statuses.writes)
			}
			if child := recordedChild(t, h, statusChild); child == nil || child.Phase != phase.Planning || child.Generation != 1 {
				t.Fatalf("the child after the refused rerun = %#v, want it in planning at generation 1 as before", child)
			}
		})
	}
	t.Run("a rerun the record takes", func(t *testing.T) {
		h, handlers, statuses := statusHarness(t)
		architect := newLiveClaim(t, h, statusRoot, claim.RoleArchitect)
		backlog := intake.DispatchIssue{Key: statusChild, Seq: 5, Type: "issue.updated", Status: "backlog", Title: "the child", Parent: statusRoot, Rank: "B",
			ResolvedRepo: "acme/widgets"}
		if _, err := intake.ApplyFact(context.Background(), h.store.Pool(), "dispatch", "person-backlog", backlog, handlers...); err != nil {
			t.Fatalf("apply a person's backlog of the child: %v", err)
		}
		shown := statuses.issues[statusChild]
		shown.Status, shown.LastSeq = "backlog", 5
		statuses.issues[statusChild] = shown
		if child := recordedChild(t, h, statusChild); child == nil || record.InLegionsWork(*child) {
			t.Fatalf("the child after a person's backlog = %#v, want it out of Legion's work: this case proves nothing", child)
		}
		statuses.reads = nil
		recorder := h.request(http.MethodPost, "/legion/v1/children/rerun", map[string]any{"grantId": architect.grant(t), "issue": statusChild}, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("rerun = %d %s, want 200", recorder.Code, recorder.Body)
		}
		if len(statuses.reads) != 0 {
			t.Fatalf("the rerun the record took read %v from Dispatch, want nothing", statuses.reads)
		}
		if want := []statusWrite{{issue: statusChild, status: "todo"}}; !slices.Equal(statuses.writes, want) {
			t.Fatalf("Dispatch writes = %v, want %v", statuses.writes, want)
		}
	})
}
