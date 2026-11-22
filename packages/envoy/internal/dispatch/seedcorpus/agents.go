package seedcorpus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/outbox"
	"github.com/sjawhar/envoy/internal/session"
	envoystore "github.com/sjawhar/envoy/internal/store"
)

// writeAgents writes the agents and the identities: the agent's session, the role it holds and
// the role's page (bullet 16), the issue message to that agent whose delivery attempt failed
// (c-agent-page), dana's two writes in two letter cases and the role whose holder changed (bullet
// 24), which holds an issue of its own. The roles' nodes, their pages, their held_by and each
// holder's part_of are the outbox's to write: its record of a batch of events routed to a role
// states them (outbox's deliveredGraph), so the loader routes an issue to each role and publishes
// the issue's events through the outbox, run in this process with the claim store answering the
// holder the bullet names.
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

	holders := &roleHolders{held: map[string]string{agentRole: agentSession, handoverRole: handoverFirst}}
	if err := s.publish(ctx, holders, key, handover.Key); err != nil {
		return err
	}
	// The handover: the role's next event is published with its second holder holding it.
	holders.set(handoverRole, handoverSecond)
	if _, err := s.comment(ctx, asPerson(alice), handover.Key, "The handover role changed hands.", ""); err != nil {
		return err
	}
	if err := s.publish(ctx, holders, handover.Key); err != nil {
		return err
	}
	s.corpus.set("handover.role", handoverRole)
	s.corpus.set("handover.first", handoverFirst)
	s.corpus.set("handover.second", handoverSecond)
	return nil
}

// publishBound is how long one publish waits for the outbox to publish the issues' events.
const publishBound = 30 * time.Second

// publish runs the outbox in this process until every event of the issues is published, its
// publisher accepting every send, and its claim store and session lookup answering from holders.
// The outbox is the one writer of a role's node, its page, its held_by and its holder's part_of
// (outbox's recordBatch), so these facts are written as a deployment writes them. The e2e server
// runs with no bus (DISPATCH_NATS_DISABLED), so no other outbox publishes the corpus's events.
func (s *seeder) publish(ctx context.Context, holders *roleHolders, issues ...string) error {
	running, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		outbox.New(outbox.Deps{
			Store: s.database, Publisher: acceptingPublisher{}, Broker: events.NewBroker(),
			Listen: s.database.Pool.Listener, RoleClaims: holders, Sessions: holders,
		}).Run(running)
	}()
	defer func() {
		stop()
		<-stopped
	}()
	deadline := time.Now().Add(publishBound)
	for {
		var unpublished int
		if err := s.database.Pool.QueryRow(ctx, `
			select count(*) from events where issue_key = any($1) and published_at is null
		`, issues).Scan(&unpublished); err != nil {
			return fmt.Errorf("count the unpublished events of %v: %w", issues, err)
		}
		if unpublished == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the outbox left %d events of %v unpublished after %s", unpublished, issues, publishBound)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(settlePoll):
		}
	}
}

// roleHolders is the claim store and the session lookup of the outbox the loader runs: each role
// of the corpus is claimed by the session held names, which is live, and a role the corpus does not
// hold is answered as no read answers it, so the outbox states nothing of who holds it.
type roleHolders struct {
	mu   sync.Mutex
	held map[string]string
}

// errNotTheCorpusRole is the answer for a role the corpus holds no holder of.
var errNotTheCorpusRole = errors.New("the corpus loader answers only the corpus's own roles")

func (h *roleHolders) set(role, session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held[role] = session
}

func (h *roleHolders) RoleClaim(_ context.Context, role string) (envoystore.RoleClaim, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	holder, held := h.held[role]
	if !held {
		return envoystore.RoleClaim{}, errNotTheCorpusRole
	}
	return envoystore.RoleClaim{HolderSessionID: holder}, nil
}

func (h *roleHolders) Holder(context.Context, string) (session.SessionEntry, error) {
	return session.SessionEntry{SelfSubscribed: true, UpdatedAt: time.Now().UnixMilli()}, nil
}

func (h *roleHolders) EntryState(entry session.SessionEntry) session.State {
	return session.StateOf(entry, time.Now(), session.LiveWindow, session.GoneHorizon)
}

func (h *roleHolders) LastSeen(string) int64 { return time.Now().UnixMilli() }

// acceptingPublisher is the bus of the outbox the loader runs: every send is taken at once. No
// session the corpus names runs, so there is nobody for a send to reach.
type acceptingPublisher struct{}

func (acceptingPublisher) Connected() bool { return true }

func (acceptingPublisher) ConnectionChanged() <-chan struct{} { return nil }

func (p acceptingPublisher) WindowUntil(func() time.Time) bus.Sender { return p }

func (acceptingPublisher) PublishAsync(contracts.Envelope) <-chan error { return accepted() }

func (acceptingPublisher) PublishStoredAsync(string, contracts.Envelope, string) <-chan error {
	return accepted()
}

func (acceptingPublisher) Request(string, contracts.Envelope, bus.RequestOptions) <-chan error {
	return accepted()
}

func (acceptingPublisher) Probe(time.Duration) error { return nil }

func accepted() <-chan error {
	answer := make(chan error, 1)
	answer <- nil
	return answer
}
