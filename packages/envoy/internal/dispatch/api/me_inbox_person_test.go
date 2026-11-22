package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
)

// personInboxTrace records the statements a counted read sends (meInboxStatements), each batch as
// the statements it carries rather than as their count, so a read that answers its arms from
// several batches prints every statement it sent.
type personInboxTrace struct{ *meInboxStatements }

func (s personInboxTrace) TraceBatchStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	statements := make([]string, 0, len(data.Batch.QueuedQueries))
	for _, queued := range data.Batch.QueuedQueries {
		statements = append(statements, strings.Join(strings.Fields(queued.SQL), " "))
	}
	s.record(ctx, conn, fmt.Sprintf("a batch of %d statements: %s", len(statements), strings.Join(statements, "; ")))
	return ctx
}

// personInboxRig is a server whose store is traced (storetest.TracedStore) for the statements of
// the one inbox read: a read marked countedRead is the one whose statements the trace records, and
// the caller's identity is resolved on the untraced pool, so a sign-in lookup is no statement of
// the read.
type personInboxRig struct {
	database *store.Store
	traced   *store.Store
	handler  http.Handler
	trace    personInboxTrace
}

func newPersonInboxRig(t *testing.T) *personInboxRig {
	t.Helper()
	database := storetest.Open(t)
	trace := personInboxTrace{&meInboxStatements{}}
	traced := storetest.TracedStore(t, database, trace)
	handler, _, _ := newTestServer(t, testServerOptions{
		store:    traced,
		identity: headerIdentity(database),
		// A document's settlement is database work of its own on the traced pool; a delay no
		// test reaches keeps it out of the read's statements.
		settle: time.Hour,
	})
	return &personInboxRig{database: database, traced: traced, handler: handler, trace: trace}
}

// read is one counted read of login's inbox under query: the answer, and every statement the
// traced pool sent for it.
func (rig *personInboxRig) read(t *testing.T, login string, query url.Values) (meInboxResponse, []string) {
	t.Helper()
	rig.trace.ownStatements(t, rig.traced)
	marked := context.WithValue(context.Background(), countedRead{}, true)
	answer := decodeMeInbox(t, meInboxRequestAs(t, rig.handler, marked, login, query))
	return answer, rig.trace.ownStatements(t, rig.traced)
}

// personInboxNode is one node of the corpus and the name the test prints it by.
type personInboxNode struct {
	kind, id, name string
}

func (node personInboxNode) key() string { return node.kind + ":" + node.id }

func (node personInboxNode) String() string { return node.name + " (" + node.key() + ")" }

// personInboxArm is one arm of the one read: the relation kind, the end of it the person's node
// holds, and the nodes the fixture states the arm answers.
type personInboxArm struct {
	name, kind, direction string
	nodes                 []personInboxNode
}

// personInboxCorpus is the person and the four expected sets the fixture states, one for each arm,
// which share no node, so a defect in one arm cannot hide behind another; relations is every
// relation each node's entry carries, as kind/direction in the order the read answers them.
type personInboxCorpus struct {
	person, session, role, closed string
	arms                          []personInboxArm
	relations                     map[string][]string
	names                         map[string]string
}

// closedIssueBy files an issue as login and closes it through the route a person uses. Another
// person files it, so the issue and its document tie the corpus's person to nothing.
func closedIssueBy(t *testing.T, handler http.Handler, login, project, title string) string {
	t.Helper()
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": project, "name": project}, login); response.Code != http.StatusCreated {
		t.Fatalf("create project %s: status=%d body=%s", project, response.Code, response.Body.String())
	}
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{"project": project, "title": title}, login)
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue: status=%d body=%s", created.Code, created.Body.String())
	}
	key := decodeBody[model.Issue](t, created).Key
	if closed := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+key, map[string]string{"status": "done"}, login); closed.Code != http.StatusOK || !strings.Contains(closed.Body.String(), `"status":"done"`) {
		t.Fatalf("close %s: status=%d body=%s", key, closed.Code, closed.Body.String())
	}
	return key
}

