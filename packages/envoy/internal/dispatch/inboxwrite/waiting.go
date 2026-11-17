package inboxwrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The assignee arm (dispatch://INFRA-1152 R2 and R4, decision 10, departure P4): an open ask
// reaches the person it waits on, the person asks.AskedOfPersonExpression names - the person it
// was handed to, else its issue's assignee, else its project's default assignee - and that person
// takes part in its thread. The ask's create appends both tuples (Record, for an Opened write),
// and each write that changes who an ask waits on moves them (MoveWaiting): patchIssue's
// assignee, putAskAskedOf, putProjectDefaultAssignee, the restore of the asks an issue's close
// withdrew and a settlement's restore of an ask it retracted. Neither raises a user_threads
// version (P13): the old triggers moved no row for either, and the census counts only the
// deliveries a reply caused.
//
// The list follows the assignment only if no two of those writes interleave, so each serialises
// against the others on the rows the rule reads, in the lock order written on api/inbox_read.go's
// markInboxReadThreads. An issue's assignee and an ask's asked_of are written under the issue's or
// the document's owner row, which every create and every move of an ask on that owner takes
// first. A project's default is written under its project row `for no key update`
// (putProjectDefaultAssignee), so a write that reads the default to decide a delivery holds that
// row `for share` first (HoldDefault). And a move holds the asks rows it moves (MoveWaiting), so
// two moves of one ask, and a move and an answer, take turns there, and the move reads who the
// ask waits on, and who its delivery stands to, only once it holds them.

// waitingOnTuples is what an ask reaching the person it waits on appends: participates_in from
// waiting to askID and delivered_to from askID to waiting, made by who at at, under event. The
// asker - asker is their canonical login, "" for an ask a session or nobody asked - is reached by
// neither: their own ask draws no delivery to them, and they take part in it already as its asker
// (Record's step 1).
func waitingOnTuples(askID, waiting, asker string, at time.Time, who model.Actor, event int64) []graphstore.Tuple {
	waiting = model.CanonicalLogin(waiting)
	if waiting == "" || waiting == asker {
		return nil
	}
	return []graphstore.Tuple{{
		FromKind: graphstore.KindPerson, FromID: waiting, Kind: graphstore.TupleParticipatesIn,
		ToKind: graphstore.KindAsk, ToID: askID,
		At: at, Who: who, SourceEvent: event, Asserted: true,
	}, {
		FromKind: graphstore.KindAsk, FromID: askID, Kind: graphstore.TupleDeliveredTo,
		ToKind: graphstore.KindPerson, ToID: waiting,
		At: at, Who: who, SourceEvent: event, Asserted: true,
	}}
}

// askerOf is the canonical login of the person author names, and "" for a session or nobody.
func askerOf(author model.Actor) string {
	if kind, id, named := graphstore.AuthorOf(author); named && kind == graphstore.KindPerson {
		return id
	}
	return ""
}

// HoldDefault takes the row of the project an ask on the owner issueKey or artifactID falls
// through to `for share`, where it falls through to that project's default assignee: the owner is
// a project document, or an issue nobody holds. A write that is about to read who such an ask
// waits on, to deliver it - an ask's create, a change of an issue's assignee to nobody, the
// restore of an issue's withdrawn asks, a settlement that may restore an ask it retracted - takes
// it after its owner row and before any asks row or node it writes, so a move of the default
// (putProjectDefaultAssignee, which holds the row `for no key update`) either commits before the
// write reads the default or waits for the write to commit and then finds the ask it delivered.
// The caller holds the owner row, under which an issue's assignee does not move; an owner that
// does not fall through is read and nothing is locked, so its write takes no lock on projects.
func HoldDefault(ctx context.Context, tx pgx.Tx, issueKey, artifactID *string) error {
	var project string
	if err := tx.QueryRow(ctx, `
		select coalesce(i.project_key, ar.project_key, '')
		from (select 1) as one
		left join issues i on i.key = $1 and i.assignee is null
		left join artifacts ar on ar.id = $2::uuid and ar.issue_key is null
	`, issueKey, artifactID).Scan(&project); err != nil {
		return fmt.Errorf("read whether an ask falls through to its project's default: %w", err)
	}
	if project == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `select 1 from projects where key = $1 for share`, project); err != nil {
		return fmt.Errorf("hold the default assignee of project %s: %w", project, err)
	}
	return nil
}

