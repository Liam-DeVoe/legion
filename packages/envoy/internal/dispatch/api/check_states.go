package api

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// check_states.go publishes the state of each `before_merge` check at each open pull request head
// of an issue, one check.state_changed event for each change of a state.
//
// A write that can change a state does not publish anything itself: a row trigger on each table a
// state is computed from marks the issue owed, in the writer's own transaction
// (check_states_owe, migration 20261121000327, and the triggers after it), and notifies
// CheckStatesOwedChannel, which Postgres delivers at the writer's commit and never for a rollback.
// So no writer runs code of this file or takes a lock it did not take before, a write of the
// release before this one is marked too, and a writer that a later change adds to a watched table
// is marked without code of its own.
//
// The settler (checkStatesSettler) is woken on that channel in every process. A settle of one
// issue runs under the issue's own lease, so one settle at a time computes its states: it reads
// the marks that are due, computes the states with the computation GET
// /api/v1/issues/{key}/results answers (answerIssueChecks), compares each with the state it
// published last (check_states), and in one transaction appends one event for each difference,
// stores what it published and deletes the marks it read. Changes that land between two computes
// count as one change, and the event names the state Dispatch computed then.
//
// A test handoff waits for the states its results caused before it is recorded: that fence,
// which the handoff route runs and the settler does not, is check_states_fence.go's.

// CheckStatesOwedChannel is the Postgres channel check_states_owe notifies each time it marks an
// issue's check states owed: the wake of the settler's loop (runCheckStates).
const CheckStatesOwedChannel = "dispatch_check_states_owed"

// checkStatesLeasePrefix is the start of the name of each issue's lease, which the issue key
// ends: a settle runs under its issue's lease alone, so two processes settle two issues at once
// and never one issue twice at once.
const checkStatesLeasePrefix = "check-states:"

// checkStatesSchedule is the settler's timing, a value so a test shortens it. minGap is the least
// time between the starts of two passes, and the wait before a pass that skipped an issue, read a
// full batch or left a due mark tries again; sweep the longest time between two passes, so a mark
// whose notification no process received is settled within it; retry the wait after a pass that
// failed, and the first wait of a mark kept after a failed read of GitHub, which doubles with each
// failure up to retryMax; fenceWait and fencePoll the fence of a test handoff
// (awaitCheckStatesSettled); batch the most issues one pass reads; rounds the most rounds one
// settle runs.
type checkStatesSchedule struct {
	minGap, sweep, retry, retryMax, fenceWait, fencePoll time.Duration
	batch, rounds                                        int
}

// checkStatesProduction is the schedule a server runs. The fence waits 30 s, half the minute the
// dispatch command's client waits for an answer, so the client reads the fence's refusal.
var checkStatesProduction = checkStatesSchedule{
	minGap: time.Second, sweep: time.Minute, retry: 30 * time.Second, retryMax: 10 * time.Minute,
	fenceWait: 30 * time.Second, fencePoll: 100 * time.Millisecond, batch: 20, rounds: 5,
}

// checkStatesHooks hold a settle at fixed points in a test; nil in production.
type checkStatesHooks struct {
	afterMarks   func(issue string) // the due marks are read, the compute has not started
	afterCompute func(issue string) // the compute is done, the transaction has not begun
}

// checkStatesSettler settles the issues whose check states are owed.
type checkStatesSettler struct {
	server    *server
	leases    duty.Leases        // the "check states" reservation of the lease pool
	exclusive duty.ExclusiveFunc // duty.Exclusive
	schedule  checkStatesSchedule
	hooks     checkStatesHooks
}

// runCheckStates is the settler's loop (Background.RunCheckStates), which every process runs on
// its reservation of the lease pool and its wake on CheckStatesOwedChannel.
func (s *server) runCheckStates(ctx context.Context, leases duty.Leases, wake <-chan struct{}) {
	settler := &checkStatesSettler{server: s, leases: leases, exclusive: duty.Exclusive, schedule: s.checkStatesSchedule}
	settler.run(ctx, wake)
}

