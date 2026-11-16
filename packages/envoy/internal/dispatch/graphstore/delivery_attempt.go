package graphstore

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// KindDeliveryAttempt is the node kind of one attempt to reach one session or role: one row of
// comment_deliveries or message_deliveries, which is retry machinery keyed per target per
// attempt. The attempt is a node of its own, and not a value of the comment or message it
// carries, because its number, state, envelope and errors belong to no comment and to no person.
// A delivery is the TupleDeliveredTo tuple the attempt ends in, and never a node.
const KindDeliveryAttempt = "delivery_attempt"

// The relations an attempt states besides TupleDeliveredTo. TupleOwedTo points at the session or
// role the attempt is for while its send is undecided, and ends with a retraction once the attempt
// leaves pending; TupleAnsweredBy points at the reply the attempt drew. Neither is an arm of
// graph_edges, so neither is one of refs.TupleKinds().
const (
	TupleOwedTo     = "owed_to"
	TupleAnsweredBy = "answered_by"
)

// The values of an attempt, as the kinds of the value tuples that state them: the store names of
// the attribute nodes migration 20261116102759 ties to the delivery_attempt supertag. Each is the
// attribute's own name but the attempt's state, which shares its name with a base attribute of
// every node and is stored as its type's name, `_` and its own name (pmdoc/schemanodes.StoreName):
// a value tuple of kind `state` out of the attempt would read as the node's own state.
const (
	AttemptNumber       = "attempt"
	AttemptState        = "delivery_attempt_state"
	AttemptEnvelopeID   = "envelope_id"
	AttemptError        = "error"
	AttemptResolveError = "resolve_error"
	AttemptDuplicate    = "duplicate"
	AttemptClaimedAt    = "claimed_at"
	AttemptAcceptedAt   = "accepted_at"
	AttemptAcceptedAs   = "accepted_as"
)

// attemptValueKinds and attemptRelationKinds are every tuple kind Attempted states, the set its
// read of what the store holds of an attempt is bounded to.
var (
	attemptValueKinds = []string{
		AttemptNumber, AttemptState, AttemptEnvelopeID, AttemptError, AttemptResolveError,
		AttemptDuplicate, AttemptClaimedAt, AttemptAcceptedAt, AttemptAcceptedAs,
	}
	attemptRelationKinds = []string{TupleOwedTo, TupleDeliveredTo, TupleAnsweredBy}
)

// AttemptKey names one attempt row by its table's primary key: (comment, target, attempt) of
// comment_deliveries, or (message, attempt) of message_deliveries. Exactly one of Comment and
// Message is set.
type AttemptKey struct {
	Comment, Target string
	Message         string
	Attempt         int
}

// CommentAttempt and MessageAttempt are the keys of a comment's attempt and a message's.
func CommentAttempt(comment, target string, attempt int) AttemptKey {
	return AttemptKey{Comment: comment, Target: target, Attempt: attempt}
}

func MessageAttempt(message string, attempt int) AttemptKey {
	return AttemptKey{Message: message, Attempt: attempt}
}

// ID is the address of the attempt's node: its row's primary key, spelled after the table it is
// a row of, `comment/<comment id>/<target>/<attempt>` or `message/<message id>/<attempt>`. The
// prefix keeps the two tables' keys apart, and a comment's target route (`role:<topic>`,
// `session:<id>`) holds no `/`, so no two rows spell one address.
func (k AttemptKey) ID() string {
	if k.Message != "" {
		return "message/" + k.Message + "/" + strconv.Itoa(k.Attempt)
	}
	return "comment/" + k.Comment + "/" + k.Target + "/" + strconv.Itoa(k.Attempt)
}

// AttemptWriter is who an attempt's facts are written by where the transaction that writes them
// appends no event: the delivery machinery itself. A claim, a release, a refresh and a renewal
// move the row on the server's own work and record no event, and what each records is how this
// server's sending went, which is nobody's act, as the outbox's record of what reached a session
// is its own (outbox.graphWriter); so does a Retry's or a sender's opening of an attempt. A write
// that appends an event beside the row - a comment's mentions, a targeted message, a broadcast's
// copies, a settle and its receipt, a reply, an acceptance - states the facts as that event's
// actor instead, under that event (AttemptStated, then Caused).
var AttemptWriter = model.Actor{Kind: "system", ID: "dispatch-delivery"}