// seedPersonInboxCorpus writes the corpus of the person's four arms straight into the store,
// through the store's own writers, so the fixture states each set rather than the code under test
// computing it:
//
//   - delivery: a message, a broadcast copy, a comment, an ask and a direct reply, each delivered
//     to the person; the broadcast copy to the session the person's credential proved theirs,
//     which is how a broadcast reaches a person;
//   - authorship: the direct message the reply answers, and the person's comment in a thread she
//     took part in, written from that session, which is what proves the session hers; nothing
//     is delivered to either;
//   - participation: that thread, and an ask she answered, both on a closed issue, with no new
//     delivery on either;
//   - mention: a comment that mentions a role, which resolved to her session, the role forward
//     that reached her; its delivery ends at the role.
func seedPersonInboxCorpus(t *testing.T, handler http.Handler, database *store.Store) personInboxCorpus {
	t.Helper()
	const project = "ARMS"
	corpus := personInboxCorpus{
		person: "alice", session: "s-" + uuid.NewString(), role: "arms-role-" + uuid.NewString()[:8],
		relations: map[string][]string{}, names: map[string]string{},
	}
	corpus.closed = closedIssueBy(t, handler, "bob", project, "The issue the person took part in, closed since")
	carol, bob := model.Actor{Kind: "user", ID: "carol"}, model.Actor{Kind: "user", ID: "bob"}
	agent := model.Actor{Kind: "session", ID: "s-" + uuid.NewString()}
	base := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	at := func(minute int) time.Time { return base.Add(time.Duration(minute) * time.Minute) }
	node := func(kind, name string) personInboxNode {
		return personInboxNode{kind: kind, id: uuid.NewString(), name: name}
	}
	message := node(graphstore.KindMessage, "the message")
	broadcast := node(graphstore.KindMessage, "the broadcast")
	comment := node(graphstore.KindComment, "the comment")
	ask := node(graphstore.KindAsk, "the ask")
	reply := node(graphstore.KindMessage, "the reply")
	conversation := node(graphstore.KindMessage, "the direct message she wrote, which the reply answers")
	written := node(graphstore.KindComment, "her comment in the thread she took part in")
	thread := node(graphstore.KindComment, "the thread on the closed issue she wrote in")
	answered := node(graphstore.KindAsk, "the ask on the closed issue she answered")
	mention := node(graphstore.KindComment, "the mention that reached her through a role forward")

	seed := meInboxSeed{t: t, database: database}
	nodes := []graphstore.Node{}
	for index, made := range []struct {
		node    personInboxNode
		author  model.Actor
		project string
	}{
		{message, carol, ""}, {broadcast, carol, ""}, {comment, carol, ""}, {ask, carol, ""}, {reply, agent, ""},
		{conversation, model.Actor{Kind: "user", ID: corpus.person}, ""},
		{thread, bob, project}, {written, model.Actor{Kind: "user", ID: corpus.person}, project}, {answered, bob, project},
		{mention, carol, ""},
	} {
		nodes = append(nodes, graphstore.Node{
			Kind: made.node.kind, ID: made.node.id, Supertag: made.node.kind, Visibility: graphstore.VisibilityProject,
			State: graphstore.StateLive, Project: made.project, MadeAt: at(index), MadeBy: made.author, SourceEvent: graphstore.NoEvent,
		})
	}
	seed.write(func(ctx context.Context, tx pgx.Tx) error { return graphstore.Write(ctx, tx, nodes, nil) })

	// Authorship, through the store's writer of who wrote a thing. Her comment is written from the
	// session her credential proved, so the writer states the session's alias_of to her beside it.
	authored := func(node personInboxNode, actor model.Actor, minute int) {
		seed.write(func(ctx context.Context, tx pgx.Tx) error {
			return store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
				return graphstore.QueueAuthored(b, node.kind, node.id, actor, at(minute), graphstore.NoEvent)
			})
		})
	}
	authored(conversation, model.Actor{Kind: "user", ID: corpus.person}, 20)
	authored(written, model.Actor{Kind: "user", ID: corpus.person, Act: &model.ActorAct{Session: corpus.session}}, 21)

	// What ties each node to its place and to the others, none of it a relation to the person.
	seed.tuples(
		meInboxTuple(reply.kind, reply.id, graphstore.TupleRepliesTo, conversation.kind, conversation.id, at(22)),
		meInboxTuple(reply.kind, reply.id, graphstore.TuplePartOf, conversation.kind, conversation.id, at(22)),
		meInboxTuple(written.kind, written.id, graphstore.TupleRepliesTo, thread.kind, thread.id, at(21)),
		meInboxTuple(thread.kind, thread.id, graphstore.TuplePartOf, graphstore.KindIssue, corpus.closed, at(6)),
		meInboxTuple(answered.kind, answered.id, graphstore.TuplePartOf, graphstore.KindIssue, corpus.closed, at(8)),
		meInboxTuple(broadcast.kind, broadcast.id, graphstore.TupleBroadcastID, graphstore.KindValue, uuid.NewString(), at(1)),
	)

	// Delivery: four to the person's own node through the one writer of a delivery to a person,
	// and the broadcast copy to her proved session, which names no person.
	seed.toPerson(
		meInboxTuple(message.kind, message.id, meInboxDelivered, graphstore.KindPerson, corpus.person, at(30)),
		meInboxTuple(comment.kind, comment.id, meInboxDelivered, graphstore.KindPerson, corpus.person, at(31)),
		meInboxTuple(ask.kind, ask.id, meInboxDelivered, graphstore.KindPerson, corpus.person, at(32)),
		meInboxTuple(reply.kind, reply.id, meInboxDelivered, graphstore.KindPerson, corpus.person, at(33)),
	)
	seed.tuples(meInboxTuple(broadcast.kind, broadcast.id, meInboxDelivered, graphstore.KindSession, corpus.session, at(34)))

	// Participation: the thread she wrote in, and the ask she answered, with the answer's own
	// relation to the person who gave it. The issue closed after both, and nothing reached her on
	// either since.
	seed.toPerson(
		meInboxTuple(graphstore.KindPerson, corpus.person, meInboxParticipates, thread.kind, thread.id, at(21)),
		meInboxTuple(graphstore.KindPerson, corpus.person, meInboxParticipates, answered.kind, answered.id, at(35)),
	)
	seed.tuples(meInboxTuple(answered.kind, answered.id, graphstore.TupleAnsweredBy, graphstore.KindPerson, corpus.person, at(35)))

	// Mention: the comment mentions the role, and the role resolved to her session, which states a
	// second mentions tuple to that session; its delivery is to the role, which is no node of hers.
	seed.tuples(
		meInboxTuple(mention.kind, mention.id, meInboxNamed, graphstore.KindRole, corpus.role, at(36)),
		meInboxTuple(mention.kind, mention.id, meInboxNamed, graphstore.KindSession, corpus.session, at(36)),
		meInboxTuple(mention.kind, mention.id, meInboxDelivered, graphstore.KindRole, corpus.role, at(36)),
	)

	corpus.arms = []personInboxArm{
		{"delivery", meInboxDelivered, meInboxInbound, []personInboxNode{message, broadcast, comment, ask, reply}},
		{"authorship", meInboxAuthored, meInboxInbound, []personInboxNode{conversation, written}},
		{"participation", meInboxParticipates, meInboxOutbound, []personInboxNode{thread, answered}},
		{"mention", meInboxNamed, meInboxInbound, []personInboxNode{mention}},
	}
	for _, arm := range corpus.arms {
		for _, node := range arm.nodes {
			corpus.relations[node.key()] = []string{arm.kind + "/" + arm.direction}
			corpus.names[node.key()] = node.name
		}
	}
	corpus.relations[answered.key()] = []string{graphstore.TupleAnsweredBy + "/" + meInboxInbound, meInboxParticipates + "/" + meInboxOutbound}
	return corpus
}

