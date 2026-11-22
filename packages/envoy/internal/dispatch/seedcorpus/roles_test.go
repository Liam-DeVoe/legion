package seedcorpus

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/outbox"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/roles"
	"github.com/sjawhar/envoy/internal/session"
	envoystore "github.com/sjawhar/envoy/internal/store"
)

// TestTheLoaderStatesTheRolesAsTheOutboxDoes holds the loader's own statement of the roles
// (writeRoles, from writeAgents) to the statement it stands in for: the outbox's record of a batch
// routed to a role (outbox's deliveredGraph, written by recordBatch as Run publishes the batch).
// The loader runs no outbox, since the database it seeds is a server's to publish, so nothing else
// would notice a loader that stated a role's facts in a shape no deployment writes.
//
// Each side writes the same people and agents on a database of its own, through the API as the
// corpus writes them: the agent's session and the two holders of the handover role as nodes from
// their own comments, the seeded issue routed to the agent's role, and the handover issue routed
// to the handover role. On the loader's side writeAgents then states the roles. On the outbox's
// side the real outbox publishes every event through a stream that acknowledges each send, with
// each role claimed by the holder the corpus names; then the handover role passes to its second
// holder, and one more event routed to that role is published, so the outbox resolves the new
// holder as a later batch of a deployment does.
//
// The two must state the same facts of the two roles: each role's node and its page, with their
// supertag, visibility, state and attributes, and every held_by and part_of of a role, of its page
// and of a session into a role, each relation read as the order of its assertions and
// retractions. What only a writer or a moment says - each row's time, its writer, the event that
// caused it and its place in the log - is left out, since the loader writes as itself, at its own
// time and under no event, by design.
func TestTheLoaderStatesTheRolesAsTheOutboxDoes(t *testing.T) {
	ctx := context.Background()
	people := []string{alice, bob, dana}

	loaderSide := storetest.Open(t)
	loader := startSeeder(t, loaderSide, people, "ROLES")
	seeded := createRoutedIssue(t, loader, "The issue the agent's role is routed")
	if err := loader.writeAgents(ctx, seeded); err != nil {
		t.Fatalf("the loader's writeAgents: %v", err)
	}
	closeSeeder(t, loader)

	outboxSide := storetest.Open(t)
	claims := &heldRoles{holder: map[string]string{agentRole: agentSession, handoverRole: handoverFirst}}
	stop := runOutbox(t, outboxSide, claims)
	publisher := startSeeder(t, outboxSide, people, "ROLES")
	routed := createRoutedIssue(t, publisher, "The issue the agent's role is routed")
	handover := createRoutedIssue(t, publisher, handoverTitle)
	for _, written := range []struct{ session, issue, body string }{
		{agentSession, routed, "The agent picked the seeded issue up."},
		{handoverFirst, handover, "The first holder of the handover role took the issue."},
		{handoverSecond, handover, "The second holder of the handover role stands by."},
	} {
		if _, err := publisher.comment(ctx, asSession(written.session), written.issue, written.body, ""); err != nil {
			t.Fatalf("comment as %s: %v", written.session, err)
		}
	}
	routeTo(t, publisher, routed, agentRole)
	routeTo(t, publisher, handover, handoverRole)
	allPublished(t, outboxSide)
	claims.pass(handoverRole, handoverSecond)
	if _, err := publisher.comment(ctx, asPerson(alice), handover, "The handover role has a new holder.", ""); err != nil {
		t.Fatalf("comment on the handover issue after the handover: %v", err)
	}
	allPublished(t, outboxSide)
	closeSeeder(t, publisher)
	stop()

	want := roleFactsOf(t, outboxSide)
	got := roleFactsOf(t, loaderSide)
	t.Logf("the outbox stated %d nodes and %d relations of the two roles; the loader %d and %d",
		len(want.nodes), len(want.relations), len(got.nodes), len(got.relations))
	if len(want.nodes) == 0 || len(want.relations) == 0 {
		t.Fatalf("the outbox stated %d nodes and %d relations of the two roles, so the comparison would compare nothing",
			len(want.nodes), len(want.relations))
	}
	if differences := want.differences(got); len(differences) > 0 {
		t.Fatalf("the loader states the roles otherwise than the outbox does:\n%s", strings.Join(differences, "\n"))
	}
}

