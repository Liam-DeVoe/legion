// Package inboxwrite is the one writer of the threads a person takes part in and of what reaches
// them there (dispatch://INFRA-1152 D3, R2, R4 and R9). Every write path that stores a comment, a
// message, an ask or an answer calls Record after its event, in that write's own transaction, and
// Record appends the participation and delivery tuples of the write through graphstore and writes
// the user_threads projection of them. The triggers of migrations 0095-0097, which wrote that
// projection before, pass a write this release marked by (migration 20261116160001), and keep
// answering a write an older build makes during a roll until the backfill takes it once.
//
// It sits below api and docs, which both write asks and answers, and the seed calls it too, since
// the seed sets the markers the triggers pass by.
package inboxwrite

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/failpoint"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// Arm is what a write does in the thread it is written in.
type Arm int

const (
	// Replies is a write the thread's other participants are told of: a comment, a message, a
	// broadcast copy, a session's reply, and an answer, whose node is the ask it answers. Its
	// writer takes part, every other participant is delivered it, and each of them who holds a
	// row of the thread sees the row move.
	Replies Arm = iota + 1
	// Opens is an ask's create: the asker takes part in the thread the ask opens and keeps a row
	// of it, read through the ask, and the person the ask waits on is reached by it and takes part
	// in it (the assignee arm, waitingOnTuples); nobody else is told of it.
	Opens
	// TakesPart is a write that takes part and delivers nothing: a decline, which closes an ask
	// with no answer and moves no row of anybody's.
	TakesPart
)

// Write is one write as Record records it: its own node, which is also the node of the thread
// it opens, who wrote it, its moment and the owner it belongs to, which names the project its
// thread's rows are kept under.
type Write struct {
	Arm Arm
	// NodeKind and NodeID are the write's own node: a comment, a message, or the ask a create
	// opens and an answer or a decline closes.
	NodeKind, NodeID string
	// Author is whose write it is, as graphstore.AuthorOf reads it: a person or a session takes
	// part, and an actor that names nobody takes no part and keeps no row.
	Author model.Actor
	// Who is the actor the tuples record as their writer.
	Who model.Actor
	// At is the write's own moment: the row's created_at, or the answer's at.
	At time.Time
	// IssueKey and ArtifactID are the owner the write belongs to, which a user_threads row names,
	// and Project the project that owner resolves to (graphstore.ScopeOf), "" for an owner that
	// resolves to none: a thread such as an agent conversation's file, or a direct message, keeps
	// no row, while its participation and its deliveries are tuples all the same.
	IssueKey, ArtifactID *string
	Project              string
	// Starter is, for a session's reply under a direct conversation, the person who started it:
	// who wrote its root, a message on no issue a person sent to a session (R2's
	// direct-conversation arm, which user_threads_message never reached, returning on a message
	// on no issue). The reply reaches them whether or not their participates_in stands. "" for
	// any other write.
	Starter string
	// ask is the answered ask as its answer writer read it before the update that writes the
	// answer (ReadAsk), whose catch-up Record appends first.
	ask *Ask
	// waiting is, for an ask's create, the person the ask waits on as its writer read it
	// (WaitingOn), and nil where Record reads it itself (waitingOn).
	waiting *string
}

// Comment is a comment's write, a reply in the thread its head pointers name.
func Comment(id string, author model.Actor, at time.Time, issueKey, artifactID *string, project string) Write {
	return Write{Arm: Replies, NodeKind: graphstore.KindComment, NodeID: id, Author: author, Who: author, At: at,
		IssueKey: issueKey, ArtifactID: artifactID, Project: project}
}

// Message is a message's write - an issue message, a direct message, a broadcast copy or a
// session's reply - a reply in the thread its replies_to chain names.
func Message(id string, author model.Actor, at time.Time, issueKey *string, project string) Write {
	return Write{Arm: Replies, NodeKind: graphstore.KindMessage, NodeID: id, Author: author, Who: author, At: at,
		IssueKey: issueKey, Project: project}
}

