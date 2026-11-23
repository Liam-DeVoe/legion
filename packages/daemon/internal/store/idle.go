package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

// idleWaitingKinds are the outbox kinds that reach a role's own session, which is what "a task, a
// notice, a message or a start waits for a role of the pod" means: a supervise row (a start, the
// task it carries, a stop), an architect's notice, the words a hook fired on a move back puts in
// front of a role (record.HookPrompt), the ask an update round puts to a role of the pod
// (record.UpdateAsk), the message that tells the merger its pull request left the merge queue
// (record.MergeQueueRemoval), and each message of a merge forward, the ask to the implementer or,
// in merging, the merger, and the word to the worker of the phase the issue stays in
// (record.MergeForwardMessage). Two reads of GitHub reach no session themselves, but their
// answers are what write a message, so each holds the pod as its message does: the read that
// comes before a merge-queue removal's message (record.MergeQueueRead), and the second read of a
// head a first read found conflicting, whose confirmation writes a merge forward's messages
// (record.ConflictConfirm). The issue's board writes — its Dispatch status, its messages on the
// Dispatch issue — reach no session and hold no pod. The same set is what the release's boundary
// is read over (IssueRelease), so the rule that holds a pod and the rule that cancels its release
// never disagree about which row speaks to a role. Every kind of the role-message lane belongs
// here: one left out lets the daemon release the
// issue's pod out from under the row that was just written for one of its roles, and that role
// never reads it. An update ask holds the pod only while its row is pending, which is one attempt
// where the publish goes through or finds no holder.
var idleWaitingKinds = []string{"supervise", "notice", "hook_prompt", "update_ask", "merge_queue_read", "merge_queue_removal",
	"conflict_confirm", "merge_forward"}

// idleWaitingRow is the predicate, over the outbox with idleWaitingKinds as $2, of a row that
// waits for a role of the pod, and so holds the pod and cancels its release at the boundary
// (readIdleView, IssueReleaseStop), and, where it is a start, at the teardown (IssueRelease): a
// row of one of those kinds, but a start queued for news of the approved design alone
// (record.SuperviseRequest.News, which its payload writes as `"news": true`). Such a start
// reaches only a claim already given its phase's entry, and the claim an idle release suspended
// is one: the start resumes it as any start does, so a release under the start loses nothing, and
// the role comes back with its news. And such a start can wait a long time. A merger's waits for
// its tree's design gate for the whole time the gate stays closed (the daemon's
// mergerWaitsForTheDesignGate), and any one waits behind a task its claim has not finished
// (supervise's queue). A pod held for it would stand idle for all that time. The teardown passes
// over one start of this predicate: a wake's own start of a role the release recorded, with a
// wake since the mark (wakeStartRow, IssueRelease). That start waits for the teardown and starts
// the role in the pod that comes back.
const idleWaitingRow = `kind = any($2) and not (kind = 'supervise' and coalesce(payload->>'news', '') = 'true')`

// IdleReleaseRow is one outbox row a release writes, encoded by its caller (record.NewOutboxRow),
// in the order the issue's lifecycle lane runs them: the save of the workspace's unpushed work,
// then one role stop per live claim, then the teardown. The store writes them rather than
// record's Enqueue because the boundary row's id must be read back under the same transaction.
//
// Boundary marks the one row whose id is the release's boundary: the stop of the first role
// process of the pod, which is what every later decision about this release reads
// (IssueRelease). The save before it is not the boundary — it stops nothing, so a delivery
// queued while it runs still cancels the whole release.
type IdleReleaseRow struct {
	Kind     string
	Payload  []byte
	Boundary bool
}

// IdleReleaseMark is what a decision to release writes: the rows above and the role whose shim
// the bundle is asked of. Rows empty means the pod is not released, and nothing is written.
//
// SuspendsNoRole is the release of a pod no role can use (workflow.UnownedPodRelease), whose
// every claim is already failed, retired or suspended: it suspends no role and saves no bundle,
// so it names no role and queues the teardown alone. The mark records which of the two rules
// wrote it (issue_releases.kind), because what a release did is what its teardown and its
// cancel must read: a release that suspended nothing has nothing to resume and no roles to
// record. A mark that suspends a role names it, and one that suspends none leaves Role empty.
type IdleReleaseMark struct {
	Generation     uint64
	Role           claim.Role
	SuspendsNoRole bool
	Rows           []IdleReleaseRow
}

// releaseKindIdle and releaseKindUnowned are the two release rules as issue_releases.kind spells
// them: the release of a pod whose roles are all quiet, and the release of a pod no role can use
// any more. Every reader of a mark reads the kind rather than guessing the rule from the role,
// which is empty for the second and for nothing else.
const (
	releaseKindIdle    = "idle"
	releaseKindUnowned = "unowned"
)

