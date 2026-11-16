package docs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// DecisionStep is the step of this release's boot and of a tick of its own that brings what the
// release before wrote onto the store (dispatch://INFRA-1150, "The step"). Its phases run in
// order:
//
//  1. The backfill (graphcensus.BackfillAsks): the object nodes of every row written before its
//     kind's writer wrote one, then every asks and ask_followers row that names no node, stated on
//     the ask's node and marked, in committed batches.
//  2. The catch-up of the events a process of another build appended on a row this build marked
//     (catchUp): its ask.answered, ask.resolved, ask.opened, ask.follower_added and
//     ask.follower_removed, each stated on the ask's node under its own event, from its payload,
//     and never from the row.
//
// It runs at boot, before the server serves, where only phase 1 runs and a failure stops the boot
// (Boot), as FillDocumentSearch does: the readers of this release that take an ask's answer from
// its node - a reading's check of its ask (askAnswers) and this step's own count - read an ask
// phase 1 has not stated yet as open, and the release before keeps serving through a roll until
// this process serves. It then runs on a tick of its own (Run) every BlockIndexReconcileInterval
// for as long as the process runs: a process of another build can go live after a tick found
// nothing to do - a rollback, or a process of the release before that starts late - so the tick
// never stops for want of work, and a tick with none costs the probes of what each phase owes
// (probe) and nothing more. Phase 1 has work while an unmarked row stands (graphcensus.AsksOwed).
// Phase 2 has work on each tick while processes.Live lists a process of another build, once more
// on the first tick that finds the fleet one build, which is also a process's first tick - the
// window it reads opens before this process started, so a restart of a process on a fleet that
// stood whole never misses a write - and again after any pass on which an ask failed. Each tick
// counts what the restore and the conversion owe (DecisionStepOwed), which /healthz answers
// (Owed).
//
// The runner takes a session advisory lock of its own (decisionStepLock), so two processes that
// boot together do not run it both at once: a booting process waits for it, so every process has
// phase 1 behind it before it serves, and a tick that finds it held skips, since the holder is
// doing the work. A failure on one batch of phase 1 rolls back that batch, and one on one ask of
// phase 2 that ask; either is logged with its reason and tried again at the next tick.
type DecisionStep struct {
	database *store.Store
	commit   string
	logger   *slog.Logger
	// interval is the wait between two ticks of Run: BlockIndexReconcileInterval.
	interval time.Duration
	// catchUpOwed is whether phase 2 owes a pass on a tick that finds the fleet one build: true
	// until the first such tick runs it, and again after any tick that finds another build live or
	// on which an ask failed.
	catchUpOwed bool
	// horizon marks the event log for the catch-up (writerFloor), and marks are the marks its
	// passes took, newest first and at most decisionStepMarksKept of them.
	horizon *events.Horizon
	marks   []events.Mark
	// caughtUpThrough and caughtUpAbove are the catch-up's high-water mark: every event of the
	// window at or below caughtUpThrough, and each event above it whose id caughtUpAbove holds,
	// was read by a pass on which no ask failed, and so applied or found to need nothing.
	caughtUpThrough int64
	caughtUpAbove   []int64
	// owed is what the last tick found owed, nil until a pass has run.
	owed atomic.Pointer[DecisionStepOwed]
}

// DecisionStepOwed is what the step's last pass found the restore and the conversion owe, as
// /healthz answers it in `decisions_step_owed`: the decisions documents still standing, the
// answered asks whose block left their spec, the stored lists of gated steps that hold
// `decisions`, and their sum. A deploy check reads total 0 as the step having nothing left to do.
type DecisionStepOwed struct {
	DecisionsDocuments int64 `json:"decisions_documents"`
	AnswersLeftSpec    int64 `json:"answers_left_spec"`
	GatedStepsLists    int64 `json:"gated_steps_lists"`
	Total              int64 `json:"total"`
}

// Owed is what the step's last pass found owed, nil before a pass has run - and so for as long as
// DISPATCH_TEST_HOLD_DECISIONS_STEP holds the step - and on a nil step.
func (s *DecisionStep) Owed() *DecisionStepOwed {
	if s == nil {
		return nil
	}
	return s.owed.Load()
}