// startSeeder is the loader's in-process API over database, with people recorded as signed in and
// project made, as Seed starts it.
func startSeeder(t *testing.T, database *store.Store, people []string, project string) *seeder {
	t.Helper()
	server, err := startAPI(context.Background(), database, people)
	if err != nil {
		t.Fatalf("start the in-process API: %v", err)
	}
	s := &seeder{api: server, database: database, project: project}
	if err := s.ensureProject(context.Background()); err != nil {
		t.Fatalf("create project %s: %v", project, err)
	}
	return s
}

// closeSeeder settles what the seeder's API wrote and shuts it down, as Seed does.
func closeSeeder(t *testing.T, s *seeder) {
	t.Helper()
	if err := s.api.close(context.Background()); err != nil {
		t.Fatalf("close the in-process API: %v", err)
	}
}

// createRoutedIssue creates an issue as alice, as writeModelIssue and writeAgents create theirs,
// and answers its key.
func createRoutedIssue(t *testing.T, s *seeder, title string) string {
	t.Helper()
	var issue model.Issue
	if err := s.api.call(context.Background(), asPerson(alice), http.MethodPost, "/api/v1/issues", map[string]any{
		"project": s.project, "title": title, "force": true, "spec": "An issue a role holds.\n",
	}, &issue, http.StatusCreated); err != nil {
		t.Fatalf("create the issue %q: %v", title, err)
	}
	return issue.Key
}

// routeTo routes issue key to role as alice, as writeAgents does.
func routeTo(t *testing.T, s *seeder, key, role string) {
	t.Helper()
	if err := s.api.call(context.Background(), asPerson(alice), http.MethodPatch, "/api/v1/issues/"+key,
		map[string]any{"route": "role:" + role}, nil); err != nil {
		t.Fatalf("route %s to %s: %v", key, role, err)
	}
}

// heldRoles is the role claim store and the session reader of the test's outbox, standing in for
// Envoy's role and session buckets: each role is claimed by the session holder names, which pass
// moves, and every session is live and subscribed itself, so a role route is forwarded to it. It
// judges a session on the windows roles.Buckets judges one on (roles.Windows).
type heldRoles struct {
	roles.Windows
	mu     sync.Mutex
	holder map[string]string
}

func (h *heldRoles) RoleClaim(_ context.Context, role string) (envoystore.RoleClaim, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return envoystore.RoleClaim{HolderSessionID: h.holder[role]}, nil
}

func (h *heldRoles) Holder(context.Context, string) (session.SessionEntry, error) {
	return session.SessionEntry{SelfSubscribed: true, UpdatedAt: time.Now().UnixMilli()}, nil
}

func (h *heldRoles) LastSeen(string) int64 { return 0 }

// pass hands role to session, as a new claim of the role does.
func (h *heldRoles) pass(role, session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.holder[role] = session
}

// acceptingStream is the outbox's NATS client as a stream that acknowledges every send at once,
// and the window of each batch is that same stream: what the test reads is what the outbox
// records once its sends are answered.
type acceptingStream struct{}

func (acceptingStream) WindowUntil(func() time.Time) bus.Sender { return acceptingStream{} }

func (acceptingStream) Connected() bool { return true }

func (acceptingStream) ConnectionChanged() <-chan struct{} { return nil }

func (acceptingStream) PublishAsync(contracts.Envelope) <-chan error { return accepted() }

func (acceptingStream) PublishStoredAsync(string, contracts.Envelope, string) <-chan error {
	return accepted()
}

func (acceptingStream) Request(string, contracts.Envelope, bus.RequestOptions) <-chan error {
	return accepted()
}

func (acceptingStream) Probe(time.Duration) error { return nil }

func accepted() <-chan error {
	answer := make(chan error, 1)
	answer <- nil
	return answer
}