// IdlePods is every issue of project whose pod has a live claim — one neither failed, retired nor
// already suspended — in key order: the issues the idle tick decides. An issue whose claims are
// all stopped has no role to ask for a bundle and no claim to suspend, so there is nothing for a
// release to do with it.
func (s *Store) IdlePods(ctx context.Context, project string) ([]string, error) {
	what := "list the pods of " + project
	var issues []string
	err := TransactWith(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct issue from claims
			where project = $1 and state not in ('failed', 'retired', 'suspended') and issue <> '' order by issue`, project)
		if err != nil {
			return err
		}
		issues, err = issueKeys(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return issues, nil
}

// issueKeys reads a one-column answer of issue keys into a slice: the candidate lists of the two
// release rules are two statements and one loop (IdlePods, UnownedPods), and each caller names
// whatever goes wrong once. An answer of no rows is a nil slice, which is what a caller reads as
// no candidate, and the rows are closed whichever way the read ends.
func issueKeys(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	var issues []string
	for rows.Next() {
		var issue string
		if err := rows.Scan(&issue); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

// UnownedPods is every issue of project whose pod no role can use, in key order: one that holds
// at least one claim, holds no claim whose state is outside ('failed', 'retired', 'suspended')
// — no live claim, that is — and whose own row and whose tree root's row both still stand in
// the workflow. It is the candidate list of the second release rule
// (workflow.UnownedPodRelease), beside IdlePods, which answers the other half: the pods a live
// claim still holds. Such a pod stands with 3 CPU and 19 GiB reserved and no role that can take
// a turn in it, which is what the rule releases.
//
// The project is spelled two ways, and the two are not the same string, so this read takes
// both. project is the issue record's own project (issues.project, the Dispatch project key
// the records carry, "LEGION"), and claimProject is the claim token's spelling the claims
// table holds (claims.project, "legion"), which is what IdlePods and ReleaseIdleIssue read.
// The caller has both already (daemon.releaseUnownedPods: w.dispatchProject and w.projectID).
// The claims this read asks about are this project's, as they are in every other claim read of
// this file and of the cancel's (IdlePods, readIdleView, release_cancel.go's stillReleasedRoles),
// and the predicate costs nothing: the subqueries scan the same rows either way.
//
// outOfWorkflow is the workflow's list of statuses that take an issue out of it
// (record.OutOfWorkflowStatuses), which the store does not restate: the gate every other path
// reads is a Go predicate (pods.InWorkflowNotLingering) and a select cannot call one, so the
// rule arrives as a list and the filter is this statement's three conditions per row — the
// status outside that list, the phase short of done, and no linger.
//
// Those three conditions are what the filter is for, since the claim states alone would answer
// every issue of the deployment whose roles ever stopped. Every candidate costs one transaction
// of ReleaseIdleIssue, which opens by taking the daemon's global fact serializer
// (treelifecycle.Serialize, pg_advisory_xact_lock(hashtext('legion.apply_fact'))) — the same
// lock the intake takes for every fact, so a tick of candidates is a tick of facts not applied.
// The reading behind that: on the INFRA deployment at 2026-11-10 19:39Z, 1,136 issues held
// claims while 5 stood in the workflow under a live tree.
//
// It adds no index of its own, and the answer is read once a minute. The partial index
// issues_live (migrations/0062_live_issue_reads.up.sql) serves the issue rows: the query's
// i.phase <> 'done' implies that index's predicate. Nothing serves the two subqueries over
// claims, which carries the token primary key and claims_boot_token_hash and no index on issue
// or project at all (migrations/0002_claims.up.sql), so each of them reads the whole of claims,
// once a minute — the same relation, at the same rate, that IdlePods already scans whole for
// its own answer.
func (s *Store) UnownedPods(ctx context.Context, project, claimProject string, outOfWorkflow []string) ([]string, error) {
	what := "list the pods of " + project + " no claim owns"
	var issues []string
	err := TransactWith(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select i.key from issues i join issues root on root.key = i.tree
			where i.project = $1
			and i.status <> all($2) and i.phase <> 'done' and i.linger_until is null
			and root.status <> all($2) and root.phase <> 'done' and root.linger_until is null
			and exists (select 1 from claims c where c.issue = i.key and c.project = $3)
			and not exists (select 1 from claims c where c.issue = i.key and c.project = $3
				and c.state not in ('failed', 'retired', 'suspended'))
			order by i.key`, project, outOfWorkflow, claimProject)
		if err != nil {
			return err
		}
		issues, err = issueKeys(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return issues, nil
}

// ReleaseIdleIssue is the release's one transaction: it reads issue's idle view and hands it to
// decide, and when decide answers rows it writes them and the release's mark together, so the
// view the decision rests on and the mark that records it are one atomic act.
//
// The mark records the ids the rows were given: first_row and last_row are the release's own
// first and last, which is how a row of this release is told from a row of one that is over
// (IssueReleaseStop, IssueRelease), and boundary_row is the row the mark names its boundary —
// the stop of the first role process of the pod — which is where a delivery that reached the
// pod after the mark, while every role still ran, cancels the release. Once a stop has recorded
// its role's claim as `released`, a wake of that role no longer cancels the release: the release
// runs to its end, and the role comes back in the new pod (releaseOvertaken).
//
// It answers whether a release was marked. decide answering no rows writes nothing, which is the
// ordinary case: nearly every tick finds a pod that is working, or one whose idle period has not
// passed.
//
// A project that is paused is marked no release, whatever decide answers: the pause starts no
// release (decision 12 of dispatch://INFRA-3759). decide is still handed the view, which carries
// the pause (pods.IdleView.Paused), so its caller can record what kept the pod. The view is read
// under treelifecycle.Serialize, which the pause's transaction takes too, so a pause and a mark
// commit one after the other.
//
// A mark that names no role and does not say it suspends none is refused before anything is
// written, as a fault of the rule that decided it rather than of the database: the save of the
// workspace is asked of the role the mark names, so a release without one and without the
// second rule's kind is a release nobody can carry out.
func (s *Store) ReleaseIdleIssue(ctx context.Context, project, issue string, decide func(pods.IdleView) (IdleReleaseMark, error)) (bool, error) {
	if project == "" || issue == "" {
		return false, errors.New("an idle release needs its project and its issue")
	}
	var marked bool
	err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := treelifecycle.Serialize(ctx, tx); err != nil {
			return err
		}
		view, err := readIdleView(ctx, tx, project, issue)
		if err != nil {
			return err
		}
		mark, err := decide(view)
		if err != nil || len(mark.Rows) == 0 {
			return err
		}
		if mark.Role == "" && !mark.SuspendsNoRole {
			return fmt.Errorf("the release of %s names no role and is not a release that suspends none", issue)
		}
		if view.Releasing || view.Paused {
			return nil
		}
		var first, boundary, last int64
		for _, row := range mark.Rows {
			id, err := insertOutboxRow(ctx, tx, project, issue, row)
			if err != nil {
				return err
			}
			if first == 0 {
				first = id
			}
			last = id
			if row.Boundary {
				boundary = id
			}
		}
		if boundary == 0 {
			return fmt.Errorf("the release of %s named no boundary row", issue)
		}
		var started int64
		for _, c := range view.Claims {
			started = max(started, c.LastStartRow)
		}
		// The newest wake this pod has had: a wake recorded above it is one this release never
		// saw. It cancels the release, unless it is a wake of a role this release has since
		// recorded as `released` (releaseOvertaken).
		var wake int64
		if err := tx.QueryRow(ctx, `select coalesce(max(id), 0) from pod_resumes where project = $1 and issue = $2`,
			project, issue).Scan(&wake); err != nil {
			return fmt.Errorf("read the newest wake of %s: %w", issue, err)
		}
		// The kind is the rule that decided this release, and the role is the one whose shim the
		// save is asked of: a release that suspends no role has none, and leaves the column
		// empty, which is the one spelling the table's check takes beside that kind.
		kind, role := releaseKindIdle, string(mark.Role)
		if mark.SuspendsNoRole {
			kind, role = releaseKindUnowned, ""
		}
		if _, err := tx.Exec(ctx, `insert into issue_releases
			(project, issue, generation, first_row, boundary_row, last_row, started_row, wake_row, role, kind, marked_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
			project, issue, int64(mark.Generation), first, boundary, last, started, wake, role, kind); err != nil {
			return fmt.Errorf("mark the release of %s: %w", issue, err)
		}
		// The pod this release stops is gone, so a resume of it still open — a wake whose pod
		// came back and whose role took no turn the daemon saw — belongs to that pod and not to
		// the next one. Closing them here, in the transaction that marks the release, is what
		// lets the next wake of each role take its own (store's TakePodWake, one open resume per
		// role).
		//
		// A release that suspends no role is no wake's to end, so it closes none. It stops a pod
		// whose every claim is failed, retired or suspended, which is a pod no wake of this issue
		// is waiting on: a claim an earlier release left `released` is waiting for the pod that
		// release took, not for this one, and its resume is the record of that wait. Closing it
		// here would stamp that release woken and never turned up, and charge it from its own
		// moment to every later read — the distortion the records exist to remove, moved onto the
		// row beside it. internal/daemon's
		// TestAnIssueWithNoLiveClaimHasItsSandboxReleased/"the pod holding a claim an earlier
		// release left released" is what holds this, and the teardown's own reading in that
		// package's outbox arms no backstop here for the same reason. A resume this release
		// leaves open is closed by the next wake of its role, which reads that a release was
		// recorded after that wake was taken (store's ClosePodGoneResume, the workflow's
		// wakeReleased).
		if !mark.SuspendsNoRole {
			if err := CloseOpenResumes(ctx, tx, project, issue); err != nil {
				return err
			}
		}
		marked = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("release the idle pod of %s: %w", issue, err)
	}
	return marked, nil
}

// insertOutboxRow queues one outbox row of project, encoded by the caller, and answers the id the
// outbox gave it: the ids of a release's own rows are the range its mark is read against
// (IssueReleaseStop), and a resume a cancel queues takes its id from the same sequence, above every
// row of the release it ends. A pause's stop rows and its notices are queued through it too
// (queuePauseStopRow, queuePauseNotice).
//
// project is the caller's project, its Dispatch key or its claim token's spelling, and a row keyed
// by an issue of another project is refused (claim.KeyOfProject): only that project's daemon runs
// it, against that daemon's own claims. The refusal is an error (claim.ErrKeyOfAnotherProject),
// which rolls the caller's transaction back, since every caller writes the row together with what
// names it — a release's mark, a cancel's end of one, a pause and its stops — and neither stands
// without the other.
func insertOutboxRow(ctx context.Context, tx pgx.Tx, project, issue string, row IdleReleaseRow) (int64, error) {
	if !claim.KeyOfProject(project, issue) {
		return 0, fmt.Errorf("queue the %s row of %s for project %s: %w", row.Kind, issue, project, claim.ErrKeyOfAnotherProject)
	}
	var id int64
	if err := tx.QueryRow(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error, lease_token)
		values ($1, $2, $3, 0, now(), '', '') returning id`, row.Kind, issue, string(row.Payload)).Scan(&id); err != nil {
		return 0, fmt.Errorf("queue the %s row of %s: %w", row.Kind, issue, err)
	}
	return id, nil
}

// ClearIssueRelease forgets the mark of issue's release once the release finished. A cancel
// forgets its mark in the transaction that queues its resumes (CancelIssueRelease). A mark
// already gone is success, so a retried finish finishes.
func (s *Store) ClearIssueRelease(ctx context.Context, project, issue string) error {
	if err := Transact(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return deleteIssueRelease(ctx, tx, project, issue)
	}); err != nil {
		return fmt.Errorf("clear the release mark of %s: %w", issue, err)
	}
	return nil
}