// run is a woken loop (duty.RunWoken) with no lease of its own, since each issue has its lease:
// one pass at start, which settles what a process that stopped left owed, one on each wake, and one
// at each due time a pass answers, at most sweep after the pass before it, which settles a mark
// whose notification no process received. The background passes run on this goroutine, under the
// issues' leases, so the loop needs one connection of the lease pool and no more.
func (c *checkStatesSettler) run(ctx context.Context, wake <-chan struct{}) {
	ctx = store.WithTransactionTracking(ctx)
	duty.RunWoken(ctx, duty.WokenLoop{Wake: wake, MinGap: c.schedule.minGap, Pass: c.pass})
}

// pass settles up to batch issues that hold a due mark, the issue of the oldest mark first, each
// under its own lease, and answers when the loop next runs with no wake: minGap from now where it
// skipped an issue another process held, read a full batch, or left a due mark after an issue's
// last round; retry from now where a settle failed; else the earliest due time of a mark not due
// yet, and sweep from now at the latest.
func (c *checkStatesSettler) pass(ctx context.Context) (next time.Time) {
	issues, err := c.owedIssues(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return time.Time{}
		}
		slog.Warn("dispatch: read the issues whose check states are owed", "error", err)
		return time.Now().Add(c.schedule.retry)
	}
	soon := len(issues) >= c.schedule.batch
	failed := false
	for _, issue := range issues {
		ran, settleFailed, owed := c.settle(ctx, issue)
		if ctx.Err() != nil {
			return time.Time{}
		}
		failed = failed || settleFailed
		soon = soon || !ran || owed
	}
	switch {
	case failed:
		return time.Now().Add(c.schedule.retry)
	case soon:
		return time.Now().Add(c.schedule.minGap)
	}
	wait, held, err := c.nextDue(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return time.Time{}
		}
		slog.Warn("dispatch: read the next due check states mark", "error", err)
		return time.Now().Add(c.schedule.retry)
	}
	if !held || wait > c.schedule.sweep {
		wait = c.schedule.sweep
	}
	return time.Now().Add(wait)
}

// owedIssues is up to batch issues that hold a due mark, the issue of the oldest mark first.
func (c *checkStatesSettler) owedIssues(ctx context.Context) ([]string, error) {
	rows, err := c.server.deps.Store.Pool.Query(ctx, `
		select issue_key from check_states_owed where due_at <= now()
		group by issue_key order by min(id) limit $1`, c.schedule.batch)
	if err != nil {
		return nil, fmt.Errorf("read the issues whose check states are owed: %w", err)
	}
	issues, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the issues whose check states are owed: %w", err)
	}
	return issues, nil
}

// nextDue is how long until the earliest mark that is not due yet falls due, measured on the
// database's clock as every due time is written, and false where no such mark is held.
func (c *checkStatesSettler) nextDue(ctx context.Context) (time.Duration, bool, error) {
	var seconds *float64
	if err := c.server.deps.Store.Pool.QueryRow(ctx, `
		select extract(epoch from min(due_at) - now())::float8 from check_states_owed where due_at > now()`).Scan(&seconds); err != nil {
		return 0, false, fmt.Errorf("read the next due check states mark: %w", err)
	}
	if seconds == nil {
		return 0, false, nil
	}
	return time.Duration(*seconds * float64(time.Second)), true, nil
}

// settle runs the rounds of one issue under its lease, at most rounds of them. ran is false where
// the lease was not taken: another process holds it and settles the issue, and the wake of the
// same commit earns this process another pass. failed is true where a round failed before its
// commit, which keeps every mark it read; owed is true where a due mark is left after the last
// round.
func (c *checkStatesSettler) settle(ctx context.Context, issue string) (ran, failed, owed bool) {
	ran = c.exclusive(ctx, c.leases, checkStatesLeasePrefix+issue, time.Time{}, 0, func(ctx context.Context) {
		for range c.schedule.rounds {
			more, err := c.settleRound(ctx, issue)
			if err != nil {
				failed = true
				if ctx.Err() == nil {
					slog.Warn("dispatch: settle the check states of an issue", "issue", issue, "error", err)
				}
				return
			}
			if !more {
				return
			}
		}
		owed = true
	})
	return ran, failed, owed
}