// countDecisionStepOwed reads what the restore and the conversion owe, in one statement. It is
// the one reading of those three counts, which the finders of the restore and the conversion
// replace part by part:
//
//   - a decisions document is an artifact whose role is `decisions` or whose slug is
//     `decisions-md`;
//   - an answer whose block left its spec is an answered `question` ask, as its node's
//     answer_state holds it, whose row's block_artifact_id is its issue's primary document and
//     whose block id no `document_blocks` row of type `ask` holds in that document, leaving out a
//     spec whose block index is owed (document_block_versions.version null), whose blocks are not
//     known yet;
//   - a list of gated steps that holds `decisions` is a projects row whose gated_steps does.
func countDecisionStepOwed(ctx context.Context, q graphstore.Queryer) (DecisionStepOwed, error) {
	var owed DecisionStepOwed
	if err := q.QueryRow(ctx, `
		select
			(select count(*) from artifacts a where a.role = 'decisions' or a.slug = 'decisions-md'),
			(select count(*) from asks k
			 join artifacts spec on spec.id = k.block_artifact_id and spec.is_primary
			 join document_block_versions indexed on indexed.artifact_id = spec.id and indexed.version is not null
			 where k.kind = 'question' and k.block_id is not null
			   and (select t.to_id from graph_tuples t
			        where t.from_kind = $1 and t.from_id = k.id::text and t.kind = $2 and t.to_kind = $3 and t.asserted
			          and t.seq = (select max(n.seq) from graph_tuples n
			                       where n.from_kind = t.from_kind and n.from_id = t.from_id
			                         and n.kind = t.kind and n.to_kind = t.to_kind)) = $4
			   and not exists (select 1 from document_blocks d
			                   where d.artifact_id = spec.id and d.block_id = k.block_id and d.type = 'ask')),
			(select count(*) from projects p where 'decisions' = any(p.gated_steps))
	`, graphstore.KindAsk, graphstore.TupleAnswerState, graphstore.KindValue, asks.StateAnswered).Scan(
		&owed.DecisionsDocuments, &owed.AnswersLeftSpec, &owed.GatedStepsLists); err != nil {
		return DecisionStepOwed{}, fmt.Errorf("count what the decisions step owes: %w", err)
	}
	owed.Total = owed.DecisionsDocuments + owed.AnswersLeftSpec + owed.GatedStepsLists
	return owed, nil
}

// NewDecisionStep is the step of a process serving commit, the commit it registers
// (processes.Instance.Commit), over database, logging through logger (slog.Default when nil).
func NewDecisionStep(database *store.Store, commit string, logger *slog.Logger) *DecisionStep {
	if logger == nil {
		logger = slog.Default()
	}
	return &DecisionStep{
		database: database, commit: commit, logger: logger, interval: BlockIndexReconcileInterval,
		catchUpOwed: true, horizon: events.NewHorizon(database.Pool),
	}
}

// decisionStepLock is the session advisory lock the step's passes take ("DDECISNS"), a single
// 64-bit key apart from every other single-key advisory lock of the server.
const decisionStepLock int64 = 0x4444454349534E53

// decisionStepApplication is what pg_stat_activity names the connection the step holds its lock
// on.
const decisionStepApplication = "dispatch-decision-step"

// Boot is the step's pass at boot: it waits for the step's lock, then probes what each phase owes
// (probe) and runs phase 1 where a row stands unmarked. It answers a failure of either, which
// stops the boot.
func (s *DecisionStep) Boot(ctx context.Context) error {
	conn, err := s.database.Pool.Dedicated(ctx, decisionStepApplication)
	if err != nil {
		return fmt.Errorf("open the decision step's connection: %w", err)
	}
	defer closeDecisionStepConn(conn)
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, decisionStepLock); err != nil {
		return fmt.Errorf("take the decision step's lock: %w", err)
	}
	work, err := s.probe(ctx)
	if err != nil || !work.backfill {
		return err
	}
	return s.backfill(ctx)
}