// deleteIssueRelease forgets the mark of issue's release in the caller's transaction. One mark,
// one delete, whichever runs it.
func deleteIssueRelease(ctx context.Context, tx pgx.Tx, project, issue string) error {
	_, err := tx.Exec(ctx, `delete from issue_releases where project = $1 and issue = $2`, project, issue)
	return err
}

// readIdleView is issue's pod as the idle rules read it (pods.IdleView), through tx; project is
// the project token, as the claims hold it.
//
// The issue's record counts only where its project is project's (issues.project holds the
// Dispatch key, which the token lowercases), and any other record is read as none: the view
// answers Found false, as for an issue no record holds, and both rules then keep the pod
// (workflow.IdleHoldNoPod, IdleHoldUnownedNoClaim). The issues table holds the issues of every
// project on the database, and the operator's spawn makes a claim of another project's issue
// under this project's token, so its pod can find the record that project's daemon keeps of the
// issue. A release decided on that record would queue rows keyed by the other project's issue,
// which this project's outbox never runs (record's DueRows), under a mark that would never
// clear. The root is read through the issue's own tree and needs no check of its own: a record
// takes its root's project when the workflow writes it, so the root of a record of this project
// is this project's too.
func readIdleView(ctx context.Context, tx pgx.Tx, project, issue string) (pods.IdleView, error) {
	view := pods.IdleView{Issue: issue}
	var issueGeneration, rootGeneration int64
	var issuePhase, rootPhase string
	err := tx.QueryRow(ctx, `select i.tree, i.phase, i.status, i.generation, i.linger_until,
		root.phase, root.status, root.generation, root.linger_until,
		root.phase = 'awaiting_person', root.awaiting_answered_at is not null
		from issues i join issues root on root.key = i.tree where i.key = $1 and lower(i.project) = $2`, issue, project).Scan(
		&view.Tree, &issuePhase, &view.Status, &issueGeneration, &view.LingerUntil,
		&rootPhase, &view.Root.Status, &rootGeneration, &view.Root.LingerUntil,
		&view.Root.AwaitingPerson, &view.Root.Answered)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return view, nil
	case err != nil:
		return pods.IdleView{}, fmt.Errorf("read %s for its idle view: %w", issue, err)
	}
	view.Found = true
	view.Phase, view.Root.Phase = phase.Phase(issuePhase), phase.Phase(rootPhase)
	view.Generation, view.Root.Generation = uint64(issueGeneration), uint64(rootGeneration)

	claims, err := tx.Query(ctx, `select c.role, c.state, c.plugin_contract, c.serving_generation,
		c.last_start_row, c.activity_at, c.activity_background, c.activity_kinds, c.activity_next_reminder_at,
		c.update_round, c.updated_at, c.locator is not null, c.suspended_by,
		greatest(d.delivered_at, d.confirmed_at)
		from claims c left join pending_task_deliveries d on d.claim_token = c.token
		where c.project = $1 and c.issue = $2 order by c.token`, project, issue)
	if err != nil {
		return pods.IdleView{}, fmt.Errorf("read the claims of %s for its idle view: %w", issue, err)
	}
	defer claims.Close()
	for claims.Next() {
		var c pods.IdleClaim
		var role, state, kinds, suspendedBy string
		var servingGeneration int64
		var activityAt, reminder, lastTurn *time.Time
		if err := claims.Scan(&role, &state, &c.PluginContract, &servingGeneration, &c.LastStartRow,
			&activityAt, &c.ActivityBackground, &kinds, &reminder, &c.UpdateRound, &c.UpdatedAt,
			&c.HasProcess, &suspendedBy, &lastTurn); err != nil {
			return pods.IdleView{}, fmt.Errorf("read the claims of %s for its idle view: %w", issue, err)
		}
		c.Role, c.State = claim.Role(role), supervise.ClaimState(state)
		c.SuspendedBy = supervise.SuspendSource(suspendedBy)
		c.ServingGeneration = uint64(servingGeneration)
		c.ActivityAt, c.ActivityNextReminderAt, c.LastTurnAt = orZero(activityAt), orZero(reminder), orZero(lastTurn)
		if kinds != "" {
			c.ActivityKinds = strings.Split(kinds, ",")
		}
		view.Claims = append(view.Claims, c)
	}
	if err := claims.Err(); err != nil {
		return pods.IdleView{}, fmt.Errorf("read the claims of %s for its idle view: %w", issue, err)
	}

	if err := tx.QueryRow(ctx, `select exists (
		select 1 from outbox where issue = $1 and `+idleWaitingRow+`)`, issue, idleWaitingKinds).Scan(&view.Waiting); err != nil {
		return pods.IdleView{}, fmt.Errorf("read the queued rows of %s for its idle view: %w", issue, err)
	}
	if err := tx.QueryRow(ctx, `select exists (select 1 from issue_releases where project = $1 and issue = $2)`,
		project, issue).Scan(&view.Releasing); err != nil {
		return pods.IdleView{}, fmt.Errorf("read whether %s is being released: %w", issue, err)
	}
	unrestored, err := workspaceBundleUnrestored(ctx, tx, issue)
	if err != nil {
		return pods.IdleView{}, err
	}
	view.BundleUnrestored = unrestored
	// The pause's two facts, read by the view's project token as readOpenPauseByToken reads a
	// pause (which says why): project_pauses and the pod holds carry its Dispatch key, which
	// project, the token (claim.ProjectToken), lowercases for every key project_pauses admits.
	// The view is read under treelifecycle.Serialize, which the pause's own transaction takes
	// too, so a release is marked either before a pause or not at all (ReleaseIdleIssue,
	// decision 12 of dispatch://INFRA-3759).
	if err := tx.QueryRow(ctx, `select
		exists (select 1 from project_pauses where lower(project) = $1 and ended_at is null),
		exists (select 1 from project_pause_pod_holds where lower(project) = $1 and issue = $2 and ended_at is null)`,
		project, issue).Scan(&view.Paused, &view.PauseHeld); err != nil {
		return pods.IdleView{}, fmt.Errorf("read whether a pause holds %s: %w", issue, err)
	}
	holding, err := openPodHold(ctx, tx, project, issue)
	if err != nil {
		return pods.IdleView{}, err
	}
	view.Holding = holding
	return view, nil
}
