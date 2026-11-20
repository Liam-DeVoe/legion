package api

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/asof"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The ask routes, the inbox and open asks at a cut (task t7 of INFRA-4620's plan). Each runs its
// present statements under the cut, which the store binds to the shadows. What the shadows do not
// give of an ask, each answer leaves out: the fields of leaveOutPastAskFields, the reach of an
// ask's route, which the route roster gives now and which keeps no past state (loadAskReaches
// reads no roster at a cut), and of each reply of an ask's thread what commentsAtTheCut names. A
// request at no cut leaves out nothing.

// leaveOutRouteReach names, for reach the reach of an ask with a route, the route's status, its
// holder and the reason for its status at place (the ask read's own place, or a thread's): the
// roster that gives them now keeps no past state. A reach with no route holds none of them at any
// time, so nothing is left out.
func leaveOutRouteReach(ctx context.Context, reach askReach, place string) {
	if reach.Route != nil {
		leaveOut(ctx, place+"/route_status", place+"/route_holder", place+"/route_status_reason")
	}
}

// threadAtTheCut names what a past-time answer leaves out of thread at place: the reach of the
// ask's route, and of each reply what commentsAtTheCut names.
func (s *server) threadAtTheCut(ctx context.Context, thread askThread, place string) error {
	leaveOutRouteReach(ctx, thread.askReach, place)
	replies := make([]model.Comment, len(thread.Replies))
	for index := range thread.Replies {
		replies[index] = thread.Replies[index].Comment
	}
	return s.commentsAtTheCut(ctx, replies, elementsOf(place+"/replies"))
}

// issueAsksAtTheCut names what an issue's ask list at a cut leaves out: of each row, the fields of
// leaveOutPastAskFields, and of a row's thread (?thread=1) what threadAtTheCut names. asks are the
// rows' asks, in the rows' order.
func (s *server) issueAsksAtTheCut(ctx context.Context, asks []model.Ask, rows []issueAskRow) error {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return nil
	}
	at := elementsOf("")
	if err := s.leaveOutPastAskFields(ctx, asks, at); err != nil {
		return err
	}
	for index := range rows {
		if rows[index].Thread != nil {
			if err := s.threadAtTheCut(ctx, *rows[index].Thread, at(index)+"/thread"); err != nil {
				return err
			}
		}
	}
	return nil
}

// openAsksAtTheCut names what open asks at a cut leave out of each of asks: the person an ask
// waits on, where it rests on a project default that the records at the cut do not give (reason
// D). The answer's as_of and each ask's age count from the cut in the read itself (listOpenAsks).
func openAsksAtTheCut(ctx context.Context, asks []openAsk) {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return
	}
	at := elementsOf("/asks")
	for index := range asks {
		if asks[index].WaitingOnPerson == "" {
			leaveOut(ctx, at(index)+"/waiting_on_person")
		}
	}
}

// askAtTheCut names what the read of one ask at a cut leaves out: the fields of
// leaveOutPastAskFields of the ask, and of its thread what threadAtTheCut names. The place of
// the ask's anchored block is left out where the read skips it (anchorAtTheCut).
func (s *server) askAtTheCut(ctx context.Context, read askRead) error {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return nil
	}
	if err := s.leaveOutPastAskFields(ctx, []model.Ask{read.Ask.Ask}, func(int) string { return "/ask" }); err != nil {
		return err
	}
	return s.threadAtTheCut(ctx, read.askThread, "")
}

// inboxAtTheCut names what the inbox at a cut leaves out of each of rows (decision 10): the
// caller's snooze, which keeps no history, the fields of leaveOutPastAskFields, and of the row's
// thread what threadAtTheCut names.
func (s *server) inboxAtTheCut(ctx context.Context, rows []inboxAsk) error {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return nil
	}
	asks := make([]model.Ask, len(rows))
	at := elementsOf("")
	for index := range rows {
		asks[index] = rows[index].Ask
		leaveOut(ctx, at(index)+"/snoozed_until")
		if err := s.threadAtTheCut(ctx, rows[index].Thread, at(index)+"/thread"); err != nil {
			return err
		}
	}
	return s.leaveOutPastAskFields(ctx, asks, at)
}