// Tick is one pass of the step on its tick. It probes what each phase owes first (probe), on the
// shared pool, so a tick with no work - every tick once the fleet is one build and every row is
// stated - costs those reads and dials no connection. Otherwise it takes the step's lock on a
// connection of its own, skips where another process holds it, since the holder is doing the
// work, and runs each phase with work. A failure of a phase is logged and tried again at the next
// tick; it answers a failure of the probes or of the lock.
func (s *DecisionStep) Tick(ctx context.Context) error {
	work, err := s.probe(ctx)
	if err != nil {
		return err
	}
	catchUp := work.other || s.catchUpOwed
	if !work.backfill && !catchUp {
		return nil
	}
	conn, err := s.database.Pool.Dedicated(ctx, decisionStepApplication)
	if err != nil {
		return fmt.Errorf("open the decision step's connection: %w", err)
	}
	defer closeDecisionStepConn(conn)
	var locked bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, decisionStepLock).Scan(&locked); err != nil {
		return fmt.Errorf("take the decision step's lock: %w", err)
	}
	if !locked {
		return nil
	}
	if work.backfill {
		if err := s.backfill(ctx); err != nil {
			s.logger.Error("dispatch: decisions step backfill; the next tick tries again", "error", err)
		}
	}
	if catchUp {
		failed, err := s.catchUp(ctx)
		if err != nil {
			s.logger.Error("dispatch: decisions step catch-up; the next tick tries again", "error", err)
		}
		s.catchUpOwed = work.other || failed || err != nil
	}
	return nil
}

// Run ticks at once and then every interval (BlockIndexReconcileInterval) until ctx ends. It never
// stops for want of work: a process of another build can go live after a tick found none, and the
// next tick catches up what it writes. A tick that fails is logged and tried again at the next one.
func (s *DecisionStep) Run(ctx context.Context) {
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("dispatch: decisions step tick; the next tick tries again", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval):
		}
	}
}

// decisionStepWork is what a pass's probes found the step's phases owe.
type decisionStepWork struct {
	// backfill is whether phase 1 has a row to state (graphcensus.AsksOwed).
	backfill bool
	// other is whether processes.Live lists a process of another build.
	other bool
}

// probe reads what a pass owes: what the restore and the conversion owe, which Owed answers from
// then on, whether phase 1 has a row to state, and whether a process of another build is live.
// These are the reads a tick with no work costs: one statement for the counts, one probe of each
// kind phase 1 states, each stopping at its first row, and one read of the live processes.
func (s *DecisionStep) probe(ctx context.Context) (decisionStepWork, error) {
	owed, err := countDecisionStepOwed(ctx, s.database.Pool)
	if err != nil {
		return decisionStepWork{}, err
	}
	s.owed.Store(&owed)
	var work decisionStepWork
	if work.backfill, err = graphcensus.AsksOwed(ctx, s.database.Pool); err != nil {
		return decisionStepWork{}, err
	}
	if work.other, err = s.anotherBuildLive(ctx); err != nil {
		return decisionStepWork{}, err
	}
	return work, nil
}

// backfill runs phase 1 and logs what each of its passes stated.
func (s *DecisionStep) backfill(ctx context.Context) error {
	done, err := graphcensus.BackfillAsks(ctx, s.database)
	for _, kind := range done {
		if kind.Rows > 0 {
			s.logger.Info("dispatch: decisions step stated rows written before this release on the store",
				"kind", kind.Kind, "rows", kind.Rows, "tuples", kind.Tuples, "remaining", kind.Remaining)
		}
	}
	if err != nil {
		return fmt.Errorf("decisions step, the backfill: %w", err)
	}
	return nil
}

// anotherBuildLive reports whether processes.Live lists a process serving a commit other than
// this process's own. An empty commit is one build with every other empty commit.
func (s *DecisionStep) anotherBuildLive(ctx context.Context) (bool, error) {
	live, err := processes.Live(ctx, s.database.Pool)
	if err != nil {
		return false, err
	}
	for _, process := range live {
		if process.Commit != s.commit {
			return true, nil
		}
	}
	return false, nil
}

// caughtUpAsk is the ask an event of the catch-up's read names, aliased e: a follower event names
// it as ask_id, every other as the ask's own id.
const caughtUpAsk = `case when e.type like 'ask.follower_%' then e.payload->>'ask_id' else e.payload->>'id' end`