// settleRound is one round of a settle: it reads the issue's due marks, computes the issue's
// states, publishes each change in one transaction that deletes the marks it read, and answers
// whether a due mark is left. A mark is drawn its id at its insert and not at its commit, so a
// mark of a lower id can commit after the round read: the round deletes the marks it read by id,
// never a range of ids, and a mark it did not read stays for the next round.
func (c *checkStatesSettler) settleRound(ctx context.Context, issue string) (more bool, err error) {
	pool := c.server.deps.Store.Pool
	marks, failures, err := dueMarksOf(ctx, pool, issue)
	if err != nil || len(marks) == 0 {
		return false, err
	}
	if c.hooks.afterMarks != nil {
		c.hooks.afterMarks(issue)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `select exists (select 1 from issues where key = $1)`, issue).Scan(&exists); err != nil {
		return false, fmt.Errorf("read whether %s exists: %w", issue, err)
	}
	var published []model.Event
	if !exists {
		// An issue nobody has any more has no state to publish, and its marks settle nothing.
		if _, err := pool.Exec(ctx, `delete from check_states_owed where id = any($1)`, marks); err != nil {
			return false, fmt.Errorf("delete the marks of %s, which no longer exists: %w", issue, err)
		}
	} else {
		// Each mark read committed before this compute, in the transaction of its change, so the
		// compute sees every change the marks record.
		heads, err := delivery.HeadsAttributedTo(ctx, pool, issue)
		if err != nil {
			return false, err
		}
		answer, held, err := c.server.answerIssueChecks(ctx, issue, noCommitNamed, nil)
		if err != nil {
			return false, fmt.Errorf("compute the checks of %s: %w", issue, err)
		}
		computed, readFailed, err := checkStatesAtOpenHeads(issue, heads, answer)
		if err != nil {
			return false, err
		}
		if c.hooks.afterCompute != nil {
			c.hooks.afterCompute(issue)
		}
		var artifact *string
		if held {
			artifact = &answer.VerificationArtifactID
		}
		if published, err = c.publish(ctx, issue, marks, failures, artifact, computed, readFailed); err != nil {
			return false, err
		}
		c.server.publish(ctx, published...)
	}
	slog.Info("dispatch: check states settled", "issue", issue, "marks", len(marks), "events", len(published))
	if err := pool.QueryRow(ctx, `select exists (select 1 from check_states_owed where issue_key = $1 and due_at <= now())`, issue).Scan(&more); err != nil {
		return false, fmt.Errorf("read the marks of %s left: %w", issue, err)
	}
	return more, nil
}

// dueMarksOf is the ids of issue's due marks and the most failures any of them counts.
func dueMarksOf(ctx context.Context, pool *store.Pool, issue string) ([]int64, int, error) {
	var marks []int64
	var failures int
	if err := pool.QueryRow(ctx, `
		select coalesce(array_agg(id), '{}'), coalesce(max(failures), 0)
		from check_states_owed where issue_key = $1 and due_at <= now()`, issue).Scan(&marks, &failures); err != nil {
		return nil, 0, fmt.Errorf("read the due marks of %s: %w", issue, err)
	}
	return marks, failures, nil
}

// headStates is what the settler computed at one open head of an issue: the head's repository and
// commit, the open pull requests of the issue at it, in number order, the block ids of the issue's
// `before_merge` checks in document order, which is the head's set of checks, and the state of
// each of them there, in the same order.
type headStates struct {
	repo, head   string
	pullRequests []int
	checks       []string
	states       []checkStateAt
}

// checkStateAt is the state of one check at one head: its subject there, as passStandsOf computed
// it (model.PassStandsSubject).
type checkStateAt struct {
	check, state, reason, ciCase string
	version                      int
	roles, results               []string
}