// name is the corpus's name for the entry keyed kind:id, or the key alone for a node it does not
// hold.
func (corpus personInboxCorpus) name(key string) string {
	if name, held := corpus.names[key]; held {
		return name + " (" + key + ")"
	}
	return key
}

// personInboxArmOf is every node of answer holding a relation of kind at direction, as kind:id.
func personInboxArmOf(answer meInboxResponse, kind, direction string) []string {
	var nodes []string
	for _, entry := range answer.Entries {
		for _, relation := range entry.Relations {
			if relation.Kind == kind && relation.Direction == direction {
				nodes = append(nodes, entry.Node.Kind+":"+entry.Node.ID)
				break
			}
		}
	}
	return nodes
}

// requireArms holds answer to the corpus: each arm against its own set, every node the fixture
// states answered by the relation its arm names and by no relation the fixture does not state, and
// the inbox as the union of the four, each node once. Each failure names the arm, its relation
// kind and the node.
func (corpus personInboxCorpus) requireArms(t *testing.T, what string, answer meInboxResponse) {
	t.Helper()
	if answer.Person != corpus.person {
		t.Errorf("%s answers for %q, want the seeded person %q", what, answer.Person, corpus.person)
	}
	union := map[string]bool{}
	for _, arm := range corpus.arms {
		got := personInboxArmOf(answer, arm.kind, arm.direction)
		for _, node := range arm.nodes {
			union[node.key()] = true
			if !slices.Contains(got, node.key()) {
				t.Errorf("%s does not answer %s by its %s relation (%s): the %s arm is missing it", what, node, arm.kind, arm.direction, arm.name)
			}
		}
		for _, key := range got {
			if !slices.ContainsFunc(arm.nodes, func(node personInboxNode) bool { return node.key() == key }) {
				t.Errorf("%s answers %s in the %s arm (%s/%s), which the fixture's %s set does not hold", what, corpus.name(key), arm.name, arm.kind, arm.direction, arm.name)
			}
		}
	}
	held := map[string]int{}
	for _, entry := range answer.Entries {
		key := entry.Node.Kind + ":" + entry.Node.ID
		held[key]++
		if !union[key] {
			t.Errorf("%s answers %s carrying %v, which is in none of the four arms' sets", what, key, meInboxKinds(entry))
			continue
		}
		if kinds := meInboxKinds(entry); !slices.Equal(kinds, corpus.relations[key]) {
			t.Errorf("%s answers %s carrying %v, want %v", what, corpus.name(key), kinds, corpus.relations[key])
		}
	}
	for key, count := range held {
		if count > 1 {
			t.Errorf("%s answers %s as %d entries, want one", what, corpus.name(key), count)
		}
	}
	if answer.Total != len(union) || len(answer.Entries) != len(union) {
		t.Errorf("%s counts %d and answers %d entries, want the %d of the union of the four arms", what, answer.Total, len(answer.Entries), len(union))
	}
}

// c-inbox: the inbox is one read from the person's own node, and each of its four arms - delivery,
// authorship, participation and mention - answers the set the fixture states for it; then, over a
// server whose writes are made through its routes, how the kinds of entry differ in their read
// state.
func TestTheInboxAnswersEveryRelationOfAPerson(t *testing.T) {
	rig := newPersonInboxRig(t)
	corpus := seedPersonInboxCorpus(t, rig.handler, rig.database)

	t.Run("one read from the person's own node answers each arm's own set, and the inbox is their union", func(t *testing.T) {
		answer, statements := rig.read(t, corpus.person, nil)
		if len(statements) != 1 {
			t.Errorf("the inbox read ran %d statements, want one:\n%s", len(statements), strings.Join(statements, "\n---\n"))
		}
		corpus.requireArms(t, "the inbox", answer)
	})

	t.Run("a delivered entry arrives unread, and what the person wrote or takes part in arrives read", personInboxKindsOfEntry)
}