// caughtUpRead is the catch-up's one read of a pass: every event of the window above $1 - an
// answer, a resolution (a decline is one), an opening (a reopen, or settlement's restoration), a
// follower added or removed - that no tuple of its ask's node states, since none carries the
// event's id as its source_event, and whose id is none of $2, which an earlier pass read already.
// Each row is the event and the ask it names, in the order of the asks and, within an ask, of the
// events' ids. It reads events through their primary key from $1 on, so a pass reads what was
// appended since the passes before it and not the whole window again, and each event's tuples
// through an index keyed on its ask's node (TestTheCatchUpReadsItsWindowThroughIndexes). The
// tuples are looked for by a scalar subquery, `(select … limit 1) is null`, rather than by NOT
// EXISTS, which Postgres plans as an anti-join over every tuple of every ask, read once and kept
// beside the events (copied_asks.go's owedCopiesQuery meets the same with a hashed subplan); the
// subquery runs once for each event, each run one probe of the index.
const caughtUpRead = `
	select e.id, e.type, e.payload, e.actor, e.created_at, coalesce(` + caughtUpAsk + `, '') as ask
	from events e
	where e.id > $1
	  and e.type in ('ask.answered', 'ask.resolved', 'ask.opened', 'ask.follower_added', 'ask.follower_removed')
	  and e.id <> all($2::bigint[])
	  and (select 1 from graph_tuples t
	       where t.from_kind = 'ask' and t.from_id = ` + caughtUpAsk + ` and t.source_event = e.id
	       limit 1) is null
	order by ask, e.id`

// decisionStepMarksKept bounds the marks of the event log the catch-up keeps (writerFloor).
const decisionStepMarksKept = 8

// catchUp is phase 2, and reports whether an ask failed. Its window opens at the newest
// server_build_announcements row naming a commit other than this process's own: the fleet last
// stood whole on another build then, and every process of this build started after it. Where none
// names another commit, as on a database this build created, it opens at the first event. A pass
// reads the window's events no tuple states once (caughtUpRead), from the step's high-water mark
// on, and catches each ask up in a transaction of its own (catchUpAsk); a failure on one is logged
// and leaves the others to go on.
//
// An event that needs nothing - one older than the node's newest tuple of its attribute, a follow
// the backfill already stated at the sentinel, an event of an ask the backfill has not marked yet -
// states nothing, so no tuple ever names it and every pass would read it again. The step keeps
// instead what its passes read. After a pass on which no ask failed, every event at or below the
// floor it read from (writerFloor) was final before it read, so it read each of them, and the
// high-water mark moves up to that floor; the ids it read above the floor are kept until the mark
// passes them. A pass on which an ask failed moves neither, and the next pass reads that ask's
// events again, with whatever else needed nothing. The mark is the process's own: a process that
// takes the step over reads its window again from where it opens, and an event already stated is
// left out of its read as it is of every read.
func (s *DecisionStep) catchUp(ctx context.Context) (bool, error) {
	var start int64
	if err := s.database.Pool.QueryRow(ctx, `
		select coalesce((
			select e.id from events e
			where e.created_at < (select a.announced_at from server_build_announcements a
			                      where a.commit <> $1 order by a.seq desc limit 1)
			order by e.id desc limit 1), 0)
	`, s.commit).Scan(&start); err != nil {
		return false, fmt.Errorf("read where the catch-up's window opens: %w", err)
	}
	floor, newest, err := s.writerFloor(ctx)
	if err != nil {
		return false, fmt.Errorf("read which events the catch-up reads final: %w", err)
	}
	if newest < s.caughtUpThrough {
		// The log holds no event as far as the mark (TRUNCATE ... RESTART IDENTITY): its ids are
		// a new log's, and nothing the mark says holds of them.
		s.caughtUpThrough, s.caughtUpAbove = 0, nil
	}
	read := s.caughtUpAbove
	if read == nil {
		// A nil list is bound as null, and no id is unequal to every member of null.
		read = []int64{}
	}
	rows, err := s.database.Pool.Query(ctx, caughtUpRead, max(start, s.caughtUpThrough), read)
	if err != nil {
		return false, fmt.Errorf("read the events no tuple states: %w", err)
	}
	unstated, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (caughtUpEvent, error) {
		var event caughtUpEvent
		var actor []byte
		if err := row.Scan(&event.id, &event.eventType, &event.payload, &actor, &event.at, &event.ask); err != nil {
			return event, err
		}
		if err := json.Unmarshal(actor, &event.actor); err != nil {
			return event, fmt.Errorf("read the actor of event %d: %w", event.id, err)
		}
		return event, nil
	})
	if err != nil {
		return false, fmt.Errorf("read the events no tuple states: %w", err)
	}
	failed := false
	for first := 0; first < len(unstated); {
		last := first + 1
		for last < len(unstated) && unstated[last].ask == unstated[first].ask {
			last++
		}
		askID := unstated[first].ask
		applied, err := s.catchUpAsk(ctx, askID, unstated[first:last])
		first = last
		if err != nil {
			failed = true
			s.logger.Error("dispatch: decisions step could not catch up an ask; the next tick tries again",
				"ask", askID, "error", err)
			continue
		}
		if applied > 0 {
			s.logger.Info("dispatch: decisions step caught up the events another build wrote", "ask", askID, "events", applied)
		}
	}
	if !failed {
		s.keepCaughtUp(floor, unstated)
	}
	return failed, nil
}

