package docs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
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
// (Boot), as FillDocumentSearch does: this release reads every answer from the node, and the
// release before keeps serving through a roll until this process serves. It then runs on a tick of
// its own (Run) at BlockIndexReconcileInterval while any phase has work. Phase 1 has work while an
// unmarked row stands (graphcensus.AsksOwed). Phase 2 has work on each tick while processes.Live
// lists a process of another build, and once more on the first tick that finds the fleet one build,
// which is also a process's first tick: the window it reads opens before this process started, so
// a restart of a process on a fleet that stood whole never misses a write. What the restore and
// the conversion owe (DecisionStepOwed) is work too, and each pass counts it again, which /healthz
// answers (Owed).
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
	// catchUpOwed is whether phase 2 owes a pass on a tick that finds the fleet one build: true
	// until the first such tick runs it, and again after any tick that finds another build live.
	catchUpOwed bool
	// owed is what the last pass that ran found owed, nil until a pass has run.
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
	return &DecisionStep{database: database, commit: commit, logger: logger, catchUpOwed: true}
}

// decisionStepLock is the session advisory lock the step's passes take ("DDECISNS"), a single
// 64-bit key apart from every other single-key advisory lock of the server.
const decisionStepLock int64 = 0x4444454349534E53

// decisionStepApplication is what pg_stat_activity names the connection the step holds its lock
// on.
const decisionStepApplication = "dispatch-decision-step"

// Boot is the step's pass at boot: it waits for the step's lock, then runs phase 1. It answers
// phase 1's failure, which stops the boot.
func (s *DecisionStep) Boot(ctx context.Context) error {
	conn, err := s.database.Pool.Dedicated(ctx, decisionStepApplication)
	if err != nil {
		return fmt.Errorf("open the decision step's connection: %w", err)
	}
	defer closeDecisionStepConn(conn)
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, decisionStepLock); err != nil {
		return fmt.Errorf("take the decision step's lock: %w", err)
	}
	_, err = s.pass(ctx, true)
	return err
}

// Tick is one pass of the step on its tick: it skips where another process holds the step's lock,
// and otherwise runs each phase with work. It reports whether any phase had work, which is whether
// the tick runs again; a tick that skipped reports true, since its holder's work is not read here.
func (s *DecisionStep) Tick(ctx context.Context) (bool, error) {
	conn, err := s.database.Pool.Dedicated(ctx, decisionStepApplication)
	if err != nil {
		return true, fmt.Errorf("open the decision step's connection: %w", err)
	}
	defer closeDecisionStepConn(conn)
	var locked bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, decisionStepLock).Scan(&locked); err != nil {
		return true, fmt.Errorf("take the decision step's lock: %w", err)
	}
	if !locked {
		return true, nil
	}
	return s.pass(ctx, false)
}

// Run ticks at once and then every BlockIndexReconcileInterval while the tick reports work, until
// ctx ends. A tick that fails is logged and tried again at the next one.
func (s *DecisionStep) Run(ctx context.Context) {
	for {
		more, err := s.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("dispatch: decisions step tick; the next tick tries again", "error", err)
			more = true
		}
		if !more {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(BlockIndexReconcileInterval):
		}
	}
}