// runOutbox runs the real outbox over database until the answered stop is called, woken by the
// commits it hears on its own LISTEN connection, as cmd/dispatch runs it.
func runOutbox(t *testing.T, database *store.Store, claims *heldRoles) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		outbox.New(outbox.Deps{
			Store: database, Publisher: acceptingStream{}, Broker: events.NewBroker(),
			Listen: database.Pool.Listener, RoleClaims: claims, Sessions: claims,
		}).Run(ctx)
		close(done)
	}()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("the outbox did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

// allPublished waits until the outbox has published every event the database holds. The record
// that marks an event published is the statement that writes what the event states in the store
// (the outbox's recordBatch), so the store holds all of it then.
func allPublished(t *testing.T, database *store.Store) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var unpublished int
		if err := database.Pool.QueryRow(context.Background(),
			`select count(*) from events where published_at is null`).Scan(&unpublished); err != nil {
			t.Fatalf("count the events the outbox has not published: %v", err)
		}
		if unpublished == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the outbox left %d event(s) unpublished", unpublished)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// roleFacts is what one database states of the two roles: each node by its kind and id, read as
// its supertag, visibility, state and attributes; and each relation, by its two ends and its kind,
// read as the order of its assertions and retractions.
type roleFacts struct {
	nodes     map[string]string
	relations map[string]string
}

// roleFactsOf reads the facts of agentRole and handoverRole from database: the two roles' nodes
// and their pages' nodes, and every held_by and part_of whose near end is one of those nodes or
// whose near end is a session and whose far end is one of the roles.
func roleFactsOf(t *testing.T, database *store.Store) roleFacts {
	t.Helper()
	ctx := context.Background()
	held := []string{agentRole, handoverRole}
	pages := []string{graphstore.RolePageID(agentRole), graphstore.RolePageID(handoverRole)}
	facts := roleFacts{nodes: map[string]string{}, relations: map[string]string{}}
	rows, err := database.Pool.Query(ctx, `
		select kind, id, supertag, visibility, state, attributes::text from graph_nodes
		where (kind = $1 and id = any($3)) or (kind = $2 and id = any($4))
		order by kind, id`, graphstore.KindRole, graphstore.KindPage, held, pages)
	if err != nil {
		t.Fatalf("read the roles' nodes: %v", err)
	}
	for rows.Next() {
		var kind, id, supertag, visibility, state, attributes string
		if err := rows.Scan(&kind, &id, &supertag, &visibility, &state, &attributes); err != nil {
			t.Fatalf("read a role's node: %v", err)
		}
		facts.nodes[kind+" "+id] = fmt.Sprintf("supertag=%s visibility=%s state=%s attributes=%s", supertag, visibility, state, attributes)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the roles' nodes: %v", err)
	}
	rows.Close()
	rows, err = database.Pool.Query(ctx, `
		select from_kind, from_id, kind, to_kind, to_id, asserted from graph_tuples
		where kind in ($1, $2)
		  and ((from_kind = $3 and from_id = any($6))
		    or (from_kind = $4 and from_id = any($7))
		    or (from_kind = $5 and to_kind = $3 and to_id = any($6)))
		order by seq`,
		graphstore.TupleHeldBy, graphstore.TuplePartOf, graphstore.KindRole, graphstore.KindPage, graphstore.KindSession, held, pages)
	if err != nil {
		t.Fatalf("read the roles' relations: %v", err)
	}
	defer rows.Close()
	history := map[string][]string{}
	for rows.Next() {
		var fromKind, fromID, kind, toKind, toID string
		var asserted bool
		if err := rows.Scan(&fromKind, &fromID, &kind, &toKind, &toID, &asserted); err != nil {
			t.Fatalf("read a role's relation: %v", err)
		}
		relation := fmt.Sprintf("%s %s -%s-> %s %s", fromKind, fromID, kind, toKind, toID)
		history[relation] = append(history[relation], map[bool]string{true: "asserted", false: "retracted"}[asserted])
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the roles' relations: %v", err)
	}
	for relation, steps := range history {
		facts.relations[relation] = strings.Join(steps, ", then ")
	}
	return facts
}

// differences names each fact the loader states otherwise than the outbox does, want being the
// outbox's: a node or a relation one of them states and the other does not, and one both state
// with a different reading.
func (want roleFacts) differences(got roleFacts) []string {
	var differences []string
	compare := func(what string, want, got map[string]string) {
		names := make([]string, 0, len(want)+len(got))
		for name := range want {
			names = append(names, name)
		}
		for name := range got {
			if _, held := want[name]; !held {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			wanted, outboxHolds := want[name]
			written, loaderHolds := got[name]
			switch {
			case !loaderHolds:
				differences = append(differences, fmt.Sprintf("%s %s: the outbox states %s, the loader states none", what, name, wanted))
			case !outboxHolds:
				differences = append(differences, fmt.Sprintf("%s %s: the loader states %s, the outbox states none", what, name, written))
			case wanted != written:
				differences = append(differences, fmt.Sprintf("%s %s: the outbox states %s, the loader %s", what, name, wanted, written))
			}
		}
	}
	compare("node", want.nodes, got.nodes)
	compare("relation", want.relations, got.relations)
	return differences
}