// writerFloor marks the event log now and answers the highest id at or below which no event can
// still be open (events.Horizon.WriterFloor), read from the marks the catch-up's passes took,
// newest first, or, where none of them counts, from the horizon some process persisted, and the
// newest id the new mark saw. A mark counts once every transaction that holds a write lock on
// events took its id after it: this pass's own does on a log no write holds open, and under writes
// that overlap without a break an earlier pass's does, as soon as the writes open when it was
// taken have ended. It waits on no transaction.
func (s *DecisionStep) writerFloor(ctx context.Context) (int64, int64, error) {
	mark, err := s.horizon.Mark(ctx)
	if err != nil {
		return 0, 0, err
	}
	s.marks = slices.Insert(s.marks, 0, mark)
	if len(s.marks) > decisionStepMarksKept {
		s.marks = s.marks[:decisionStepMarksKept]
	}
	floor, ok, err := s.horizon.WriterFloor(ctx, s.marks...)
	if err != nil || ok {
		return floor, mark.Newest, err
	}
	persisted, err := s.horizon.Persisted(ctx)
	return max(floor, persisted), mark.Newest, err
}

// keepCaughtUp moves the high-water mark over what a pass on which no ask failed read: every event
// at or below floor, and the ids of what it read above it, beside those an earlier pass read above
// the mark that it has not passed yet.
func (s *DecisionStep) keepCaughtUp(floor int64, read []caughtUpEvent) {
	through := max(s.caughtUpThrough, floor)
	above := s.caughtUpAbove[:0]
	for _, id := range s.caughtUpAbove {
		if id > through {
			above = append(above, id)
		}
	}
	for _, event := range read {
		if event.id > through {
			above = append(above, event.id)
		}
	}
	s.caughtUpThrough, s.caughtUpAbove = through, above
}

// caughtUpEvent is one event the catch-up read (caughtUpRead): the event, and the ask it names.
type caughtUpEvent struct {
	id        int64
	eventType string
	payload   []byte
	actor     model.Actor
	at        time.Time
	ask       string
}