// waitingOn is the person ask askID waits on while it is open (asks.AskedOfPersonExpression), and
// "" for an ask that is not open or waits on nobody. A create runs it after its insert, holding its
// owner row and, where the ask falls through to its project's default, the project's row
// (HoldDefault), so nothing it reads moves before the create commits: this build's creates store
// an open ask, and a write that stores an ask already closed - the seed's - reaches nobody, as the
// backfill gives an older build's ask that person's tuples only while it is open. A create that
// read the person itself under those same locks hands them to Record instead (Write.WaitingOn),
// and this read is not made.
func waitingOn(ctx context.Context, tx pgx.Tx, askID string) (string, error) {
	var waiting string
	err := tx.QueryRow(ctx, `
		select coalesce(`+asks.AskedOfPersonExpression+`, '')
		from asks a`+asks.AskedOfPersonJoins+`
		where a.id = $1::uuid and a.state = $2
	`, askID, asks.StateOpen).Scan(&waiting)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the person ask %s waits on: %w", askID, err)
	}
	return waiting, nil
}

// A Move is a write that changes who open asks wait on, as MoveWaiting reads it: a select of the
// ids of the asks it may move, and the event each move is recorded under. Each writer names its
// own (HandedOn, Reassigned, DefaultMoved, Restored).
type Move struct {
	// what names the write in an error.
	what string
	// candidates selects the id of each ask the write may move; args are its parameters, from $1.
	candidates string
	args       []any
	// event is the write's event, which every tuple of the move carries, and events, for a write
	// that recorded each ask under an event of its own, that event by ask id.
	event  model.Event
	events map[string]model.Event
}

// eventOf is the event the move of ask is recorded under.
func (m Move) eventOf(ask string) model.Event {
	if event, recorded := m.events[ask]; recorded {
		return event
	}
	return m.event
}

// HandedOn is putAskAskedOf's write, recorded under event: ask askID.
func HandedOn(askID string, event model.Event) Move {
	return Move{
		what:       "the hand-on of ask " + askID,
		candidates: `select $1::uuid`,
		args:       []any{askID},
		event:      event,
	}
}

// Reassigned is patchIssue's change of the assignee of issueKey, recorded under event: every open
// ask of the issue handed to nobody, which follows its issue.
func Reassigned(issueKey string, event model.Event) Move {
	return Move{
		what: "the change of " + issueKey + "'s assignee",
		candidates: `
			select a.id from asks a
			where a.issue_key = $1 and a.state = '` + asks.StateOpen + `' and a.asked_of is null`,
		args:  []any{issueKey},
		event: event,
	}
}

// DefaultMoved is putProjectDefaultAssignee's move of project's default, recorded under event:
// every open ask that falls through to that default, handed to nobody and on an issue of the
// project nobody holds or on a document of the project.
func DefaultMoved(project string, event model.Event) Move {
	return Move{
		what: "the move of project " + project + "'s default assignee",
		candidates: `
			select a.id
			from issues i
			join asks a on a.issue_key = i.key
			where i.project_key = $1 and i.assignee is null and a.state = '` + asks.StateOpen + `' and a.asked_of is null
			union all
			select a.id
			from artifacts ar
			join asks a on a.artifact_id = ar.id
			where ar.project_key = $1 and a.issue_key is null and a.state = '` + asks.StateOpen + `' and a.asked_of is null`,
		args:  []any{project},
		event: event,
	}
}

