package api

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/asof"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The ask routes, the inbox and open asks at a cut (task t7 of INFRA-4620's plan). Each runs its
// present statements under the cut, which the store binds to the shadows. What the shadows do not
// give of an ask, each answer leaves out: the fields of leaveOutPastAskFields, and the reach of an
// ask's route, which the route roster gives now and which keeps no past state (loadAskReaches
// reads no roster at a cut). A request at no cut leaves out nothing.

// leaveOutRouteReach names, for reach the reach of an ask with a route, the route's status and
// its holder at place (the ask read's own place, or a thread's): the roster that gives both now
// keeps no past state. A reach with no route holds neither at any time, so nothing is left out.
// The route's status reason stays null: it gives the reason for an unknown status, and the answer
// gives no status.
func leaveOutRouteReach(ctx context.Context, reach askReach, place string) {
	if reach.Route != nil {
		leaveOut(ctx, place+"/route_status", place+"/route_holder")
	}
}

// issueAsksAtTheCut names what an issue's ask list at a cut leaves out: of each row, the fields of
// leaveOutPastAskFields, and of a row's thread (?thread=1), the reach of the ask's route. asks are
// the rows' asks, in the rows' order.
func (s *server) issueAsksAtTheCut(ctx context.Context, asks []model.Ask, rows []issueAskRow) error {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return nil
	}
	at := elementsOf("")
	if err := s.leaveOutPastAskFields(ctx, asks, at, true); err != nil {
		return err
	}
	for index := range rows {
		if rows[index].Thread != nil {
			leaveOutRouteReach(ctx, rows[index].Thread.askReach, at(index)+"/thread")
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
// leaveOutPastAskFields of the ask, and the reach of its route.
func (s *server) askAtTheCut(ctx context.Context, read askRead) error {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return nil
	}
	if err := s.leaveOutPastAskFields(ctx, []model.Ask{read.Ask.Ask}, func(int) string { return "/ask" }, true); err != nil {
		return err
	}
	leaveOutRouteReach(ctx, read.askReach, "")
	return nil
}

// inboxAtTheCut names what the inbox at a cut leaves out of each of rows (decision 10): the
// caller's snooze, which keeps no history, the fields of leaveOutPastAskFields but an approval's
// documents, which the inbox's list does not hold, and the reach of the ask's route in its thread.
func (s *server) inboxAtTheCut(ctx context.Context, rows []inboxAsk) error {
	if _, atCut := asof.CutFrom(ctx); !atCut {
		return nil
	}
	asks := make([]model.Ask, len(rows))
	at := elementsOf("")
	for index := range rows {
		asks[index] = rows[index].Ask
		leaveOut(ctx, at(index)+"/snoozed_until")
		leaveOutRouteReach(ctx, rows[index].Thread.askReach, at(index)+"/thread")
	}
	return s.leaveOutPastAskFields(ctx, asks, at, false)
}