// AttemptOpened writes the node of the attempt row the caller is about to insert, inside the
// caller's transaction, and answers the node columns that insert binds: (node_kind,
// node_address), which the row's foreign key names, so the node goes in first. A target route
// carries no length bound, so an address past the store's bound (AddressFits) is no node: nothing
// is written for it and both columns are nil, which leaves the row unmarked, as every other
// writer leaves a row whose address the store cannot key.
//
// The node is made by who, the actor of the write that opens the attempt, at the transaction's
// own time, the now() the row's created_at defaults to, so the row and its node carry one time.
// It goes in at the sentinel and is answered held for its cause (Uncaused), as a message's node
// is: the caller states what the row holds once the insert is done (AttemptStated) and hands the
// write to Caused under the event the opening write appends in the same transaction, or under
// NoEvent where it appends none - a Retry's claim opens an attempt and records no event.
func AttemptOpened(ctx context.Context, tx pgx.Tx, key AttemptKey, who model.Actor) (nodeKind, nodeAddress *string, opened Uncaused, err error) {
	id := key.ID()
	if !AddressFits(id) {
		return nil, nil, Uncaused{}, nil
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&at); err != nil {
		return nil, nil, Uncaused{}, fmt.Errorf("read the time of delivery attempt %s: %w", id, err)
	}
	opened, err = WriteUncaused(ctx, tx, []Node{agentNode(KindDeliveryAttempt, id, who, at, NoEvent)}, nil)
	if err != nil {
		return nil, nil, Uncaused{}, fmt.Errorf("write the node of delivery attempt %s: %w", id, err)
	}
	kind := KindDeliveryAttempt
	return &kind, &id, opened, nil
}

// attemptEnd is the far end of one of an attempt's relations.
type attemptEnd struct{ kind, id string }

// attemptRow is what one attempt row says its node holds: the node it names, the transaction's
// time, its values by tuple kind (a value the row holds none of is absent), and the far end of
// each relation it states now.
type attemptRow struct {
	id                  string
	at                  time.Time
	values              map[string]string
	owedTo, deliveredTo *attemptEnd
	answeredBy          *attemptEnd
}

// AttemptStated is what the attempts the caller has just opened hold, as facts of the write
// opened is (AttemptOpened): opened with each named row's facts added, held for the cause the
// caller hands Caused, written by who. It reads and states as Attempted does, and appends nothing.
func AttemptStated(ctx context.Context, tx pgx.Tx, opened Uncaused, who model.Actor, keys ...AttemptKey) (Uncaused, error) {
	facts, err := attemptTuples(ctx, tx, who, keys)
	if err != nil {
		return Uncaused{}, err
	}
	for _, fact := range facts {
		opened.Tuples = append(opened.Tuples, UncausedTuple{Tuple: fact})
	}
	return opened, nil
}

// Attempted states each named attempt as its row now stands, inside the transaction that has
// just written that row: its values as value tuples, `owed_to` its target while it is pending,
// `delivered_to` its session once the listener took it (model.DeliveryTaken), and `answered_by`
// its reply once one is recorded. It reads the rows the caller's own statements wrote, under the
// row locks those statements hold, and appends only what moved against what the store holds of
// each node now: a value the row changed is stated again, a value it cleared is retracted, an
// `owed_to` is retracted once the attempt leaves pending, and a `delivered_to` or `answered_by`
// the node already states is not stated twice - a queued attempt answered later appends its
// `answered_by` and no second delivery. So a write that moved nothing appends nothing.
//
// A row that names no node is passed over: an older build wrote it during a roll, or its address
// is past the store's bound (AttemptOpened), and its old reading answers it until the backfill
// takes it once. The node is not written again: its row's foreign key holds it, and appending a
// tuple locks no row, so this adds no step to its caller's lock order.
//
// It is a transition's writer: the facts are the delivery machinery's (AttemptWriter) and carry
// no cause. A claim, a release and a sweep's reading of a lapsed claim record no event at all, so
// a transition is stated at the sentinel, and each fact is appended once because this reads what
// the node already states before it appends.
func Attempted(ctx context.Context, tx pgx.Tx, keys ...AttemptKey) error {
	facts, err := attemptTuples(ctx, tx, AttemptWriter, keys)
	if err != nil {
		return err
	}
	if err := Write(ctx, tx, nil, facts); err != nil {
		return fmt.Errorf("state the delivery attempts: %w", err)
	}
	return nil
}

