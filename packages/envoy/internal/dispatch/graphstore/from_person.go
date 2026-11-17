package graphstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The read state of a person (dispatch://INFRA-1152 R6): what they have read, cleared, dismissed
// or snoozed is a tuple from their node and never a node, its far end is what the mark is about,
// and the tuple's own moment is the mark it stands for. TupleReadThrough runs to the root of a
// thread, or to a session, the person has read through that moment; TupleDismissed to the root of
// a thread, or the root message of an Agent thread, they took out of their Inbox at that moment;
// TupleCleared to a session whose conversation they cleared before that moment; TupleReplyRead to
// a reply they read by its id; and TupleSnoozed to an ask they will deal with at that moment, its
// snoozed_until (decision 13). A mark that ends - a restore, an unsnooze, a dismissal a later reply
// takes back, a reply-read row the session's read mark passed - is a retraction of its tuple, so
// its history reads back. Each of the five from a person is appended by FromPerson and by nothing
// else (ErrMarkOutsideFromPerson).
const (
	TupleReadThrough = "read_through"
	TupleDismissed   = "dismissed"
	TupleCleared     = "cleared"
	TupleReplyRead   = "reply_read"
	TupleSnoozed     = "snoozed"
)

// markKinds is the five, held once for the checks below.
var markKinds = [...]string{TupleReadThrough, TupleDismissed, TupleCleared, TupleReplyRead, TupleSnoozed}

// ErrMarkOutsideFromPerson refuses a read-state tuple from a person handed to any other writer of
// this store: FromPerson is the one place that keys such a tuple on the person's node, so a writer
// that went around it could state one person's marks under two spellings of their login.
var ErrMarkOutsideFromPerson = errors.New("a read_through, dismissed, cleared, reply_read or snoozed tuple from a person is appended by FromPerson alone")

// FromPerson appends the read state one write of a person's own states: tuples from the person
// (FromKind KindPerson) under the five kinds above, each to the node the mark is about, asserted
// for a mark the write made and retracted for one it ended, every one under the write's event.
//
// The person of each tuple is PersonNode's canonical login (model.CanonicalLogin), so every
// spelling a route's actor or a projection row holds of one person - the raw actor id
// user_agent_state keys its Clear on, the canonical login the other read-state tables key on -
// meets at one node (decision 9). Each person's node is written beside the tuples, `do nothing`
// where the store holds it already, made by the tuple's writer under its event at made, the moment
// of the write: a mark's own moment can lie ahead of the write that states it (a snooze's
// snoozed_until), and a node is made when a write first records it.
//
// A tuple whose ends lie past the store's address bound is no tuple, as ToPerson has it, and the
// rest of the call is appended; a writer that marks its projection row asks AddressFits of the
// same ends first, so such a row stays its old arm's.
//
// It runs in the caller's transaction, in one statement: the person nodes, in ascending (kind,
// id) as every write of this store takes them (WriteArms), then the tuples, which take the
// relation repeat key, so one event states each mark once.
func FromPerson(ctx context.Context, tx pgx.Tx, made time.Time, tuples []Tuple) error {
	if len(tuples) == 0 {
		return nil
	}
	stated := make([]Tuple, 0, len(tuples))
	var persons []Node
	for _, tuple := range tuples {
		if tuple.FromKind != KindPerson || !isMark(tuple.Kind) || tuple.ToKind == KindValue {
			return fmt.Errorf("FromPerson appends %s from a person to a node, not %s %s/%s -> %s/%s",
				strings.Join(markKinds[:], ", "), tuple.Kind, tuple.FromKind, tuple.FromID, tuple.ToKind, tuple.ToID)
		}
		tuple.FromID = model.CanonicalLogin(tuple.FromID)
		if tuple.FromID == "" {
			return fmt.Errorf("%s -> %s/%s names no person", tuple.Kind, tuple.ToKind, tuple.ToID)
		}
		if !AddressFits(tuple.FromID) || !AddressFits(tuple.ToID) {
			continue
		}
		stated = append(stated, tuple)
		if !slices.ContainsFunc(persons, func(person Node) bool { return person.ID == tuple.FromID }) {
			persons = append(persons, PersonNode(tuple.FromID, tuple.Who, made, tuple.SourceEvent))
		}
	}
	if len(stated) == 0 {
		return nil
	}
	arms, params, err := writeArms(1, persons, stated, nil, true)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "with "+arms+"\nselect 1", params...); err != nil {
		return fmt.Errorf("append %d mark from a person: %w", len(stated), err)
	}
	return nil
}

// isMark is whether kind is one of the five read-state kinds FromPerson appends.
func isMark(kind string) bool { return slices.Contains(markKinds[:], kind) }

// marksFromPerson is whether tuple is a mark from a person, which FromPerson alone appends.
func marksFromPerson(tuple Tuple) bool {
	return tuple.FromKind == KindPerson && isMark(tuple.Kind)
}