// checkStatesAtOpenHeads is the states of issue at each of its open heads: heads are the pull
// requests delivery.HeadsAttributedTo answers, and answer is answerIssueChecks's. Two open pull
// requests at one commit of one repository are one head with one state. The state of a check at a
// head is its subject whose basis is that head's pull request and commit. readFailed says whether
// a state reads read_failed: a check CI proves whose check runs GitHub could not be read there.
//
// The heads are read apart from the computation's own read of them, so a head that moved between
// the two reads is a head the computation holds no subject of. That is an error, which keeps every
// mark: the move marked the issue owed after these marks were read, and the round after the next
// wake computes both from one read.
func checkStatesAtOpenHeads(issue string, heads []delivery.AttributedHead, answer issueCheckResults) ([]headStates, bool, error) {
	type headKey struct{ repo, head string }
	grouped := map[headKey]*headStates{}
	linked := map[headKey][]string{}
	var order []headKey
	for _, head := range heads {
		if !head.Open {
			continue
		}
		key := headKey{head.Repo, head.HeadSHA}
		group, held := grouped[key]
		if !held {
			group = &headStates{repo: head.Repo, head: head.HeadSHA}
			grouped[key] = group
			order = append(order, key)
		}
		group.pullRequests = append(group.pullRequests, head.Number)
		if head.LinkedRepo != "" {
			linked[key] = append(linked[key], head.LinkedRepo)
		}
	}
	slices.SortFunc(order, func(a, b headKey) int {
		return strings.Compare(a.repo+"\x00"+a.head, b.repo+"\x00"+b.head)
	})
	var checks []model.CheckBlock
	for _, check := range answer.Checks {
		if check.When == docs.CheckWhenBeforeMerge {
			checks = append(checks, check)
		}
	}
	readFailed := false
	computed := make([]headStates, 0, len(order))
	for _, key := range order {
		group := grouped[key]
		slices.Sort(group.pullRequests)
		group.checks = make([]string, 0, len(checks))
		for _, check := range checks {
			subject, found := subjectAtHead(check, *group, linked[key])
			if !found {
				return nil, false, fmt.Errorf("the computation of %s holds no state of %s at %s of %s#%v: the head moved after its read",
					issue, check.BlockID, group.head, group.repo, group.pullRequests)
			}
			state := checkStateAt{
				check: check.BlockID, version: check.Version, state: subject.Answer, reason: subject.Reason,
				roles: nonNilStrings(subject.Roles), results: nonNilStrings(subject.Results),
			}
			if subject.CI != nil {
				state.ciCase = subject.CI.Case
			}
			readFailed = readFailed || subject.Reason == model.PassSubjectReasonReadFailed
			group.checks = append(group.checks, check.BlockID)
			group.states = append(group.states, state)
		}
		computed = append(computed, *group)
	}
	return computed, readFailed, nil
}

// subjectAtHead is check's subject at head: a pull request basis at head's commit whose pull request
// is one of head's, of head's repository. A subject says its repository in one of two places. The
// read of a check CI proves at a head carries the repository of that head's row, where its check
// runs were read (CheckCI.Repo), and that read is the one part of a state that differs between two
// repositories at one commit. A basis names its repository only where it is another than the
// issue's (headBasis): the canonical name of the link that made the pull request the issue's, or
// the row's own repository, and none at all where the issue resolves to no repository. So two
// repositories with an open #70 at one commit can hold two subjects whose bases are alike, and the
// read's repository, then the basis's, is what tells them apart. A subject that names no
// repository either way carries no read of a check CI proves: its state is the state of its commit,
// from the results that count there whatever repository recorded them, the same at every head at
// that commit, so it serves head where no subject names head's repository.
func subjectAtHead(check model.CheckBlock, head headStates, linked []string) (model.PassStandsSubject, bool) {
	var unnamed *model.PassStandsSubject
	for index := range check.PassStands.Subjects {
		subject := &check.PassStands.Subjects[index]
		basis := subject.Basis
		if basis.Kind != model.CheckBasisPullRequest || basis.Merged || basis.Commit != head.head ||
			basis.PullRequest == nil || !slices.Contains(head.pullRequests, *basis.PullRequest) {
			continue
		}
		repo := basis.Repo
		if subject.CI != nil && subject.CI.Repo != "" {
			repo = subject.CI.Repo
		}
		switch {
		case repo == "":
			if unnamed == nil {
				unnamed = subject
			}
		case strings.EqualFold(repo, head.repo) || slices.ContainsFunc(linked, func(link string) bool { return strings.EqualFold(repo, link) }):
			return *subject, true
		}
	}
	if unnamed != nil {
		return *unnamed, true
	}
	return model.PassStandsSubject{}, false
}