// catchUpAsk applies, in one transaction, the events of askID a pass read (unstated), in the order
// of their ids. It takes the store's lock order (api/inbox_read.go's markInboxReadThreads), as
// every writer of an ask's answer does: the ask's owner row first - its issue, or the document an
// ask on no issue belongs to, read off the row without a lock and taken through
// events.LockOwnerRows - then the ask's row, `for no key update`, and only then the nodes and the
// tuples asks.Settle and asks.CatchUpFollower write. An ask whose owner moved between the two is
// tried again at the next pass. An ask whose row is gone, or names no node, is the backfill's: the
// row is stated whole when it is marked. An answer event older than the node's newest
// answer_state, and a follower event older than the node's newest followed_by to that session, is
// skipped, so an old event caught up after a newer write never puts back what that write replaced.
// Every other answer event is written as asks.Settle writes the event's own state, answer and
// resolution, under the event, by its actor at its time; a follower event is stated as the row
// stands (asks.CatchUpFollower). It answers how many events it applied.
func (s *DecisionStep) catchUpAsk(ctx context.Context, askID string, unstated []caughtUpEvent) (int, error) {
	if askID == "" {
		return 0, nil
	}
	var issue, document string
	err := s.database.Pool.QueryRow(ctx, `
		select coalesce(issue_key, ''), coalesce(artifact_id::text, '') from asks where id = $1::uuid
	`, askID).Scan(&issue, &document)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the owner of ask %s: %w", askID, err)
	}
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	owners := events.OwnerRows{}
	owners.Add(issue, document, "ask "+askID)
	if err := events.LockOwnerRows(ctx, tx, owners); err != nil {
		return 0, err
	}
	var marked bool
	var heldIssue, heldDocument string
	err = tx.QueryRow(ctx, `
		select node_kind is not null, coalesce(issue_key, ''), coalesce(artifact_id::text, '')
		from asks where id = $1::uuid for no key update
	`, askID).Scan(&marked, &heldIssue, &heldDocument)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !marked) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lock ask %s: %w", askID, err)
	}
	if heldIssue != issue || heldDocument != document {
		return 0, fmt.Errorf("ask %s moved to another owner while its catch-up took the owner's row", askID)
	}
	applied := 0
	for _, event := range unstated {
		done, err := applyCaughtUpEvent(ctx, tx, askID, event)
		if err != nil {
			return 0, fmt.Errorf("apply event %d (%s): %w", event.id, event.eventType, err)
		}
		if done {
			applied++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit the catch-up of ask %s: %w", askID, err)
	}
	return applied, nil
}

// applyCaughtUpEvent applies one event a pass read, in catchUpAsk's transaction, and reports
// whether it stated anything.
func applyCaughtUpEvent(ctx context.Context, tx pgx.Tx, askID string, event caughtUpEvent) (bool, error) {
	if event.eventType == "ask.follower_added" || event.eventType == "ask.follower_removed" {
		var payload model.AskFollowerEventPayload
		if err := json.Unmarshal(event.payload, &payload); err != nil {
			return false, err
		}
		newest, err := newestSource(ctx, tx, `t.kind = $2 and t.to_kind = $3 and t.to_id = $4`,
			askID, graphstore.TupleFollowedBy, graphstore.KindSession, payload.SessionID)
		if err != nil || event.id < newest {
			return false, err
		}
		return asks.CatchUpFollower(ctx, tx, askID, payload.SessionID, event.eventType == "ask.follower_added", event.actor, event.at, event.id)
	}
	var recorded model.Ask
	if err := json.Unmarshal(event.payload, &recorded); err != nil {
		return false, err
	}
	switch recorded.State {
	case asks.StateOpen, asks.StateAnswered, asks.StateResolved:
	default:
		return false, fmt.Errorf("the event records the answer state %q", recorded.State)
	}
	newest, err := newestSource(ctx, tx, `t.kind = $2 and t.to_kind = $3`, askID, graphstore.TupleAnswerState, graphstore.KindValue)
	if err != nil || event.id < newest {
		return false, err
	}
	change := asks.Recorded(recorded.State, recorded.Answer, recorded.Resolution)
	change.Who, change.At, change.Event = event.actor, event.at, event.id
	if _, err := asks.Settle(ctx, tx, askID, change); err != nil {
		return false, err
	}
	return true, nil
}

// newestSource is the source_event of the newest tuple out of askID's node that narrow keeps, 0
// where there is none.
func newestSource(ctx context.Context, tx pgx.Tx, narrow string, askID string, args ...any) (int64, error) {
	var newest int64
	if err := tx.QueryRow(ctx, `
		select coalesce((select t.source_event from graph_tuples t
		                 where t.from_kind = 'ask' and t.from_id = $1 and `+narrow+`
		                 order by t.seq desc limit 1), 0)
	`, append([]any{askID}, args...)...).Scan(&newest); err != nil {
		return 0, fmt.Errorf("read the newest tuple of ask %s: %w", askID, err)
	}
	return newest, nil
}

// closeDecisionStepConn closes the step's connection, which releases its lock, bounded so a
// connection gone half-open cannot hold the caller.
func closeDecisionStepConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}