// Restored is the restore of asks no move reached while they were closed, each now open again:
// the asks an issue's close withdrew (api's restoreIssueAsksTx), and the asks a settlement
// retracted whose blocks came back (docs' moveRestored). Each ask of events is recorded under the
// ask.opened that records its restore, and now reaches whoever it waits on as it is restored.
func Restored(events map[string]model.Event) Move {
	ids := make([]string, 0, len(events))
	for id := range events {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return Move{
		what:       fmt.Sprintf("the restore of %d withdrawn ask", len(ids)),
		candidates: `select unnest($1::uuid[])`,
		args:       []any{ids},
		events:     events,
	}
}

// standing is whether the relation of kind from fromKind/fromID to toKind/toID stands: its newest
// tuple asserts it (graphstore.CurrentTupleRead). Each end is SQL text of the statement it is
// spliced into, the kinds this package's own constants.
func standing(fromKind, fromID, kind, toKind, toID string) string {
	return `coalesce((` + graphstore.CurrentTupleRead(`t.kind`, `t.asserted`,
		`t.from_kind = '`+fromKind+`' and t.from_id = `+fromID+` and t.kind = '`+kind+`'
				and t.to_kind = '`+toKind+`' and t.to_id = `+toID) + `), false)`
}

// moveLock is MoveWaiting's first statement over candidates, a select of ask ids: the open asks
// the move may move, locked `for no key update` in ascending id, the order every write locking
// several asks rows takes them in.
func moveLock(candidates string) string {
	return `
		select a.id::text from asks a
		where a.id in (` + candidates + `) and a.state = '` + asks.StateOpen + `'
		order by a.id
		for no key update of a`
}

// moveRead is MoveWaiting's second statement, run once the asks are held: of each held ask still
// open on an open issue or on a project document, who it waits on now, who asked it, and each
// person its delivery stands to - a delivered_to from the ask whose newest tuple asserts it and
// was appended beside that person's participates_in under one event, as only this arm appends the
// two, and that no mention of them in the ask holds - with whether their participates_in stands
// and whether anything else holds them in the thread: a participates_in of theirs appended under
// an event that delivered them nothing from the ask, which is a write, an answer or a decline of
// theirs, or a mention of them in the thread (Record).
var moveRead = `
		with held as (
			select a.id::text as ask, ` + asks.AskedOfPersonExpression + ` as waiting, a.author
			from asks a` + asks.AskedOfPersonJoins + `
			where a.id = any($1::uuid[]) and a.state = '` + asks.StateOpen + `'
			  and (a.issue_key is null or i.closed_at is null)
		)
		select h.ask, h.waiting, h.author, reached.person, reached.takes_part, reached.held
		from held h
		left join lateral (
			select d.person,
			       ` + standing(graphstore.KindPerson, `d.person`, graphstore.TupleParticipatesIn, graphstore.KindAsk, `h.ask`) + ` as takes_part,
			       exists (
			           select 1 from graph_tuples p
			           where p.from_kind = '` + graphstore.KindPerson + `' and p.from_id = d.person
			             and p.kind = '` + graphstore.TupleParticipatesIn + `'
			             and p.to_kind = '` + graphstore.KindAsk + `' and p.to_id = h.ask and p.asserted
			             and not exists (
			                 select 1 from graph_tuples o
			                 where o.from_kind = '` + graphstore.KindAsk + `' and o.from_id = h.ask
			                   and o.kind = '` + graphstore.TupleDeliveredTo + `'
			                   and o.to_kind = '` + graphstore.KindPerson + `' and o.to_id = d.person
			                   and o.source_event = p.source_event and o.asserted)
			       ) as held
			from (` + graphstore.CurrentTupleRead(`t.to_id`, `t.to_id as person, t.asserted, t.source_event`,
	`t.from_kind = '`+graphstore.KindAsk+`' and t.from_id = h.ask and t.kind = '`+graphstore.TupleDeliveredTo+`'
				and t.to_kind = '`+graphstore.KindPerson+`'`) + `) d
			where d.asserted
			  and exists (
			      select 1 from graph_tuples p
			      where p.from_kind = '` + graphstore.KindPerson + `' and p.from_id = d.person
			        and p.kind = '` + graphstore.TupleParticipatesIn + `'
			        and p.to_kind = '` + graphstore.KindAsk + `' and p.to_id = h.ask
			        and p.source_event = d.source_event and p.asserted)
			  and not ` + standing(graphstore.KindAsk, `h.ask`, graphstore.TupleMentions, graphstore.KindPerson, `d.person`) + `
		) reached on true
		order by h.ask, reached.person`

// reach is one person an ask's delivery stands to, as moveRead reads them.
type reach struct {
	person          string
	takesPart, held bool
}

// MoveWaiting is the move of R2 for the write move names, in that write's transaction after its
// event: each ask it moves now reaches the person it waits on, and stops reaching whoever its
// delivery stood to before. Every tuple carries the event the move of its ask is recorded under,
// dated at that event's moment and made by its actor.
//
// It takes three steps. It locks the asks the write may move, `for no key update` in ascending
// id (moveLock), so a second move of one of them, and an answer, waits for this write to commit,
// and this write waits for theirs. Once it holds them it reads, in a statement of its own and so
// as every write it waited for left them (moveRead), who each ask waits on now and who its
// delivery stands to: the old person is read from the store, not from what the write changed, so
// a move another write committed in between is found where it left the delivery. An ask on a
// closed issue is not read: a closed issue retracts nothing, and the record stays what the write
// stored until the issue's reopen restores the ask (Restored). Then one graphstore.ToPerson call
// appends, for each ask whose delivery does not already stand to the person it waits on, that
// person's participates_in and delivered_to (waitingOnTuples), and for each other person its
// delivery stands to the retraction of that delivered_to, and of their participates_in where it
// stands and nothing else holds them in the thread and they did not ask the ask. That is at most
// four tuple rows an ask, and four statements however many asks it moves.
func MoveWaiting(ctx context.Context, tx pgx.Tx, move Move) error {
	rows, err := tx.Query(ctx, moveLock(move.candidates), move.args...)
	if err != nil {
		return fmt.Errorf("lock the asks %s may move: %w", move.what, err)
	}
	locked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("lock the asks %s may move: %w", move.what, err)
	}
	if len(locked) == 0 {
		return nil
	}
	rows, err = tx.Query(ctx, moveRead, locked)
	if err != nil {
		return fmt.Errorf("read the asks %s moved: %w", move.what, err)
	}
	defer rows.Close()
	type heldAsk struct {
		id, waiting, asker string
		reached            []reach
	}
	var held []heldAsk
	for rows.Next() {
		var ask string
		var waiting, person *string
		var author []byte
		var takesPart, stillHeld *bool
		if err := rows.Scan(&ask, &waiting, &author, &person, &takesPart, &stillHeld); err != nil {
			return fmt.Errorf("read an ask %s moved: %w", move.what, err)
		}
		if len(held) == 0 || held[len(held)-1].id != ask {
			var actor model.Actor
			if err := json.Unmarshal(author, &actor); err != nil {
				return fmt.Errorf("decode the author of ask %s: %w", ask, err)
			}
			next := heldAsk{id: ask, asker: askerOf(actor)}
			if waiting != nil {
				next.waiting = model.CanonicalLogin(*waiting)
			}
			held = append(held, next)
		}
		if person != nil {
			current := &held[len(held)-1]
			current.reached = append(current.reached, reach{person: *person, takesPart: *takesPart, held: *stillHeld})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the asks %s moved: %w", move.what, err)
	}
	var tuples []graphstore.Tuple
	for _, ask := range held {
		event := move.eventOf(ask.id)
		ended := func(tuple graphstore.Tuple) {
			tuple.At, tuple.Who, tuple.SourceEvent, tuple.Asserted = event.CreatedAt, event.Actor, event.ID, false
			tuples = append(tuples, tuple)
		}
		stands := false
		for _, reached := range ask.reached {
			if reached.person == ask.waiting {
				stands = true
				continue
			}
			ended(graphstore.Tuple{
				FromKind: graphstore.KindAsk, FromID: ask.id, Kind: graphstore.TupleDeliveredTo,
				ToKind: graphstore.KindPerson, ToID: reached.person,
			})
			if reached.takesPart && !reached.held && reached.person != ask.asker {
				ended(graphstore.Tuple{
					FromKind: graphstore.KindPerson, FromID: reached.person, Kind: graphstore.TupleParticipatesIn,
					ToKind: graphstore.KindAsk, ToID: ask.id,
				})
			}
		}
		if !stands {
			tuples = append(tuples, waitingOnTuples(ask.id, ask.waiting, ask.asker, event.CreatedAt, event.Actor, event.ID)...)
		}
	}
	if err := graphstore.ToPerson(ctx, tx, tuples); err != nil {
		return fmt.Errorf("move the person the asks wait on, for %s: %w", move.what, err)
	}
	return nil
}