// checkStateKey is one row of check_states: a check at a head of a repository, "" for the head's
// set row.
type checkStateKey struct {
	repo, head, check string
}

// publishedCheckState is one row of check_states as the settler compares it.
type publishedCheckState struct {
	state  *string
	reason string
	roles  []string
	ciCase string
	checks []string
}

// sameState reports whether the published row holds the state computed: a change of the state,
// its reason, its roles or its CI case is a change, and a change of its results or its check's
// version alone is none.
func (p publishedCheckState) sameState(state checkStateAt) bool {
	return p.state != nil && *p.state == state.state && p.reason == state.reason && p.ciCase == state.ciCase &&
		sameStringSet(p.roles, state.roles)
}

// publish appends one check.state_changed event for each state that differs from the one published
// at that head, and the set form for each head whose set of checks moved with no check's event to
// carry it, in one transaction under the issue's owner lock: it raises the issue's serial by one
// for each event, stores what each event published, deletes the rows of heads no longer open and
// of checks no longer in a head's set, with no event, and deletes the marks the round read. Where
// a state reads read_failed it inserts one mark due later, with no notification, so the issue is
// read again once GitHub may answer. Only the settler writes check_states, under the issue's
// lease, so the rows it reads here are the newest published. It answers the events, which the
// caller publishes once the transaction has committed.
func (c *checkStatesSettler) publish(
	ctx context.Context, issue string, marks []int64, failures int, artifact *string, computed []headStates, readFailed bool,
) ([]model.Event, error) {
	s := c.server
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	owner := issueOwner(issue)
	if err := s.deps.Events.LockOwners(ctx, tx, owner.event(model.CheckStateChangedEventType, model.CheckStatesActor, nil)); err != nil {
		return nil, err
	}
	published, err := publishedCheckStates(ctx, tx, issue)
	if err != nil {
		return nil, err
	}
	var serial int64
	if err := tx.QueryRow(ctx, `select coalesce((select serial from check_state_serials where issue_key = $1), 0)`, issue).Scan(&serial); err != nil {
		return nil, fmt.Errorf("read the check states serial of %s: %w", issue, err)
	}
	var events []model.Event
	appendState := func(payload model.CheckStateChangedEventPayload) (int64, error) {
		serial++
		payload.Serial = serial
		event, err := s.appendEvent(ctx, tx, owner.event(model.CheckStateChangedEventType, model.CheckStatesActor, payload))
		if err != nil {
			return 0, err
		}
		events = append(events, event)
		return int64(event.Seq), nil
	}
	kept := map[checkStateKey]bool{}
	for _, head := range computed {
		var lastSeq int64
		for _, state := range head.states {
			key := checkStateKey{head.repo, head.head, state.check}
			kept[key] = true
			if row, held := published[key]; held && row.sameState(state) {
				continue
			}
			check, version, answer := state.check, state.version, state.state
			seq, err := appendState(model.CheckStateChangedEventPayload{
				Issue: issue, CheckArtifactID: artifact, Repo: head.repo, Head: head.head,
				PullRequests: head.pullRequests, Checks: head.checks, Check: &check, CheckVersion: &version,
				State: &answer, Reason: state.reason, Roles: state.roles, Results: state.results, CICase: state.ciCase,
			})
			if err != nil {
				return nil, err
			}
			lastSeq = seq
			if err := upsertCheckState(ctx, tx, issue, key, artifact, &version, &answer, state.reason, state.roles, state.ciCase, head, seq); err != nil {
				return nil, err
			}
		}
		key := checkStateKey{head.repo, head.head, ""}
		kept[key] = true
		if row, held := published[key]; held && sameStringSet(row.checks, head.checks) {
			continue
		}
		// The head's set of checks moved, or the head is new. A check's event at this head carries
		// the new set; with none, the set form tells it, so a reader can tell a head with no check
		// from a head no event has reached yet.
		if lastSeq == 0 {
			if lastSeq, err = appendState(model.CheckStateChangedEventPayload{
				Issue: issue, CheckArtifactID: artifact, Repo: head.repo, Head: head.head,
				PullRequests: head.pullRequests, Checks: head.checks,
			}); err != nil {
				return nil, err
			}
		}
		if err := upsertCheckState(ctx, tx, issue, key, artifact, nil, nil, "", nil, "", head, lastSeq); err != nil {
			return nil, err
		}
	}
	for key := range published {
		if kept[key] {
			continue
		}
		if _, err := tx.Exec(ctx, `delete from check_states where issue_key = $1 and repo = $2 and head_sha = $3 and check_block_id = $4`,
			issue, key.repo, key.head, key.check); err != nil {
			return nil, fmt.Errorf("delete the published state of %s at %s of %s: %w", key.check, key.head, issue, err)
		}
	}
	if len(events) > 0 {
		if _, err := tx.Exec(ctx, `
			insert into check_state_serials (issue_key, serial) values ($1, $2)
			on conflict (issue_key) do update set serial = excluded.serial`, issue, serial); err != nil {
			return nil, fmt.Errorf("store the check states serial of %s: %w", issue, err)
		}
	}
	if _, err := tx.Exec(ctx, `delete from check_states_owed where id = any($1)`, marks); err != nil {
		return nil, fmt.Errorf("delete the settled marks of %s: %w", issue, err)
	}
	if readFailed {
		if _, err := tx.Exec(ctx, `
			insert into check_states_owed (issue_key, due_at, failures) values ($1, now() + make_interval(secs => $2), $3)`,
			issue, c.retryAfter(failures).Seconds(), failures+1); err != nil {
			return nil, fmt.Errorf("keep a mark of %s to read GitHub again: %w", issue, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return events, nil
}

// retryAfter is how long a mark kept after a failed read waits: retry, doubled for each failure
// the marks it replaces counted, at most retryMax.
func (c *checkStatesSettler) retryAfter(failures int) time.Duration {
	wait := c.schedule.retry
	for range failures {
		if wait >= c.schedule.retryMax {
			break
		}
		wait *= 2
	}
	return min(wait, c.schedule.retryMax)
}

// publishedCheckStates is every row of check_states of issue, read under its owner lock.
func publishedCheckStates(ctx context.Context, tx pgx.Tx, issue string) (map[checkStateKey]publishedCheckState, error) {
	rows, err := tx.Query(ctx, `
		select repo, head_sha, check_block_id, state, reason, roles, ci_case, checks
		from check_states where issue_key = $1`, issue)
	if err != nil {
		return nil, fmt.Errorf("read the published check states of %s: %w", issue, err)
	}
	defer rows.Close()
	published := map[checkStateKey]publishedCheckState{}
	for rows.Next() {
		var key checkStateKey
		var row publishedCheckState
		if err := rows.Scan(&key.repo, &key.head, &key.check, &row.state, &row.reason, &row.roles, &row.ciCase, &row.checks); err != nil {
			return nil, fmt.Errorf("read the published check states of %s: %w", issue, err)
		}
		published[key] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the published check states of %s: %w", issue, err)
	}
	return published, nil
}

// upsertCheckState stores what one event published at key: a check's state, or the set row,
// whose version and state are null.
func upsertCheckState(
	ctx context.Context, tx pgx.Tx, issue string, key checkStateKey, artifact *string, version *int, state *string,
	reason string, roles []string, ciCase string, head headStates, seq int64,
) error {
	if _, err := tx.Exec(ctx, `
		insert into check_states (issue_key, repo, head_sha, check_block_id, check_artifact_id, check_version, state,
			reason, roles, ci_case, checks, pull_requests, event_seq)
		values ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13)
		on conflict (issue_key, repo, head_sha, check_block_id) do update set
			check_artifact_id = excluded.check_artifact_id, check_version = excluded.check_version,
			state = excluded.state, reason = excluded.reason, roles = excluded.roles, ci_case = excluded.ci_case,
			checks = excluded.checks, pull_requests = excluded.pull_requests, event_seq = excluded.event_seq`,
		issue, key.repo, key.head, key.check, artifact, version, state, reason, nonNilStrings(roles), ciCase,
		nonNilStrings(head.checks), head.pullRequests, seq); err != nil {
		return fmt.Errorf("store the published state of %s at %s of %s: %w", key.check, key.head, issue, err)
	}
	return nil
}

// sameStringSet reports whether a and b hold the same strings, in any order.
func sameStringSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}