// personInboxKindsOfEntry is c-inbox's statement of how the kinds of entry differ, over a server
// whose every write is made through its routes, so each read state is the one the writers and the
// read keep: a delivered entry arrives unread, what the person wrote and what they take part in
// arrive read, the person's own event never reaches them, the reply sits among the entries, and
// reading an entry and Mark all read are tuples that survive a reload.
func personInboxKindsOfEntry(t *testing.T) {
	const person = "alice"
	handler, database, root, reply, _ := directConversationFrom(t, person)
	direct := decodeBody[model.Message](t, reply("The dashboard is at /dash."))
	issue := createTestIssue(t, handler, "KINDS", "The kinds of entry")
	thread := createThreadComment(t, handler, issue, map[string]any{"body": "Alice asks the team."}, person)
	delivered := createThreadComment(t, handler, issue, map[string]any{"body": "Bob answers alice.", "reply_to": thread.ID}, "bob")
	second := createThreadComment(t, handler, issue, map[string]any{"body": "Alice asks again."}, person)
	secondReply := createThreadComment(t, handler, issue, map[string]any{"body": "Bob answers again.", "reply_to": second.ID}, "bob")
	taken := createThreadComment(t, handler, issue, map[string]any{"body": "Bob starts a thread."}, "bob")
	written := createThreadComment(t, handler, issue, map[string]any{"body": "Alice writes in bob's thread.", "reply_to": taken.ID}, person)
	note := createThreadComment(t, handler, issue, map[string]any{"body": "Alice notes something nobody answers."}, person)
	unreadQuery := url.Values{"state": {myRepliesStateUnread}}
	inboxQuery := url.Values{"state": {myRepliesStateInbox}}
	commentKey := func(id string) string { return graphstore.KindComment + ":" + id }

	// marks is the person's current read marks from the store, as kind/far end -> moment.
	marks := func(t *testing.T) map[string]time.Time {
		t.Helper()
		tuples, err := graphstore.CurrentOut(context.Background(), database.Pool, graphstore.KindPerson, person,
			[]string{graphstore.TupleReadThrough, graphstore.TupleReplyRead})
		if err != nil {
			t.Fatalf("read %s's read marks from the store: %v", person, err)
		}
		held := map[string]time.Time{}
		for _, tuple := range tuples {
			held[tuple.Kind+"/"+tuple.ToKind+":"+tuple.ToID] = tuple.At
		}
		return held
	}
	requireMark := func(t *testing.T, what, kind, toKind, toID string, through time.Time) {
		t.Helper()
		key := kind + "/" + toKind + ":" + toID
		if at, held := marks(t)[key]; !held || at.Before(through) {
			t.Errorf("%s: the store holds no current %s tuple from %s to %s:%s at or after %s (held: %v, at %s)",
				what, kind, person, toKind, toID, through.Format(time.RFC3339Nano), held, at.Format(time.RFC3339Nano))
		}
	}

	t.Run("a delivered entry arrives unread", func(t *testing.T) {
		walked := readMeInboxAs(t, handler, person, nil)
		for _, node := range []struct{ kind, id, what string }{
			{graphstore.KindComment, delivered.ID, "bob's reply in her thread"},
			{graphstore.KindMessage, direct.ID, "the session's reply to her direct message"},
		} {
			entry, held := meInboxEntryOfNode(walked, node.kind, node.id)
			if !held || !slices.Contains(meInboxKinds(entry), meInboxDelivered+"/"+meInboxInbound) {
				t.Errorf("%s is no delivered entry of her read: it carries %v", node.what, meInboxKinds(entry))
			}
		}
		unread := readMeInboxAs(t, handler, person, unreadQuery)
		if entry, held := meInboxEntryOfNode(unread, graphstore.KindComment, thread.ID); !held || entry.Thread == nil || !entry.Thread.Unread || entry.Thread.Reply.ID != delivered.ID {
			t.Errorf("the unread narrowing draws her thread as %+v, want it unread with bob's reply %s as its newest write", entry.Thread, delivered.ID)
		}
		if entry, held := meInboxEntryOfNode(unread, graphstore.KindMessage, root.ID); !held || entry.Agent == nil || !entry.Agent.Unread || !slices.Contains(entry.Agent.UnreadReplyIDs, direct.ID) {
			t.Errorf("the unread narrowing draws her direct conversation as %+v, want it unread with the session's reply %s", entry.Agent, direct.ID)
		}
	})

	// What the person wrote and what she takes part in carry no delivery, so the unread narrowing
	// leaves them out, and the read state each arrives in is a tuple: the writer of a write keeps
	// its own writer's row read through it, and states that mark as a read_through from her node to
	// the thread's root (inboxwrite's queueKeepRow), standing past every write somebody else made
	// there before hers - bob's root of the thread she wrote in, and nothing in her own.
	t.Run("what the person wrote and what they take part in arrive read", func(t *testing.T) {
		walked := readMeInboxAs(t, handler, person, nil)
		unread := readMeInboxAs(t, handler, person, unreadQuery)
		inbox := readMeInboxAs(t, handler, person, inboxQuery)
		for _, node := range []struct {
			id, kind, direction, what string
			root                      string
			through                   time.Time
		}{
			{note.ID, meInboxAuthored, meInboxInbound, "the thread she wrote and nobody answered", note.ID, time.Time{}},
			{written.ID, meInboxAuthored, meInboxInbound, "her comment in bob's thread", taken.ID, taken.CreatedAt},
			{taken.ID, meInboxParticipates, meInboxOutbound, "bob's thread she wrote in", taken.ID, taken.CreatedAt},
		} {
			entry, held := meInboxEntryOfNode(walked, graphstore.KindComment, node.id)
			if kinds := meInboxKinds(entry); !held || !slices.Contains(kinds, node.kind+"/"+node.direction) || slices.Contains(kinds, meInboxDelivered+"/"+meInboxInbound) {
				t.Errorf("%s stands in her read carrying %v, want its %s relation and no delivery", node.what, kinds, node.kind)
			}
			if _, held := meInboxEntryOfNode(unread, graphstore.KindComment, node.id); held {
				t.Errorf("%s arrives unread: the unread narrowing holds it", node.what)
			}
			if entry, held := meInboxEntryOfNode(inbox, graphstore.KindComment, node.id); held && (entry.Thread == nil || entry.Thread.Unread) {
				t.Errorf("her inbox draws %s as %+v, want it read", node.what, entry.Thread)
			}
			requireMark(t, node.what, graphstore.TupleReadThrough, graphstore.KindComment, node.root, node.through)
		}
	})

	t.Run("the reply sits among the entries and not in a second view", func(t *testing.T) {
		response := meInboxRequestAs(t, handler, context.Background(), person, inboxQuery)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
			t.Fatalf("decode her inbox: %v", err)
		}
		var lists []string
		for field, value := range fields {
			if strings.HasPrefix(strings.TrimSpace(string(value)), "[") {
				lists = append(lists, field)
			}
		}
		if !slices.Equal(lists, []string{"entries"}) {
			t.Errorf("her inbox answers the lists %v, want its entries alone", lists)
		}
		inbox := decodeMeInbox(t, response)
		if entry, held := meInboxEntryOfNode(inbox, graphstore.KindMessage, root.ID); !held || entry.Agent == nil || entry.Agent.Reply.ID != direct.ID {
			t.Errorf("her inbox's entries draw no row whose reply is the session's reply %s: %v", direct.ID, meInboxNodes(inbox))
		}
		if entry, held := meInboxEntryOfNode(inbox, graphstore.KindComment, thread.ID); !held || entry.Thread == nil || entry.Thread.Reply.ID != delivered.ID {
			t.Errorf("her inbox's entries draw no row whose reply is bob's reply %s: %v", delivered.ID, meInboxNodes(inbox))
		}
	})

	t.Run("reading an entry marks it read, as a tuple", func(t *testing.T) {
		unread := readMeInboxAs(t, handler, person, unreadQuery)
		read := dispatchRequest(t, handler, http.MethodPut, "/api/v1/me/threads/"+thread.ID+"/read",
			map[string]any{"read_through": unread.AsOf.Format(time.RFC3339Nano)}, person)
		if read.Code != http.StatusNoContent {
			t.Fatalf("read her thread: status=%d body=%s", read.Code, read.Body.String())
		}
		if _, held := meInboxEntryOfNode(readMeInboxAs(t, handler, person, unreadQuery), graphstore.KindComment, thread.ID); held {
			t.Errorf("her thread is still unread after she read it")
		}
		requireMark(t, "reading her thread", graphstore.TupleReadThrough, graphstore.KindComment, thread.ID, delivered.CreatedAt)
	})

	t.Run("Mark all read empties the unread count, as tuples", func(t *testing.T) {
		before := readMeInboxAs(t, handler, person, unreadQuery)
		if before.Total == 0 {
			t.Fatalf("nothing is unread before Mark all read, so emptying the count would prove nothing")
		}
		marked := dispatchRequest(t, handler, http.MethodPost, "/api/v1/me/inbox/read",
			map[string]any{"read_through": before.AsOf.Format(time.RFC3339Nano)}, person)
		if marked.Code != http.StatusOK {
			t.Fatalf("Mark all read: status=%d body=%s", marked.Code, marked.Body.String())
		}
		if after := readMeInboxAs(t, handler, person, unreadQuery); after.Total != 0 || len(after.Entries) != 0 {
			t.Errorf("after Mark all read the unread count is %d (%v), want 0", after.Total, meInboxNodes(after))
		}
		requireMark(t, "Mark all read", graphstore.TupleReadThrough, graphstore.KindComment, second.ID, secondReply.CreatedAt)
		requireMark(t, "Mark all read", graphstore.TupleReplyRead, graphstore.KindMessage, direct.ID, time.Time{})
	})

	t.Run("both survive a reload", func(t *testing.T) {
		reloaded, _, _ := newTestServer(t, testServerOptions{store: database})
		for _, server := range []struct {
			what    string
			handler http.Handler
		}{{"a fresh read", handler}, {"a server started again on the same store", reloaded}} {
			unread := readMeInboxAs(t, server.handler, person, unreadQuery)
			if unread.Total != 0 || len(unread.Entries) != 0 {
				t.Errorf("%s counts %d unread (%v), want 0 after Mark all read", server.what, unread.Total, meInboxNodes(unread))
			}
			inbox := readMeInboxAs(t, server.handler, person, inboxQuery)
			if entry, held := meInboxEntryOfNode(inbox, graphstore.KindComment, thread.ID); !held || entry.Thread == nil || entry.Thread.Unread {
				t.Errorf("%s draws the thread she read as %+v, want it read", server.what, entry.Thread)
			}
		}
		requireMark(t, "after a reload", graphstore.TupleReadThrough, graphstore.KindComment, thread.ID, delivered.CreatedAt)
		requireMark(t, "after a reload", graphstore.TupleReadThrough, graphstore.KindComment, second.ID, secondReply.CreatedAt)
	})

	t.Run("the person's own event stays out of their own inbox", func(t *testing.T) {
		own := createThreadComment(t, handler, issue, map[string]any{"body": "Alice's own note."}, person)
		ownReply := createThreadComment(t, handler, issue, map[string]any{"body": "Alice adds to her own thread.", "reply_to": second.ID}, person)
		walked := readMeInboxAs(t, handler, person, nil)
		inbox := readMeInboxAs(t, handler, person, inboxQuery)
		unread := readMeInboxAs(t, handler, person, unreadQuery)
		for _, event := range []struct{ id, what string }{{own.ID, "her own comment"}, {ownReply.ID, "her own reply in her thread"}} {
			if reached := meInboxDeliveredTo(t, database, graphstore.KindComment, event.id); slices.Contains(reached, graphstore.KindPerson+":"+person) {
				t.Errorf("%s drew a delivery to her: %v", event.what, reached)
			}
			if slices.Contains(personInboxArmOf(walked, meInboxDelivered, meInboxInbound), commentKey(event.id)) {
				t.Errorf("%s is a delivered entry of her read", event.what)
			}
			if _, held := meInboxEntryOfNode(inbox, graphstore.KindComment, event.id); held {
				t.Errorf("%s is a row of her inbox", event.what)
			}
		}
		if unread.Total != 0 {
			t.Errorf("her own events left %d unread (%v), want 0", unread.Total, meInboxNodes(unread))
		}
	})
}