// attemptTuples is the tuples that bring what the store holds of each named attempt's node to
// what its row now says, written by who at the transaction's time and at the sentinel.
func attemptTuples(ctx context.Context, tx pgx.Tx, who model.Actor, keys []AttemptKey) ([]Tuple, error) {
	var comments, targets, messages []string
	var commentAttempts, messageAttempts []int32
	for _, key := range keys {
		if key.Message != "" {
			messages, messageAttempts = append(messages, key.Message), append(messageAttempts, int32(key.Attempt))
			continue
		}
		comments, targets = append(comments, key.Comment), append(targets, key.Target)
		commentAttempts = append(commentAttempts, int32(key.Attempt))
	}
	var rows []attemptRow
	if len(comments) > 0 {
		read, err := readCommentAttempts(ctx, tx, comments, targets, commentAttempts)
		if err != nil {
			return nil, err
		}
		rows = append(rows, read...)
	}
	if len(messages) > 0 {
		read, err := readMessageAttempts(ctx, tx, messages, messageAttempts)
		if err != nil {
			return nil, err
		}
		rows = append(rows, read...)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	held, err := readAttemptFacts(ctx, tx, rows)
	if err != nil {
		return nil, err
	}
	var tuples []Tuple
	stated := map[string]bool{}
	for _, row := range rows {
		// A key named twice is one attempt, stated once.
		if stated[row.id] {
			continue
		}
		stated[row.id] = true
		tuples = append(tuples, row.moved(held[row.id], who)...)
	}
	return tuples, nil
}

// readCommentAttempts reads the named comment_deliveries rows that name their node. A comment's
// attempt is owed to the route its row targets, a session or a role (model.ParseRoute).
func readCommentAttempts(ctx context.Context, tx pgx.Tx, comments, targets []string, attempts []int32) ([]attemptRow, error) {
	rows, err := tx.Query(ctx, `
		select d.node_address, now(), d.attempt, d.state, d.envelope_id, d.error, d.resolve_error,
		       d.duplicate, d.claimed_at, d.target, d.session_id, d.reply_id::text
		from unnest($1::uuid[], $2::text[], $3::int[]) as named (comment_id, target, attempt)
		join comment_deliveries d
		  on d.comment_id = named.comment_id and d.target = named.target and d.attempt = named.attempt
		where d.node_kind = $4
	`, comments, targets, attempts, KindDeliveryAttempt)
	if err != nil {
		return nil, fmt.Errorf("read the comment delivery attempts: %w", err)
	}
	return pgx.CollectRows(rows, func(scanned pgx.CollectableRow) (attemptRow, error) {
		var row attemptRow
		var number int
		var state, target string
		var envelope, failure, resolveError, session, reply *string
		var duplicate bool
		var claimed *time.Time
		if err := scanned.Scan(&row.id, &row.at, &number, &state, &envelope, &failure, &resolveError,
			&duplicate, &claimed, &target, &session, &reply); err != nil {
			return attemptRow{}, fmt.Errorf("scan a comment delivery attempt: %w", err)
		}
		row.fill(number, state, envelope, failure, duplicate, claimed, nil, nil, target, session, reply, KindComment)
		if resolveError != nil {
			row.values[AttemptResolveError] = *resolveError
		}
		return row, nil
	})
}

// readMessageAttempts reads the named message_deliveries rows that name their node. A message's
// attempt is owed to the route its message targets (messages.target), the conversation every
// attempt of it belongs to, and its row names the session it resolved, or the empty string before
// it resolved one.
func readMessageAttempts(ctx context.Context, tx pgx.Tx, messages []string, attempts []int32) ([]attemptRow, error) {
	rows, err := tx.Query(ctx, `
		select d.node_address, now(), d.attempt, d.state, d.envelope_id, d.error, d.duplicate,
		       d.claimed_at, d.accepted_at, d.accepted_as, coalesce(m.target, ''), nullif(d.session_id, ''),
		       d.reply_id::text
		from unnest($1::uuid[], $2::int[]) as named (message_id, attempt)
		join message_deliveries d on d.message_id = named.message_id and d.attempt = named.attempt
		join messages m on m.id = d.message_id
		where d.node_kind = $3
	`, messages, attempts, KindDeliveryAttempt)
	if err != nil {
		return nil, fmt.Errorf("read the message delivery attempts: %w", err)
	}
	return pgx.CollectRows(rows, func(scanned pgx.CollectableRow) (attemptRow, error) {
		var row attemptRow
		var number int
		var state, target string
		var envelope, failure, acceptedAs, session, reply *string
		var duplicate bool
		var claimed, accepted *time.Time
		if err := scanned.Scan(&row.id, &row.at, &number, &state, &envelope, &failure, &duplicate,
			&claimed, &accepted, &acceptedAs, &target, &session, &reply); err != nil {
			return attemptRow{}, fmt.Errorf("scan a message delivery attempt: %w", err)
		}
		row.fill(number, state, envelope, failure, duplicate, claimed, accepted, acceptedAs, target, session, reply, KindMessage)
		return row, nil
	})
}

// fill sets what both tables' rows hold alike. A time is stated as RFC 3339 in UTC to the
// microsecond Postgres keeps, a flag as `true` or `false`, and the attempt's number as decimal.
func (row *attemptRow) fill(
	number int, state string, envelope, failure *string, duplicate bool, claimed, accepted *time.Time,
	acceptedAs *string, target string, session, reply *string, replyKind string,
) {
	row.values = map[string]string{
		AttemptNumber:    strconv.Itoa(number),
		AttemptState:     state,
		AttemptDuplicate: strconv.FormatBool(duplicate),
	}
	set := func(kind string, value *string) {
		if value != nil {
			row.values[kind] = *value
		}
	}
	setTime := func(kind string, value *time.Time) {
		if value != nil {
			row.values[kind] = value.UTC().Format(time.RFC3339Nano)
		}
	}
	set(AttemptEnvelopeID, envelope)
	set(AttemptError, failure)
	setTime(AttemptClaimedAt, claimed)
	setTime(AttemptAcceptedAt, accepted)
	set(AttemptAcceptedAs, acceptedAs)
	if state == model.DeliveryStatePending {
		if route, err := model.ParseRoute(target); err == nil && AddressFits(route.ID) {
			row.owedTo = &attemptEnd{kind: route.Kind, id: route.ID}
		}
	}
	if model.DeliveryTaken(state) && session != nil && *session != "" && AddressFits(*session) {
		row.deliveredTo = &attemptEnd{kind: KindSession, id: *session}
	}
	if reply != nil {
		row.answeredBy = &attemptEnd{kind: replyKind, id: *reply}
	}
}

// attemptFacts is what the store holds of one attempt node now: the newest value tuple of each
// value kind, asserting or not, and the far ends each relation kind currently asserts.
type attemptFacts struct {
	values    map[string]heldValue
	relations map[string][]attemptEnd
}

type heldValue struct {
	value    string
	asserted bool
}

// readAttemptFacts reads, in one statement, what the store holds of every node rows names: the
// newest tuple of each value kind, which says whether the node carries that value now and which,
// and each relation the node currently asserts (CurrentTupleRead), both bounded to the kinds an
// attempt states. The two halves read graph_tuples_values and graph_tuples_current, each led by
// the node's own address.
func readAttemptFacts(ctx context.Context, tx pgx.Tx, rows []attemptRow) (map[string]attemptFacts, error) {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	read, err := tx.Query(ctx, `
		select newest.from_id, newest.kind, newest.to_kind, newest.to_id, newest.asserted
		from (
			`+CurrentTupleRead(`t.from_id, t.kind`, `t.from_id, t.kind, t.to_kind, t.to_id, t.asserted`,
		`t.from_kind = $1 and t.from_id = any($2) and t.to_kind = '`+KindValue+`' and t.kind = any($3)`)+`
		) newest
		union all
		select current.from_id, current.kind, current.to_kind, current.to_id, current.asserted
		from (
			`+CurrentTupleRead(`t.from_id, t.kind, t.to_kind, t.to_id`, `t.from_id, t.kind, t.to_kind, t.to_id, t.asserted`,
		`t.from_kind = $1 and t.from_id = any($2) and t.to_kind <> '`+KindValue+`' and t.kind = any($4)`)+`
		) current
		where current.asserted
	`, KindDeliveryAttempt, ids, attemptValueKinds, attemptRelationKinds)
	if err != nil {
		return nil, fmt.Errorf("read what the store holds of the delivery attempts: %w", err)
	}
	defer read.Close()
	held := map[string]attemptFacts{}
	for read.Next() {
		var id, kind, toKind, toID string
		var asserted bool
		if err := read.Scan(&id, &kind, &toKind, &toID, &asserted); err != nil {
			return nil, fmt.Errorf("scan what the store holds of a delivery attempt: %w", err)
		}
		facts, ok := held[id]
		if !ok {
			facts = attemptFacts{values: map[string]heldValue{}, relations: map[string][]attemptEnd{}}
		}
		if toKind == KindValue {
			facts.values[kind] = heldValue{value: toID, asserted: asserted}
		} else {
			facts.relations[kind] = append(facts.relations[kind], attemptEnd{kind: toKind, id: toID})
		}
		held[id] = facts
	}
	if err := read.Err(); err != nil {
		return nil, fmt.Errorf("read what the store holds of the delivery attempts: %w", err)
	}
	return held, nil
}

// moved is the tuples that bring what the store holds of the node to what its row says.
func (row attemptRow) moved(held attemptFacts, who model.Actor) []Tuple {
	var tuples []Tuple
	fact := func(kind, toKind, toID string, asserted bool) {
		tuples = append(tuples, Tuple{
			FromKind: KindDeliveryAttempt, FromID: row.id, Kind: kind, ToKind: toKind, ToID: toID,
			At: row.at, Who: who, SourceEvent: NoEvent, Asserted: asserted,
		})
	}
	for _, kind := range attemptValueKinds {
		value, carried := row.values[kind]
		current := held.values[kind]
		switch {
		case carried && (!current.asserted || current.value != value):
			fact(kind, KindValue, value, true)
		case !carried && current.asserted:
			fact(kind, KindValue, current.value, false)
		}
	}
	// owed_to stands for the target while the attempt is pending and for nothing after, so every
	// other end the node asserts is retracted.
	for _, end := range held.relations[TupleOwedTo] {
		if row.owedTo == nil || end != *row.owedTo {
			fact(TupleOwedTo, end.kind, end.id, false)
		}
	}
	if row.owedTo != nil && !slices.Contains(held.relations[TupleOwedTo], *row.owedTo) {
		fact(TupleOwedTo, row.owedTo.kind, row.owedTo.id, true)
	}
	// A delivery that happened and a reply that was drawn stay stated: a later settle of the row
	// takes neither back.
	if row.deliveredTo != nil && !slices.Contains(held.relations[TupleDeliveredTo], *row.deliveredTo) {
		fact(TupleDeliveredTo, row.deliveredTo.kind, row.deliveredTo.id, true)
	}
	if row.answeredBy != nil && !slices.Contains(held.relations[TupleAnsweredBy], *row.answeredBy) {
		fact(TupleAnsweredBy, row.answeredBy.kind, row.answeredBy.id, true)
	}
	return tuples
}