// Opened is an ask's create by author at the ask's created_at.
func Opened(askID string, author model.Actor, at time.Time, issueKey, artifactID *string, project string) Write {
	return Write{Arm: Opens, NodeKind: graphstore.KindAsk, NodeID: askID, Author: author, Who: author, At: at,
		IssueKey: issueKey, ArtifactID: artifactID, Project: project}
}

// WaitingOn is w, an ask's create, with waiting, the person the ask waits on as its writer read
// them after the ask's insert, through asks.AskedOfPersonExpression and under the locks waitingOn
// reads under - the owner row and, where the ask falls through to its project's default, the
// project's row (HoldDefault) - so nothing it read has moved since: Record's assignee arm then
// delivers the ask to that person without reading them a second time. A create that names
// nobody this way leaves the read to Record.
func (w Write) WaitingOn(waiting string) Write {
	w.waiting = &waiting
	return w
}

// Answered is the write of answer to ask, which its writer read before its update (ReadAsk), made
// by who: the person the answer names answered, at its own moment.
func Answered(ask Ask, answer model.AskAnswer, who model.Actor) Write {
	return Write{Arm: Replies, NodeKind: graphstore.KindAsk, NodeID: ask.id,
		Author: model.Actor{Kind: "user", ID: answer.User}, Who: who, At: answer.At,
		IssueKey: ask.issueKey, ArtifactID: ask.artifactID, Project: ask.project, ask: &ask}
}

// Declined is a decline of askID by who at at.
func Declined(askID string, who model.Actor, at time.Time) Write {
	return Write{Arm: TakesPart, NodeKind: graphstore.KindAsk, NodeID: askID, Author: who, Who: who, At: at}
}

// ThreadLogins is the thread_logins an event of this write names, given logins, the logins with
// a row on its thread before Record runs (events.ThreadLogins): those, and the writer's own where
// the write keeps them a row. Record runs after the event, so the event names the row its writer
// is about to hold, as it named the row the trigger wrote before the event when the triggers kept
// the rows. The result is sorted and never nil.
func (w Write) ThreadLogins(logins []string) []string {
	if logins == nil {
		logins = []string{}
	}
	login, keeps := w.keptLogin()
	if !keeps || slices.Contains(logins, login) {
		return logins
	}
	logins = append(slices.Clone(logins), login)
	slices.Sort(logins)
	return logins
}

// keptLogin is the login whose row the write keeps: its writer's, where its writer is a person the
// store can key (graphstore.AddressFits) and its owner resolves to a project, which is what a row
// names.
func (w Write) keptLogin() (string, bool) {
	if w.Arm == TakesPart || w.Project == "" {
		return "", false
	}
	kind, id, named := graphstore.AuthorOf(w.Author)
	return id, named && kind == graphstore.KindPerson && graphstore.AddressFits(id)
}