// provePersonSession writes a comment login's credential made from session, through the store's
// writer of who wrote a thing, which states the session's alias_of to login beside it: the
// session is one of login's own nodes from then on.
func provePersonSession(t *testing.T, database *store.Store, login, session string, at time.Time) {
	t.Helper()
	comment := uuid.NewString()
	author := model.Actor{Kind: "user", ID: login, Act: &model.ActorAct{Session: session}}
	seed := meInboxSeed{t: t, database: database}
	seed.node(graphstore.KindComment, comment, graphstore.VisibilityProject, author, at)
	seed.write(func(ctx context.Context, tx pgx.Tx) error {
		return store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
			return graphstore.QueueAuthored(b, graphstore.KindComment, comment, author, at, graphstore.NoEvent)
		})
	})
}

// meInboxEntriesOf is how many entries of answer name the node kind/id.
func meInboxEntriesOf(answer meInboxResponse, kind, id string) int {
	count := 0
	for _, entry := range answer.Entries {
		if entry.Node.Kind == kind && entry.Node.ID == id {
			count++
		}
	}
	return count
}

// c-inbox-one-read: one database statement answers the inbox, read from what the request sent to
// the database rather than from the request; a read narrowed to three arms leaves the fourth's
// entries and is still one statement. The delivery walk, not the one read, holds the cycle guard
// and the depth bound, and the test reads each apart; and eventDestinations, run in process over
// the same rows by the outbox, is a second opinion on the delivery arm.
func TestTheInboxIsOneDatabaseQuery(t *testing.T) {
	depth := meInboxWalkDepth(t)
	logged := &logtest.Buffer{}
	logtest.SetDefault(t, slog.NewTextHandler(logged, nil))
	rig := newPersonInboxRig(t)
	corpus := seedPersonInboxCorpus(t, rig.handler, rig.database)

	t.Run("one statement answers the inbox", func(t *testing.T) {
		answer, statements := rig.read(t, corpus.person, nil)
		for index, statement := range statements {
			t.Logf("statement %d of %d the read sent: %s", index+1, len(statements), statement)
		}
		if len(statements) != 1 {
			t.Errorf("the inbox read ran %d statements, want one:\n%s", len(statements), strings.Join(statements, "\n---\n"))
		}
		corpus.requireArms(t, "the inbox", answer)
	})

	t.Run("a read without one arm leaves that arm's entries in the same single statement", func(t *testing.T) {
		dropped := corpus.arms[len(corpus.arms)-1]
		var kinds []string
		for _, arm := range corpus.arms[:len(corpus.arms)-1] {
			kinds = append(kinds, arm.kind)
		}
		answer, statements := rig.read(t, corpus.person, url.Values{"kind": {strings.Join(kinds, ",")}})
		if len(statements) != 1 {
			t.Errorf("the read without the %s arm ran %d statements, want one:\n%s", dropped.name, len(statements), strings.Join(statements, "\n---\n"))
		}
		for _, node := range dropped.nodes {
			if _, held := meInboxEntryOfNode(answer, node.kind, node.id); held {
				t.Errorf("the read without the %s arm (%s) still answers %s", dropped.name, dropped.kind, node)
			}
		}
		for _, arm := range corpus.arms[:len(corpus.arms)-1] {
			got := personInboxArmOf(answer, arm.kind, arm.direction)
			for _, node := range arm.nodes {
				if !slices.Contains(got, node.key()) {
					t.Errorf("the read without the %s arm does not answer %s by its %s relation (%s)", dropped.name, node, arm.kind, arm.direction)
				}
			}
		}
	})

	// The delivery walk's own corpus, on a server whose writes are made through its routes, read
	// by the outbox that runs the walk.
	handler, database, deps := newTestServer(t, testServerOptions{})
	cycleIssue := createTestIssue(t, handler, "CYCLE", "Two comments that reply to each other")
	first := meInboxSessionComment(t, handler, cycleIssue, "s-first", "The first of a cycle.")
	second := meInboxSessionComment(t, handler, cycleIssue, "s-second", "The second of a cycle.")
	cycleReply := createThreadComment(t, handler, cycleIssue, map[string]any{"body": "Alice answers the first.", "reply_to": first}, "alice").ID
	// comments.reply_to carries no acyclicity constraint, so the two reply to each other once the
	// reply that names the first is written.
	if _, err := database.Pool.Exec(context.Background(), `
		update comments set reply_to = case id when $1::uuid then $2::uuid else $1::uuid end where id in ($1::uuid, $2::uuid)
	`, first, second); err != nil {
		t.Fatalf("tie the two comments into a cycle: %v", err)
	}
	// An acyclic chain one comment longer than the walk's bound: a root and depth+1 replies, each
	// by a session of its own and each under the one before, and alice's reply under the root,
	// which the walk reads the chain down from.
	chainIssue := createTestIssue(t, handler, "DEEP", fmt.Sprintf("A chain of %d replies", depth+1))
	chain := make([]string, 0, depth+2)
	for index := 0; index <= depth+1; index++ {
		chain = append(chain, meInboxSessionComment(t, handler, chainIssue, fmt.Sprintf("s-%02d", index), fmt.Sprintf("Link %d.", index)))
	}
	chainReply := createThreadComment(t, handler, chainIssue, map[string]any{"body": "Alice answers the root.", "reply_to": chain[0]}, "alice").ID
	for index := 1; index < len(chain); index++ {
		if _, err := database.Pool.Exec(context.Background(), `update comments set reply_to = $2 where id = $1`, chain[index], chain[index-1]); err != nil {
			t.Fatalf("chain link %d: %v", index, err)
		}
	}
	settle, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	settleOutboxWithin(t, settle, deps, cycleIssue, "a cycle of two comments under alice's reply")
	settleOutboxWithin(t, settle, deps, chainIssue, fmt.Sprintf("a chain of %d replies", depth+1))
	// dana's credential proved three of the sessions hers: the first of the cycle, the one at the
	// chain's bound and the one past it.
	atBound, pastBound := fmt.Sprintf("s-%02d", depth), fmt.Sprintf("s-%02d", depth+1)
	danas := []string{"s-first", atBound, pastBound}
	for index, session := range danas {
		provePersonSession(t, database, "dana", session, time.Date(2026, 11, 18, 10, index, 0, 0, time.UTC))
	}
	sessionsReached := func(t *testing.T, comment string) []string {
		t.Helper()
		sessions := []string{}
		for _, reached := range meInboxDeliveredTo(t, database, graphstore.KindComment, comment) {
			if strings.HasPrefix(reached, graphstore.KindSession+":") {
				sessions = append(sessions, strings.TrimPrefix(reached, graphstore.KindSession+":"))
			}
		}
		return sessions
	}
	// readReturning reads login's inbox under a deadline, failing where the read does not return.
	readReturning := func(t *testing.T, login string) meInboxResponse {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		started := time.Now()
		response := meInboxRequestAs(t, handler, ctx, login, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s's read over the cyclic thread did not return an answer within 30s (it took %s): status %d %s",
				login, time.Since(started), response.Code, response.Body.String())
		}
		return decodeMeInbox(t, response)
	}

	t.Run("the cyclic thread is listed once, and the read returns", func(t *testing.T) {
		alices := readReturning(t, "alice")
		if count := meInboxEntriesOf(alices, graphstore.KindComment, cycleReply); count != 1 {
			t.Errorf("alice's reply in the cyclic thread is %d entries of her read, want one: %v", count, meInboxNodes(alices))
		}
		danaRead := readReturning(t, "dana")
		for _, node := range []struct{ id, what string }{{first, "the cycle's first comment, by her session"}, {cycleReply, "alice's reply, delivered to her session"}} {
			if count := meInboxEntriesOf(danaRead, graphstore.KindComment, node.id); count != 1 {
				t.Errorf("%s is %d entries of dana's read, want one: %v", node.what, count, meInboxNodes(danaRead))
			}
		}
		for _, read := range []meInboxResponse{alices, danaRead} {
			if count := meInboxEntriesOf(read, graphstore.KindComment, second); count > 1 {
				t.Errorf("the cycle's second comment is %d entries of %s's read, want at most one", count, read.Person)
			}
		}
		// The delivery walk's cycle guard: alice's reply reached each author of the cycle once.
		if reached := sessionsReached(t, cycleReply); !slices.Equal(reached, []string{"s-first", "s-second"}) {
			t.Errorf("alice's reply in the cycle reached the sessions %v, want s-first and s-second, once each", reached)
		}
		var visits int
		if err := database.Pool.QueryRow(context.Background(), `
			select count(*) from graph_tuples where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4
		`, graphstore.KindComment, cycleReply, meInboxDelivered, graphstore.KindSession).Scan(&visits); err != nil {
			t.Fatalf("count alice's reply's deliveries to sessions: %v", err)
		}
		if visits != 2 {
			t.Errorf("alice's reply in the cycle holds %d deliveries to sessions, want one to each of the cycle's two authors", visits)
		}
	})

	t.Run("the delivery walk stops the chain at its bound, lists nothing past it, and says it cut the chain", func(t *testing.T) {
		reached := sessionsReached(t, chainReply)
		oneBelow := fmt.Sprintf("s-%02d", depth-1)
		if !slices.Contains(reached, atBound) {
			t.Errorf("alice's reply under the chain did not reach %s, the author at the walk's bound %d: %v", atBound, depth, reached)
		}
		if !slices.Contains(reached, oneBelow) {
			t.Errorf("alice's reply under the chain did not reach %s, the author one below the bound: %v", oneBelow, reached)
		}
		if slices.Contains(reached, pastBound) {
			t.Errorf("alice's reply under the chain reached %s, past the walk's bound %d: %v", pastBound, depth, reached)
		}
		want := []string{}
		for index := 0; index <= depth; index++ {
			want = append(want, fmt.Sprintf("s-%02d", index))
		}
		if !slices.Equal(reached, want) {
			t.Errorf("alice's reply under a chain of %d replies reached %v, want the authors down to the bound, %v", depth+1, reached, want)
		}
		const cutLine = "the comment thread walk stopped at its depth bound"
		if lines := logged.Lines(cutLine); !strings.Contains(lines, chain[0]) || !strings.Contains(lines, "depth="+strconv.Itoa(depth)) {
			t.Errorf("the walk of a chain of %d replies did not say it cut the chain under %s at depth %d; it logged %q", depth+1, chain[0], depth, lines)
		}
		danaRead := readReturning(t, "dana")
		if entry, held := meInboxEntryOfNode(danaRead, graphstore.KindComment, chainReply); !held || !slices.Contains(meInboxKinds(entry), meInboxDelivered+"/"+meInboxInbound) {
			t.Errorf("alice's reply, delivered to %s at the bound, is no delivered entry of dana's read: %v", atBound, meInboxKinds(entry))
		}
	})

	t.Run("eventDestinations over the same rows sends what the delivery arm holds", func(t *testing.T) {
		subjects := map[string]string{}
		for _, session := range danas {
			for _, form := range contracts.TopicForms(contracts.LegacyAgentSubject(session)) {
				subjects[form] = session
			}
		}
		// What the outbox's eventDestinations sent each comment's comment.created to, read from the
		// destinations its batch recorded for it.
		comments := append([]string{first, second, cycleReply, chainReply}, chain...)
		var sent []string
		for _, comment := range comments {
			for _, destination := range publishedDestinationsOf(t, database, "comment.created", comment) {
				if _, hers := subjects[destination]; hers {
					sent = append(sent, graphstore.KindComment+":"+comment)
					break
				}
			}
		}
		for _, want := range []string{cycleReply, chainReply} {
			if !slices.Contains(sent, graphstore.KindComment+":"+want) {
				t.Errorf("eventDestinations sent comment %s to none of dana's sessions %v, so the comparison would read nothing", want, danas)
			}
		}
		arm := []string{}
		for _, key := range personInboxArmOf(readMeInboxAs(t, handler, "dana", nil), meInboxDelivered, meInboxInbound) {
			if strings.HasPrefix(key, graphstore.KindComment+":") {
				arm = append(arm, key)
			}
		}
		for _, key := range sent {
			if !slices.Contains(arm, key) {
				t.Errorf("eventDestinations sends %s to one of dana's sessions, and her read's delivery arm does not hold it", key)
			}
		}
		for _, key := range arm {
			if !slices.Contains(sent, key) {
				t.Errorf("dana's read's delivery arm holds %s, which eventDestinations sends to none of her sessions", key)
			}
		}
	})
}
