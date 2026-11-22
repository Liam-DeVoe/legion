package seedcorpus

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// rolesWriter is who the loader records as the writer of the roles' facts: the loader itself,
// which acts for nobody, as the outbox records its own (outbox's graphWriter).
var rolesWriter = model.Actor{Kind: "system", ID: "dispatch-corpus"}

// writeAgents writes the agents and the identities: the agent's session, the role it holds and
// the role's page (bullet 16), the issue message to that agent whose delivery attempt failed
// (c-agent-page), dana's two writes in two letter cases and the role whose holder changed (bullet
// 24), which holds an issue of its own. Each session is a node from its own comment, and each role
// is addressed by the issue routed to it; the roles' nodes, pages, holders and their holders'
// part_of are the store's facts no route writes, which writeRoles states (see there why the
// outbox, their writer in a deployment, is not run).
func (s *seeder) writeAgents(ctx context.Context, key string) error {
	var handover model.Issue
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues", map[string]any{
		"project": s.project, "title": handoverTitle, "force": true,
		"spec": "The issue the handover role holds, whose holder changes once.\n",
	}, &handover, http.StatusCreated); err != nil {
		return fmt.Errorf("create the handover issue: %w", err)
	}
	s.corpus.set("handover.issue", handover.Key)
	// A session is a node of the store from the first row that names it: its own comment.
	for _, written := range []struct{ session, issue, body string }{
		{agentSession, key, "The agent picked the seeded issue up."},
		{handoverFirst, handover.Key, "The first holder of the handover role took the issue."},
		{handoverSecond, handover.Key, "The second holder of the handover role stands by."},
	} {
		if _, err := s.comment(ctx, asSession(written.session), written.issue, written.body, ""); err != nil {
			return fmt.Errorf("comment as %s: %w", written.session, err)
		}
	}
	for _, route := range []struct{ issue, role string }{{key, agentRole}, {handover.Key, handoverRole}} {
		if err := s.api.call(ctx, asPerson(alice), http.MethodPatch, "/api/v1/issues/"+route.issue,
			map[string]any{"route": "role:" + route.role}, nil); err != nil {
			return fmt.Errorf("route %s to %s: %w", route.issue, route.role, err)
		}
	}
	s.corpus.set("agent.session", agentSession)
	s.corpus.set("agent.role", agentRole)

	// The message posted on the seeded issue to the agent, whose one attempt fails: no Envoy
	// listener answers this process, so the API records the attempt failed.
	var posted created
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues/"+key+"/messages", map[string]any{
		"body": "Draw the decisions view from the tree read.", "target": "session:" + agentSession, "delivery": "steer",
	}, &posted); err != nil {
		return fmt.Errorf("post the message to the agent: %w", err)
	}
	s.corpus.set("agent.failed_message", posted.ID)

	// One login in two letter cases: dana writes once as she signs in and once as "Dana".
	for _, login := range []string{danaCased, dana} {
		if _, err := s.comment(ctx, asPerson(login), key, "Dana's note, written as "+login+".", ""); err != nil {
			return fmt.Errorf("comment as %s: %w", login, err)
		}
	}

	// Each role held by the session the bullet names; then the handover role passes to its second
	// holder, its first holder's claim retracted, as a later resolution of a role's holder states it.
	at := time.Now().UTC()
	var nodes []graphstore.Node
	var tuples []graphstore.Tuple
	for _, held := range []struct{ role, session string }{{agentRole, agentSession}, {handoverRole, handoverFirst}} {
		var claims []graphstore.Claim
		nodes, claims = graphstore.RoleFacts(nodes, claims, held.role, rolesWriter, at, graphstore.NoEvent)
		for _, claim := range claims {
			tuples = append(tuples, roleTuple(claim.FromKind, claim.FromID, claim.Kind, claim.ToKind, claim.ToID, true, at))
		}
		tuples = append(tuples,
			roleTuple(graphstore.KindRole, held.role, graphstore.TupleHeldBy, graphstore.KindSession, held.session, true, at),
			roleTuple(graphstore.KindSession, held.session, graphstore.TuplePartOf, graphstore.KindRole, held.role, true, at))
	}
	if err := s.writeRoles(ctx, nodes, tuples); err != nil {
		return err
	}
	later := at.Add(time.Second)
	if err := s.writeRoles(ctx, nil, []graphstore.Tuple{
		roleTuple(graphstore.KindRole, handoverRole, graphstore.TupleHeldBy, graphstore.KindSession, handoverFirst, false, later),
		roleTuple(graphstore.KindRole, handoverRole, graphstore.TupleHeldBy, graphstore.KindSession, handoverSecond, true, later),
		roleTuple(graphstore.KindSession, handoverSecond, graphstore.TuplePartOf, graphstore.KindRole, handoverRole, true, later),
	}); err != nil {
		return err
	}
	s.corpus.set("handover.role", handoverRole)
	s.corpus.set("handover.first", handoverFirst)
	s.corpus.set("handover.second", handoverSecond)
	return nil
}

// roleTuple is one fact of a role the loader states, by rolesWriter at at and caused by no event.
func roleTuple(fromKind, fromID, kind, toKind, toID string, asserted bool, at time.Time) graphstore.Tuple {
	return graphstore.Tuple{FromKind: fromKind, FromID: fromID, Kind: kind, ToKind: toKind, ToID: toID,
		At: at, Who: rolesWriter, SourceEvent: graphstore.NoEvent, Asserted: asserted}
}

// writeRoles writes nodes and appends tuples in one transaction of its own, through the store's
// writer (graphstore.Write): the facts of the roles the loader holds, which the outbox's record of
// a batch routed to a role states in a deployment (outbox's deliveredGraph) - each role's node, its
// page and the page's part_of it (graphstore.RoleFacts, the one statement of these facts), the
// role's held_by its holder and the holder's part_of the role, and a held_by retracted where the
// holder changed. It sends nothing and marks no event published: the outbox states these facts
// only as it publishes the events routed to the role, and the loader writes into a database a
// server may be serving, whose events, the loader's own and every other writer's, are that
// server's to publish, so the loader states the facts alone and leaves every event as it found it.
func (s *seeder) writeRoles(ctx context.Context, nodes []graphstore.Node, tuples []graphstore.Tuple) error {
	return pgx.BeginFunc(ctx, s.database.Pool, func(tx pgx.Tx) error {
		if err := graphstore.Write(ctx, tx, nodes, tuples); err != nil {
			return fmt.Errorf("write the roles' facts: %w", err)
		}
		return nil
	})
}