// Record records w, whose event is event (graphstore.NoEvent for a write no event records, the
// seed's), in tx, the write's own transaction, after that event and after every fact the write
// states of its own node, its thread pointers among them. For a write by A into thread T, the
// thread its head pointer tuples name (threadOf), it takes these steps, in this order:
//
//  0. an answer first appends every tuple of its ask the marker test still owes (decision 16,
//     owedBy): the participation and the citations of the ask's create where the ask's marker
//     is null, and the participation of an older build's answer the ask holds;
//  1. participates_in from A to T, whether A is a person or a session and whether or not T's
//     owner resolves to a project; and, for an ask's create, the assignee arm: participates_in
//     from the person the ask waits on - as the create read them (Write.WaitingOn), else read
//     here (waitingOn) - to it and delivered_to from it to them (waitingOnTuples), which raises
//     no version (P13);
//  2. for a reply, every user_threads row of T, `for no key update` in ascending login order, in
//     one pass (lockThread) - which takes the rows of the catch-up's persons with them, since
//     each is a row of T - and the live participant set, read in the same statement: the current
//     participates_in tuples into T, and the login of each row of T the marker test passes. Such
//     a row is the old arm's record that its person took part through an older build, during a
//     roll and before the backfill gives them their tuple, and reading it is the marker test
//     passing; no column of a marked row is read to decide anything;
//  3. for a reply, through graphstore.ToPerson, one delivered_to from the write's node to each
//     person in that set but A, so a person never draws their own write (outbox's
//     recipientFilter.keep), beside every delivered_to the catch-up owes and, for a session's
//     reply under a direct conversation, the one to the person who started it (w.Starter);
//  4. for a reply, the move of each row of T a person of that set but A holds: replied_at,
//     last_reply_id, version + 1 and dismissed_at cleared, and, for each person of that set but A
//     whose dismissed tuple into T stands, the retraction of that tuple (graphstore.FromPerson);
//  5. A's own row, where A is a person and T's owner resolves to a project: its read mark moves to
//     the write where it stood earlier, and a row this write begins records marked_version 0 and
//     marked_seq at the log position of this moment (migration 20261116160000); then, where the
//     upsert moved the mark, through graphstore.FromPerson, A's read_through to T at the moment
//     that row's read mark stands at once the upsert ran, as every writer of a read mark states it
//     (dispatch://INFRA-1152 R6).
//
// A session's reply to a message takes, between steps 1 and 2, its thread root's Agent
// dismissals: the root `for key share`, then the delete of every user_agent_thread_dismissed row
// naming it, with the retraction of each one's dismissed tuple, as the trigger
// user_agent_thread_dismissed_clear does for an older build's reply (migration 20261105093507).
//
// Steps 2 to 4 hold the lock rule of D4: the rows are locked before the delivered_to of the people
// holding them is appended, and the backfill marks a row under the same lock, so no delivery falls
// on both sides of a mark. The owner row every such write holds first (ownerRowLock) orders two
// writes into one thread, and the lock order written on api/inbox_read.go's markInboxReadThreads
// states this step.
func Record(ctx context.Context, tx pgx.Tx, w Write, event int64) error {
	thread, err := threadOf(ctx, tx, w)
	if err != nil {
		return err
	}
	authorKind, authorID, named := graphstore.AuthorOf(w.Author)
	named = named && graphstore.AddressFits(authorID)
	// person is A where A is a person, and "" for a session or nobody.
	person := ""
	if named && authorKind == graphstore.KindPerson {
		person = authorID
	}

	// 0. What an answer owes of an ask an older build wrote: its participation and its
	// citations now, its deliveries with step 3's.
	owed, err := owedBy(ctx, tx, w.ask)
	if err != nil {
		return err
	}
	if err := graphstore.ToPerson(ctx, tx, owed.people); err != nil {
		return fmt.Errorf("record what ask %s owes of its create and its older answer: %w", w.NodeID, err)
	}
	if len(owed.others) > 0 {
		if err := graphstore.Write(ctx, tx, nil, owed.others); err != nil {
			return fmt.Errorf("record what ask %s owes of its create: %w", w.NodeID, err)
		}
	}

	// 1. A takes part in T. An ask's create also reaches the person the ask waits on, who takes
	// part in it too, in the same call.
	var people []graphstore.Tuple
	if named {
		taking := graphstore.Tuple{
			FromKind: authorKind, FromID: authorID, Kind: graphstore.TupleParticipatesIn,
			ToKind: thread.kind, ToID: thread.id,
			At: w.At, Who: w.Who, SourceEvent: event, Asserted: true,
		}
		if person != "" {
			people = append(people, taking)
		} else if err := graphstore.Write(ctx, tx, nil, []graphstore.Tuple{taking}); err != nil {
			return fmt.Errorf("record that %s/%s takes part in %s/%s: %w", authorKind, authorID, thread.kind, thread.id, err)
		}
	}
	if w.Arm == Opens {
		waiting := ""
		if w.waiting != nil {
			waiting = *w.waiting
		} else if waiting, err = waitingOn(ctx, tx, w.NodeID); err != nil {
			return err
		}
		people = append(people, waitingOnTuples(w.NodeID, waiting, person, w.At, w.Who, event)...)
	}
	if err := graphstore.ToPerson(ctx, tx, people); err != nil {
		return fmt.Errorf("record who takes part in %s/%s by %s/%s: %w", thread.kind, thread.id, w.NodeKind, w.NodeID, err)
	}
	if w.Arm == TakesPart {
		return nil
	}

	if w.Arm == Replies {
		// A session's reply to a message brings its root back to everyone who dismissed it.
		if w.NodeKind == graphstore.KindMessage && authorKind == graphstore.KindSession && thread.id != w.NodeID {
			cleared, err := clearAgentDismissals(ctx, tx, thread.id)
			if err != nil {
				return err
			}
			if err := failpoint.Between(ctx, failpoint.AgentDismissalClear); err != nil {
				return err
			}
			if len(cleared) > 0 {
				if err := graphstore.FromPerson(ctx, tx, w.At, dismissalsEnded(cleared, thread, w, event)); err != nil {
					return fmt.Errorf("retract the dismissals of message %s: %w", thread.id, err)
				}
			}
		}

		// 2. Every row of T, locked in one pass, and who takes part in T.
		members, err := lockThread(ctx, tx, thread)
		if err != nil {
			return err
		}

		// 3. The write reaches every other participant: each whose participates_in stands, and
		// each who holds a row of T the marker test passes; and a session's reply reaches the
		// person who started its direct conversation, whether or not they still take part.
		deliveries := owed.deliveries
		var moved, dismissed []string
		starterDelivered := false
		delivered := func(login string) {
			deliveries = append(deliveries, graphstore.Tuple{
				FromKind: w.NodeKind, FromID: w.NodeID, Kind: graphstore.TupleDeliveredTo,
				ToKind: graphstore.KindPerson, ToID: login,
				At: w.At, Who: w.Who, SourceEvent: event, Asserted: true,
			})
		}
		starter := model.CanonicalLogin(w.Starter)
		for _, member := range members {
			if member.login == person || !(member.takesPart || (member.row && !member.marked)) {
				continue
			}
			delivered(member.login)
			starterDelivered = starterDelivered || model.CanonicalLogin(member.login) == starter
			if member.row {
				moved = append(moved, member.login)
			}
			if member.dismissed {
				dismissed = append(dismissed, member.login)
			}
		}
		if starter != "" && starter != person && !starterDelivered {
			delivered(starter)
		}
		if err := graphstore.ToPerson(ctx, tx, deliveries); err != nil {
			return fmt.Errorf("deliver %s/%s to the participants of %s/%s: %w", w.NodeKind, w.NodeID, thread.kind, thread.id, err)
		}
		if err := failpoint.Between(ctx, failpoint.UserThreads); err != nil {
			return err
		}

		// 4. Each row of T those people hold moves, and each of their dismissals of T ends.
		if len(dismissed) > 0 {
			if err := graphstore.FromPerson(ctx, tx, w.At, dismissalsEnded(dismissed, thread, w, event)); err != nil {
				return fmt.Errorf("retract the dismissals of %s/%s: %w", thread.kind, thread.id, err)
			}
		}
		if err := moveRows(ctx, tx, thread, w, moved); err != nil {
			return err
		}
	}

	// 5. A's own row, and the read mark it stands at where this write moved it.
	if login, keeps := w.keptLogin(); keeps {
		readThrough, moved, err := keepRow(ctx, tx, thread, w, login)
		if err != nil {
			return err
		}
		if !moved {
			return nil
		}
		if err := graphstore.FromPerson(ctx, tx, w.At, []graphstore.Tuple{{
			FromKind: graphstore.KindPerson, FromID: login, Kind: graphstore.TupleReadThrough,
			ToKind: thread.kind, ToID: thread.id,
			At: readThrough, Who: w.Who, SourceEvent: event, Asserted: true,
		}}); err != nil {
			return fmt.Errorf("record %s's read mark on %s/%s: %w", login, thread.kind, thread.id, err)
		}
	}
	return nil
}

// dismissalsEnded is the retraction of the dismissed tuple from each person of logins to thread's
// root, made by w's writer at w's moment and under its event.
func dismissalsEnded(logins []string, thread thread, w Write, event int64) []graphstore.Tuple {
	ended := make([]graphstore.Tuple, 0, len(logins))
	for _, login := range logins {
		ended = append(ended, graphstore.Tuple{
			FromKind: graphstore.KindPerson, FromID: model.CanonicalLogin(login), Kind: graphstore.TupleDismissed,
			ToKind: thread.kind, ToID: thread.id,
			At: w.At, Who: w.Who, SourceEvent: event, Asserted: false,
		})
	}
	return ended
}