// pass runs one pass under the step's lock: at boot phase 1 alone, whose failure it answers, and
// on a tick both phases, whose failures it logs. It counts what the restore and the conversion owe
// first, which Owed answers from then on, and reports whether any phase had work.
func (s *DecisionStep) pass(ctx context.Context, boot bool) (bool, error) {
	owed, err := countDecisionStepOwed(ctx, s.database.Pool)
	if err != nil {
		return true, err
	}
	s.owed.Store(&owed)
	backfillOwed, err := graphcensus.AsksOwed(ctx, s.database.Pool)
	if err != nil {
		return true, err
	}
	other, err := s.anotherBuildLive(ctx)
	if err != nil {
		return true, err
	}
	catchUp := other || s.catchUpOwed
	if !backfillOwed && !catchUp && owed.Total == 0 {
		return false, nil
	}
	if backfillOwed {
		done, err := graphcensus.BackfillAsks(ctx, s.database)
		for _, kind := range done {
			if kind.Rows > 0 {
				s.logger.Info("dispatch: decisions step stated rows written before this release on the store",
					"kind", kind.Kind, "rows", kind.Rows, "tuples", kind.Tuples, "remaining", kind.Remaining)
			}
		}
		if err != nil {
			if boot {
				return true, fmt.Errorf("decisions step, the backfill: %w", err)
			}
			s.logger.Error("dispatch: decisions step backfill; the next tick tries again", "error", err)
		}
	}
	if boot {
		return true, nil
	}
	if catchUp {
		if err := s.catchUp(ctx); err != nil {
			s.logger.Error("dispatch: decisions step catch-up; the next tick tries again", "error", err)
		} else {
			s.catchUpOwed = other
		}
	}
	return true, nil
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

// caughtUpTypes are the events phase 2 reads: an answer, a resolution (a decline is one), an
// opening (a reopen, or settlement's restoration), and a follower added or removed.
var caughtUpTypes = []string{"ask.answered", "ask.resolved", "ask.opened", "ask.follower_added", "ask.follower_removed"}

// caughtUpAsk is the ask an event of caughtUpTypes names, aliased e: a follower event names it as
// ask_id, every other as the ask's own id.
const caughtUpAsk = `case when e.type like 'ask.follower_%' then e.payload->>'ask_id' else e.payload->>'id' end`

// unstatedEvents keeps the events of the window no tuple of their ask's node states: none carries
// the event's id as its source_event.
const unstatedEvents = `
	e.id > $1 and e.type = any($2)
	and not exists (select 1 from graph_tuples t
	                where t.from_kind = 'ask' and t.from_id = ` + caughtUpAsk + ` and t.source_event = e.id)`

// catchUp is phase 2. Its window opens at the newest server_build_announcements row naming a commit
// other than this process's own: the fleet last stood whole on another build then, and every
// process of this build started after it. Where none names another commit, as on a database this
// build created, it opens at the first event. Each ask with an event in the window that no tuple
// states is caught up in a transaction of its own (catchUpAsk); a failure on one is logged and
// leaves the others to go on.
func (s *DecisionStep) catchUp(ctx context.Context) error {
	var start int64
	if err := s.database.Pool.QueryRow(ctx, `
		select coalesce((
			select e.id from events e
			where e.created_at < (select a.announced_at from server_build_announcements a
			                      where a.commit <> $1 order by a.seq desc limit 1)
			order by e.id desc limit 1), 0)
	`, s.commit).Scan(&start); err != nil {
		return fmt.Errorf("read where the catch-up's window opens: %w", err)
	}
	rows, err := s.database.Pool.Query(ctx, `
		select distinct `+caughtUpAsk+` from events e where `+unstatedEvents+` order by 1
	`, start, caughtUpTypes)
	if err != nil {
		return fmt.Errorf("read the asks with events no tuple states: %w", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read the asks with events no tuple states: %w", err)
	}
	for _, askID := range owed {
		applied, err := s.catchUpAsk(ctx, askID, start)
		if err != nil {
			s.logger.Error("dispatch: decisions step could not catch up an ask; the next tick tries again",
				"ask", askID, "error", err)
			continue
		}
		if applied > 0 {
			s.logger.Info("dispatch: decisions step caught up the events another build wrote", "ask", askID, "events", applied)
		}
	}
	return nil
}

// caughtUpEvent is one event catchUpAsk applies.
type caughtUpEvent struct {
	id        int64
	eventType string
	payload   []byte
	actor     model.Actor
	at        time.Time
}

// catchUpAsk applies, in one transaction, every event of askID in the window that no tuple states,
// in the order of their ids. It locks the ask's row first, as every writer of its answer does, so
// the events it reads after are every event a committed write of the row appended. An ask whose row
// is gone, or names no node, is the backfill's: the row is stated whole when it is marked. An
// answer event older than the node's newest answer_state, and a follower event older than the
// node's newest followed_by to that session, is skipped, so an old event caught up after a newer
// write never puts back what that write replaced. Every other answer event is written as asks.Settle
// writes the event's own state, answer and resolution, under the event, by its actor at its time;
// a follower event is stated as the row stands (asks.CatchUpFollower). It answers how many events
// it applied.
func (s *DecisionStep) catchUpAsk(ctx context.Context, askID string, start int64) (int, error) {
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var marked bool
	err = tx.QueryRow(ctx, `select node_kind is not null from asks where id = $1::uuid for no key update`, askID).Scan(&marked)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !marked) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lock ask %s: %w", askID, err)
	}
	rows, err := tx.Query(ctx, `
		select e.id, e.type, e.payload, e.actor, e.created_at from events e
		where `+unstatedEvents+` and `+caughtUpAsk+` = $3
		order by e.id
	`, start, caughtUpTypes, askID)
	if err != nil {
		return 0, fmt.Errorf("read the events of ask %s no tuple states: %w", askID, err)
	}
	var unstated []caughtUpEvent
	for rows.Next() {
		var event caughtUpEvent
		var actor []byte
		if err := rows.Scan(&event.id, &event.eventType, &event.payload, &actor, &event.at); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read an event of ask %s: %w", askID, err)
		}
		if err := json.Unmarshal(actor, &event.actor); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read the actor of event %d: %w", event.id, err)
		}
		unstated = append(unstated, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read the events of ask %s no tuple states: %w", askID, err)
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

// applyCaughtUpEvent applies one event catchUpAsk read, and reports whether it stated anything.
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
