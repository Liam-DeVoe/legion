package api

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/asof"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// Comments and messages at a cut (task t7 of INFRA-4620's plan). The comment and message routes
// run their present statements under the cut, and the shadows give each post as it stood then:
// its body at the cut, its present delete mark (decision 14), and each delivery attempt with the
// state that its receipt or reply event at the cut gives. These are the parts of a post that the
// records at the cut do not give. The comment and message routes leave them out (leaveOut).

// undatedCommentAttempt and undatedMessageAttempt are the members of a delivery attempt that no
// receipt or reply event at the cut dates, which a past-time answer leaves out (reason E): the
// state, the members that a send writes with the state, and the delivery mode and the session
// that a send resolves with no event. The shadows give such an attempt the empty state and the
// defaults of those members (commentDeliveriesShadow, messageDeliveriesShadow in asof). An attempt
// that no receipt settled holds no error and is no duplicate, so the answer gives those two.
var (
	undatedCommentAttempt = []string{"state", "reply_id", "envelope_id", "resolve_error", "created_at", "delivery", "session_id"}
	undatedMessageAttempt = []string{"state", "reply_id", "envelope_id", "created_at", "requested_by", "delivery", "session_id"}
)

// elementsOf names element index of the array at prefix in an answer: "" for an answer that is a
// list, /replies for a thread's replies.
func elementsOf(prefix string) func(index int) string {
	return func(index int) string { return prefix + "/" + strconv.Itoa(index) }
}

// deletedSinceTheCut reports whether a post whose present delete mark is deletedAt was deleted
// after cut. Such a post reads at the cut as deleted, with no part of its body (decision 14). A
// post deleted at or before the cut was deleted at the cut, and its empty body is its body then.
func deletedSinceTheCut(deletedAt *time.Time, cut asof.Cut) bool {
	return deletedAt != nil && deletedAt.After(cut.Time())
}

// leaveOutUndatedAttempts names, of each of a post's attempts whose state (state) is empty, which
// is an attempt that no event at the cut dates, the members of it at place/deliveries/<n>.
func leaveOutUndatedAttempts[Attempt any](ctx context.Context, place string, attempts []Attempt, state func(Attempt) string, members []string) {
	for index, attempt := range attempts {
		if state(attempt) != "" {
			continue
		}
		at := place + "/deliveries/" + strconv.Itoa(index) + "/"
		named := make([]string, len(members))
		for member := range members {
			named[member] = at + members[member]
		}
		leaveOut(ctx, named...)
	}
}

// commentsAtTheCut names what a past-time answer leaves out of each of comments, the comment at
// the place that at(index) names in the answer:
//   - body, of a comment that its author deleted after the cut (deletedSinceTheCut), and of a
//     comment whose body at the cut no record holds (reason T,
//     asof.CommentBodiesNotGivenStatement). The shadow gives each of them the empty body;
//   - reply_block.removed, which says whether the block that the reply answers is in its item
//     now. No event records it (reason E);
//   - the members of each delivery attempt that no event at the cut dates (undatedCommentAttempt).
//
// A request at no cut leaves out nothing.
func (s *server) commentsAtTheCut(ctx context.Context, comments []model.Comment, at func(index int) string) error {
	cut, atCut := asof.CutFrom(ctx)
	if !atCut {
		return nil
	}
	var emptied []string
	for index, comment := range comments {
		place := at(index)
		switch {
		case deletedSinceTheCut(comment.DeletedAt, cut):
			leaveOut(ctx, place+"/body")
		case comment.DeletedAt == nil && comment.Body == "":
			// An empty body at the cut is the body then (a suggestion's comment can hold none),
			// or a body that no record holds. Only such a comment is read again.
			emptied = append(emptied, comment.ID)
		}
		if comment.ReplyBlock != nil {
			leaveOut(ctx, place+"/reply_block/removed")
		}
		leaveOutUndatedAttempts(ctx, place, comment.Deliveries, func(attempt model.CommentDelivery) string { return attempt.State }, undatedCommentAttempt)
	}
	if len(emptied) == 0 {
		return nil
	}
	rows, err := s.deps.Store.Pool.Query(ctx, asof.CommentBodiesNotGivenStatement, emptied, cut.Time())
	if err != nil {
		return fmt.Errorf("read which comments have no body at the cut: %w", err)
	}
	unknown := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		unknown[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read which comments have no body at the cut: %w", err)
	}
	for index, comment := range comments {
		if unknown[comment.ID] {
			leaveOut(ctx, at(index)+"/body")
		}
	}
	return nil
}

// messagesAtTheCut names what a past-time answer leaves out of each of reads, the thread at the
// place that at(index) names in the answer ("" for an answer that is one thread): of its root
// (/message) and of each reply (/replies/<n>), the body of a message that its author deleted
// after the cut (deletedSinceTheCut), and the members of each delivery attempt that no event at
// the cut dates (undatedMessageAttempt). The records give the body of every other message at the
// cut (asof.CommentBodiesNotGivenStatement says why). A request at no cut leaves out nothing.
func messagesAtTheCut(ctx context.Context, reads []messageRead, at func(index int) string) {
	cut, atCut := asof.CutFrom(ctx)
	if !atCut {
		return
	}
	for index, read := range reads {
		thread := at(index)
		messageAtTheCut(ctx, cut, thread+"/message", read.Message.Message)
		for reply, message := range read.Replies {
			messageAtTheCut(ctx, cut, thread+"/replies/"+strconv.Itoa(reply), message.Message)
		}
	}
}

// messageAtTheCut names what a past-time answer leaves out of message, at place.
func messageAtTheCut(ctx context.Context, cut asof.Cut, place string, message model.Message) {
	if deletedSinceTheCut(message.DeletedAt, cut) {
		leaveOut(ctx, place+"/body")
	}
	leaveOutUndatedAttempts(ctx, place, message.Deliveries, func(attempt model.MessageDelivery) string { return attempt.State }, undatedMessageAttempt)
}
