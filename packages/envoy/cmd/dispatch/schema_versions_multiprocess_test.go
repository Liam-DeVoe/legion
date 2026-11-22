//go:build multiprocess

// c-schema-versions of dispatch://INFRA-1098: the two block schema bumps the children take, one
// each and in order, and the safety of a bump while two processes serve one database - a process
// still holding the schema before it admits no write of a type it does not hold, and the switch
// leaves no open editor read-only. Its own file, under the multiprocess tag and the
// dispatch-multiprocess job as multiprocess_test.go's rows are, because it starts two real server
// processes and waits on their listeners and their restarts.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/docs/docstest"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/schemanodes"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testport"
)

// issueTreeBlockSchema is dispatch://INFRA-1253's type migration, the bump that lands the
// criterion type, and issueTreeWriter the writer every row it writes carries.
const (
	issueTreeBlockSchema = "20261116163700_issue_tree_block_schema.up.sql"
	issueTreeWriter      = `{"kind": "system", "id": "block-schema-issue-tree"}`
)

// staleSchemaWait bounds each wait of the row on a process: an install after a supertag write, a
// registry row's refresh, a socket's close, a listener's new connection.
const staleSchemaWait = 30 * time.Second

// schemaBumps is what the store's schema says of the two bumps: the version main held before
// dispatch://INFRA-1150 merged, the version that child's decision and reading types stand at, and
// the version dispatch://INFRA-1253's criterion type stands at.
type schemaBumps struct {
	main, decision, criterion int
}

// readSchemaBumps reads the store's schema, built from its type nodes, and the build's
// blocks.json, and fails t on each clause of the two bumps either breaks: both of INFRA-1150's
// types at one version, the next after the one main held; INFRA-1253's criterion at the version
// after that, a version of its own; no more than two versions above main's; and blocks.json and
// the store agreeing on each, since each child writes its version as a store migration too. It
// logs every version and the elements it introduces.
func readSchemaBumps(t *testing.T, held, file pmdoc.BlockSchema) schemaBumps {
	t.Helper()
	introduced := map[int][]string{}
	since := map[string]int{}
	for _, typ := range held.Types {
		version := max(typ.Since, 1)
		since[typ.Name] = version
		introduced[version] = append(introduced[version], "type "+typ.Name)
		for _, attribute := range typ.Attributes {
			if attribute.Since > 1 {
				introduced[attribute.Since] = append(introduced[attribute.Since], "attribute "+typ.Name+"."+attribute.Name)
			}
			for item, versions := range attribute.ItemVersions {
				if versions.Since > 1 {
					introduced[versions.Since] = append(introduced[versions.Since], "item "+item+" of "+typ.Name+"."+attribute.Name)
				}
			}
			for choice, versions := range attribute.ChoiceVersions {
				if versions.Since > 1 {
					introduced[versions.Since] = append(introduced[versions.Since], "choice "+choice+" of "+typ.Name+"."+attribute.Name)
				}
			}
		}
	}
	versions := make([]int, 0, len(introduced))
	for version := range introduced {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	for _, version := range versions {
		sort.Strings(introduced[version])
		t.Logf("the store's schema, at version %d installed, introduces at version %d: %s", held.Version, version, strings.Join(introduced[version], ", "))
	}
	bumps := schemaBumps{decision: since["decision"], criterion: since["criterion"]}
	for _, version := range versions {
		if version < bumps.decision {
			bumps.main = version
		}
	}
	reading := since["reading"]
	if bumps.decision == 0 || reading == 0 || bumps.criterion == 0 {
		t.Fatalf("the store's schema declares decision at %d, reading at %d and criterion at %d, want all three", bumps.decision, reading, bumps.criterion)
	}
	if reading != bumps.decision {
		t.Errorf("the reading's type stands at version %d and the decision's at %d: dispatch://INFRA-1150 declares both in one bump, and version %d is a second one, introducing %v",
			reading, bumps.decision, max(reading, bumps.decision), introduced[max(reading, bumps.decision)])
	}
	if bumps.decision != bumps.main+1 {
		t.Errorf("dispatch://INFRA-1150's types stand at version %d, want the next after the version main held, %d", bumps.decision, bumps.main)
	}
	if bumps.criterion == bumps.decision || bumps.criterion == reading {
		t.Errorf("the criterion's type and dispatch://INFRA-1150's types share version %d: two children declare one version, and the admission compares numbers, not types", bumps.criterion)
	}
	if bumps.criterion != bumps.decision+1 {
		t.Errorf("dispatch://INFRA-1253's criterion stands at version %d, want the one after dispatch://INFRA-1150's %d", bumps.criterion, bumps.decision)
	}
	for _, version := range []int{bumps.decision, bumps.criterion} {
		for _, element := range introduced[version] {
			name, isType := strings.CutPrefix(element, "type ")
			if !isType {
				continue
			}
			want := version == bumps.decision && (name == "decision" || name == "reading") || version == bumps.criterion && name == "criterion"
			if !want {
				t.Errorf("version %d introduces the type %s beside its child's own: one bump carries one child's types", version, name)
			}
		}
	}
	if held.Version > bumps.main+2 {
		t.Errorf("this tree's store declares version %d, %d above the version main held, %d: two bumps, one per child that adds a block type, and no more",
			held.Version, held.Version-bumps.main, bumps.main)
	}
	if file.Version != held.Version {
		t.Errorf("blocks.json is at version %d and the store's type nodes at %d", file.Version, held.Version)
	}
	for _, name := range []string{"decision", "reading", "criterion"} {
		index := slices.IndexFunc(file.Types, func(typ pmdoc.BlockTypeSchema) bool { return typ.Name == name })
		if index < 0 || max(file.Types[index].Since, 1) != since[name] {
			declared := 0
			if index >= 0 {
				declared = file.Types[index].Since
			}
			t.Errorf("blocks.json declares %s at version %d and the store's type nodes at %d: each child writes its version as a store migration too", name, declared, since[name])
		}
	}
	return bumps
}

// takeTheStoreBackOneBump leaves the store's type nodes as they stood before dispatch://INFRA-1253's
// bump: every node and tuple that bump wrote taken out, and the type migrations before it run again
// (runTypeMigrationsAgain), which puts back what that bump moved - every block type's version, and
// the criterion supertag as one that declares no block type.
func takeTheStoreBackOneBump(t *testing.T, ctx context.Context, pool *store.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `delete from graph_tuples where who = $1::jsonb`, issueTreeWriter); err != nil {
		t.Fatalf("take out the tuples %s wrote: %v", issueTreeBlockSchema, err)
	}
	if _, err := pool.Exec(ctx, `delete from graph_nodes where made_by = $1::jsonb`, issueTreeWriter); err != nil {
		t.Fatalf("take out the nodes %s made: %v", issueTreeBlockSchema, err)
	}
	var before []string
	for _, name := range store.TypeMigrations {
		if name != issueTreeBlockSchema {
			before = append(before, name)
		}
	}
	runTypeMigrationsAgain(t, ctx, pool, before)
}

// schemaChannel is the channel a supertag write announces itself on (schemanodes.Channel). Only a
// process's shared listener LISTENs on it, so a connection that names it is that listener.
var schemaChannel = []byte(schemanodes.Channel)

// schemaNotifyHold stands between one server process and Postgres and passes every byte both
// ways. While held it keeps back everything Postgres sends that process's schema listener - each
// connection that has named the schema channel - so the process hears no supertag write, and a
// listener it opens again, which names the channel as it LISTENs, waits on that LISTEN until the
// hold is released. Released, it sends what it kept, in order. cut closes the listener's
// connections, as a dropped network does, so the process opens its listener again.
type schemaNotifyHold struct {
	listener net.Listener
	upstream string
	mu       sync.Mutex
	held     bool
	links    map[*schemaNotifyLink]struct{}
	// listens counts the connections that have named the schema channel, and named is closed and
	// replaced each time one does.
	listens int
	named   chan struct{}
}

// schemaNotifyLink is one connection through the hold: the process's side and Postgres's, whether
// it has named the schema channel, and what Postgres sent it while the hold kept it back.
type schemaNotifyLink struct {
	hold           *schemaNotifyHold
	client, server net.Conn
	mu             sync.Mutex
	listening      bool
	pending        [][]byte
}

// startSchemaNotifyHold listens on a loopback address of its own (testport.ListenLoopback) in front
// of database's Postgres until the test ends.
func startSchemaNotifyHold(t *testing.T, database *store.Store) *schemaNotifyHold {
	t.Helper()
	listener, err := testport.ListenLoopback()
	if err != nil {
		t.Fatalf("listen for the hold in front of Postgres: %v", err)
	}
	config := database.Pool.Config().ConnConfig
	hold := &schemaNotifyHold{
		listener: listener, upstream: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))),
		links: map[*schemaNotifyLink]struct{}{}, named: make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = listener.Close()
		hold.mu.Lock()
		links := make([]*schemaNotifyLink, 0, len(hold.links))
		for link := range hold.links {
			links = append(links, link)
		}
		hold.mu.Unlock()
		for _, link := range links {
			link.close()
		}
	})
	go hold.accept()
	return hold
}

func (h *schemaNotifyHold) accept() {
	for {
		client, err := h.listener.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", h.upstream)
		if err != nil {
			_ = client.Close()
			continue
		}
		link := &schemaNotifyLink{hold: h, client: client, server: server}
		h.mu.Lock()
		h.links[link] = struct{}{}
		h.mu.Unlock()
		go link.up()
		go link.down()
	}
}

// up passes what the process sends to Postgres, and marks the link its schema listener once what
// it sends names the schema channel.
func (l *schemaNotifyLink) up() {
	defer l.close()
	buffer := make([]byte, 32<<10)
	var tail []byte
	for {
		n, err := l.client.Read(buffer)
		if n > 0 {
			l.mu.Lock()
			listening := l.listening
			l.mu.Unlock()
			if !listening {
				scanned := append(tail, buffer[:n]...)
				if bytes.Contains(scanned, schemaChannel) {
					l.markListening()
				}
				tail = append([]byte(nil), scanned[max(0, len(scanned)-len(schemaChannel)+1):]...)
			}
			if _, err := l.server.Write(buffer[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// down passes what Postgres sends the process, but keeps it back for a listening link while the
// hold is held; anything kept goes before anything sent after it.
func (l *schemaNotifyLink) down() {
	defer l.close()
	buffer := make([]byte, 32<<10)
	for {
		n, err := l.server.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			l.mu.Lock()
			if l.listening && l.hold.isHeld() {
				l.pending = append(l.pending, chunk)
				l.mu.Unlock()
			} else {
				werr := l.flushLocked()
				if werr == nil {
					_, werr = l.client.Write(chunk)
				}
				l.mu.Unlock()
				if werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (l *schemaNotifyLink) flushLocked() error {
	for _, chunk := range l.pending {
		if _, err := l.client.Write(chunk); err != nil {
			return err
		}
	}
	l.pending = nil
	return nil
}

func (l *schemaNotifyLink) markListening() {
	l.mu.Lock()
	l.listening = true
	l.mu.Unlock()
	l.hold.mu.Lock()
	l.hold.listens++
	close(l.hold.named)
	l.hold.named = make(chan struct{})
	l.hold.mu.Unlock()
}

func (l *schemaNotifyLink) close() {
	_ = l.client.Close()
	_ = l.server.Close()
	l.hold.mu.Lock()
	delete(l.hold.links, l)
	l.hold.mu.Unlock()
}

func (h *schemaNotifyHold) isHeld() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held
}

// hold starts keeping back what Postgres sends the schema listener.
func (h *schemaNotifyHold) hold() {
	h.mu.Lock()
	h.held = true
	h.mu.Unlock()
}

// release sends each listener what was kept back from it, and keeps nothing more.
func (h *schemaNotifyHold) release() {
	h.mu.Lock()
	h.held = false
	links := make([]*schemaNotifyLink, 0, len(h.links))
	for link := range h.links {
		links = append(links, link)
	}
	h.mu.Unlock()
	for _, link := range links {
		link.mu.Lock()
		_ = link.flushLocked()
		link.mu.Unlock()
	}
}

// cut closes every listening link, and answers how many connections had named the schema channel
// by then.
func (h *schemaNotifyHold) cut() int {
	h.mu.Lock()
	listens := h.listens
	var listening []*schemaNotifyLink
	for link := range h.links {
		link.mu.Lock()
		if link.listening {
			listening = append(listening, link)
		}
		link.mu.Unlock()
	}
	h.mu.Unlock()
	for _, link := range listening {
		link.close()
	}
	return listens
}

// listening answers how many connections have named the schema channel.
func (h *schemaNotifyHold) listening() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listens
}

// awaitListen waits up to staleSchemaWait for a connection after the first after to name the
// schema channel: the process opening its listener again.
func (h *schemaNotifyHold) awaitListen(t *testing.T, after int, what string) {
	t.Helper()
	deadline := time.After(staleSchemaWait)
	for {
		h.mu.Lock()
		listens, named := h.listens, h.named
		h.mu.Unlock()
		if listens > after {
			return
		}
		select {
		case <-named:
		case <-deadline:
			t.Fatalf("%s: no connection named the schema channel within %s", what, staleSchemaWait)
		}
	}
}

// databaseURL is the URL a process reaches database through the hold by.
func (h *schemaNotifyHold) databaseURL(t *testing.T, database *store.Store) string {
	t.Helper()
	address, err := url.Parse(databaseURLOf(t, database))
	if err != nil {
		t.Fatalf("parse the database URL: %v", err)
	}
	address.Host = h.listener.Addr().String()
	return address.String()
}

// startHeldProcess starts the pair's second process, reaching Postgres through hold and NATS
// through a proxy of its own, and waits until it is healthy.
func (p *dispatchPair) startHeldProcess(t *testing.T, hold *schemaNotifyHold) *dispatchProcess {
	t.Helper()
	proxy := testnats.StartProxy(t, p.nats)
	process := startDispatchProcessWith(t, hold.databaseURL(t, p.database),
		dispatchOptions{NATSURL: proxy.URL(), SigningKey: multiprocessSigningKey})
	p.proxies[process] = proxy
	return process
}

// schemaPage is a page of one build of the dashboard: the schema version it reads and the highest
// since among the elements it may create, the two numbers its document socket presents.
type schemaPage struct{ version, creatable int }

// schemaPageWords is the paragraph the row's document holds, which a page's copy holds once the
// room has sent it the document.
const schemaPageWords = "A paragraph both processes read."

// openSchemaPage connects page to the document's socket on p as the test's login, with doc as its
// copy, binding fragment; authenticates it with the page's schema version, as its provider does;
// waits until its copy holds the document; and answers it and the scope the room granted.
func (p *dispatchProcess) openSchemaPage(t *testing.T, artifactID, fragment string, page schemaPage, doc *crdt.Doc) (*docstest.Peer, string) {
	t.Helper()
	address := fmt.Sprintf("ws://127.0.0.1:%d/ws/doc/%s?schema_version=%d&schema_creatable=%d", p.port, artifactID, page.version, page.creatable)
	peer := docstest.Dial(t, address, http.Header{"X-Dispatch-User": {dispatchTestLogin}}, artifactID, doc)
	peer.Fragment = fragment
	scope, err := peer.Authenticate(strconv.Itoa(page.version), 10*time.Second)
	if err != nil {
		t.Fatalf("authenticate the page of version %d: %v\n%s", page.version, err, p.Output.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(markdown(peer), schemaPageWords) {
		if time.Now().After(deadline) {
			t.Fatalf("the page of version %d holds %q, want the document's paragraph", page.version, markdown(peer))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return peer, scope
}

// writeCriterion has the page add a criterion block with id to its copy and send the room the
// update, as an editor that holds the type writes one.
func writeCriterion(t *testing.T, page *docstest.Peer, id string) {
	t.Helper()
	parsed, err := pmdoc.Parse(":::criterion{#" + id + "}\nA criterion the stale process cannot read.\n:::\n")
	if err != nil {
		t.Fatalf("parse the criterion block: %v", err)
	}
	fragment := page.Doc.GetXmlFragment(editorFragment(page))
	var changeErr error
	if _, err := page.Send(func(txn *crdt.Transaction) {
		tree, err := pmdoc.ReadInTransaction(txn, fragment)
		if err != nil {
			changeErr = err
			return
		}
		tree.Children = append(tree.Children, parsed.Children...)
		changeErr = pmdoc.Update(txn, fragment, tree)
	}); err != nil || changeErr != nil {
		t.Fatalf("the page's write of the criterion block: %v %v", err, changeErr)
	}
}

// storedUpdates counts the stored updates of the room the document is edited in.
func storedUpdates(t *testing.T, database *store.Store, artifactID string) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(),
		`select count(*) from doc_updates where artifact_id = `+refs.DocumentRoomSQL("$1::uuid"), artifactID).Scan(&count); err != nil {
		t.Fatalf("count the document's stored updates: %v", err)
	}
	return count
}

// storedWithin answers whether the room stores an update past before within the span given.
func storedWithin(t *testing.T, database *store.Store, artifactID string, before int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if storedUpdates(t, database, artifactID) > before {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitFleet waits until the live server processes are want of them and each registers schema
// version and creatable, and fails t with what they register otherwise.
func awaitFleet(t *testing.T, database *store.Store, want int, version, creatable int, what string) {
	t.Helper()
	deadline := time.Now().Add(staleSchemaWait)
	for {
		live, err := processes.Live(context.Background(), database.Pool)
		if err != nil {
			t.Fatalf("read the live server processes: %v", err)
		}
		agreed := len(live) == want
		var numbers []string
		for _, process := range live {
			numbers = append(numbers, fmt.Sprintf("%d/%d", process.SchemaVersion, process.SchemaCreatable))
			agreed = agreed && process.SchemaVersion == version && process.SchemaCreatable == creatable
		}
		if agreed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the live server processes register %v, want %d of them at %d/%d", what, numbers, want, version, creatable)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// awaitClose waits up to staleSchemaWait for the page's socket to end, and answers the code the
// server closed it with.
func awaitClose(t *testing.T, page *docstest.Peer, what string) int {
	t.Helper()
	select {
	case <-page.Ended:
		return page.CloseCode()
	case <-time.After(staleSchemaWait):
		t.Errorf("%s: the socket is still open %s later", what, staleSchemaWait)
		return 0
	}
}

// TestTwoProcessesNeverAdmitAStaleSchemaWrite holds c-schema-versions. Its first part reads the
// store's schema: dispatch://INFRA-1150 declares the decision and the reading types in one bump, one
// above the version main held before it merged, and dispatch://INFRA-1253 the criterion type in the
// bump after that, each as a store migration as well as in blocks.json, and the tree declares no
// more than those two versions above main's.
//
// Its second part proves a safety property, not an instant of agreement: a process holding an
// obsolete schema cannot accept an incompatible write. Two real server processes serve one
// Postgres and one NATS, the store at the version before INFRA-1253's bump. An editor of the
// decision types, a page of that version, is open on the first process, writable, and a page of
// this build, the bump's version, read-only beside it, before the supertag write. The second
// process reaches Postgres through a hold that keeps back what its schema listener hears. The test
// writes the supertag - INFRA-1253's type migration, run again - and the first process installs the
// new version and closes its document sockets. The editor of the decision types connects to the
// second process and writes a criterion, a type that process does not hold: the second process
// admits it read-only and stores no row of it. The test repeats the write while the second process
// opens its listener again, and again after that process restarts, and reads the same refusal each
// time; a page that connects before every live process has installed the version is admitted
// read-only. Once the hold is released both processes answer the new version, and only then does
// the test read the page of this build, whose socket the server closed, admitted writable when it
// connects again to either process, and both store its criterion.
func TestTwoProcessesNeverAdmitAStaleSchemaWrite(t *testing.T) {
	ctx := context.Background()
	bumps := schemaBumps{main: pmdoc.BuiltSchemaVersion() - 2, decision: pmdoc.BuiltSchemaVersion() - 1, criterion: pmdoc.BuiltSchemaVersion()}
	t.Run("one bump per child that adds block types, in order, over the version main held", func(t *testing.T) {
		head, err := schemanodes.Read(ctx, storetest.Open(t).Pool)
		if err != nil {
			t.Fatalf("build the schema from the store's type nodes: %v", err)
		}
		bumps = readSchemaBumps(t, head, readBlockSchemaFile(t))
	})

	pair := newPair(t)
	takeTheStoreBackOneBump(t, ctx, pair.database.Pool)
	if before, err := schemanodes.Read(ctx, pair.database.Pool); err != nil || before.Version != bumps.decision || servedType(before, "criterion") {
		t.Fatalf("the store taken back before dispatch://INFRA-1253's bump builds version %d with the types %v (%v), want version %d without criterion",
			before.Version, typeNames(before), err, bumps.decision)
	}
	hold := startSchemaNotifyHold(t, pair.database)
	pair.a = pair.startProcess(t, nil)
	pair.b = pair.startHeldProcess(t, hold)
	pair.a.waitHealthy(t)
	pair.b.waitHealthy(t)
	for _, process := range []*dispatchProcess{pair.a, pair.b} {
		if served := process.servedSchema(t); served.Version != bumps.decision || servedType(served, "criterion") {
			t.Fatalf("process %s serves version %d with the types %v, want version %d without criterion", pair.name(process), served.Version, typeNames(served), bumps.decision)
		}
	}
	awaitFleet(t, pair.database, 2, bumps.decision, bumps.decision, "both processes boot on the store before the bump")
	if hold.listening() == 0 {
		t.Fatalf("process B's schema listener never named %s through the hold, so the hold keeps nothing back", schemanodes.Channel)
	}

	project := uniqueProjectKey(t, "SCH")
	pair.a.createProject(t, project, "Schema versions")
	var issue struct {
		Key               string `json:"key"`
		PrimaryArtifactID string `json:"primary_artifact_id"`
	}
	created := pair.a.call(t, http.MethodPost, "/api/v1/issues",
		encodedBody(t, map[string]any{"project": project, "title": "The schema switch", "spec": "# Versions\n\n" + schemaPageWords + "\n", "force": true}), http.StatusCreated)
	if err := json.Unmarshal(created, &issue); err != nil || issue.PrimaryArtifactID == "" {
		t.Fatalf("create the row's issue: %v: %s", err, created)
	}
	document := issue.PrimaryArtifactID
	fragment := pair.a.artifactFragment(t, document)
	decisions := schemaPage{version: bumps.decision, creatable: bumps.decision}
	thisBuild := schemaPage{version: bumps.criterion, creatable: bumps.criterion}

	// Both pages are open on the first process before the supertag write.
	editor, scope := pair.a.openSchemaPage(t, document, fragment, decisions, crdt.New())
	if scope != "read-write" {
		t.Fatalf("before the supertag write process A admits the editor of version %d %s, want read-write: every live process reads and creates what it does", decisions.version, scope)
	}
	built, scope := pair.a.openSchemaPage(t, document, fragment, thisBuild, crdt.New())
	if scope != "readonly" {
		t.Errorf("before the supertag write process A admits the page of version %d %s, want read-only: no live process reads the version it creates", thisBuild.version, scope)
	}

	// The supertag write, with B's listener held: INFRA-1253's type migration, run again.
	hold.hold()
	runTypeMigrationsAgain(t, ctx, pair.database.Pool, []string{issueTreeBlockSchema})
	pair.a.waitForServedSchema(t, "process A installs the criterion type", func(served pmdoc.BlockSchema) bool {
		return served.Version == bumps.criterion && servedType(served, "criterion")
	})
	deadline := time.Now().Add(staleSchemaWait)
	for {
		live, err := processes.Live(ctx, pair.database.Pool)
		if err != nil {
			t.Fatalf("read the live server processes: %v", err)
		}
		if slices.ContainsFunc(live, func(process processes.Process) bool { return process.SchemaVersion == bumps.criterion }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process A never registered version %d after it installed it: %+v", bumps.criterion, live)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, page := range []struct {
		name string
		peer *docstest.Peer
	}{{"the editor of the decision types", editor}, {"the page of this build", built}} {
		if code := awaitClose(t, page.peer, page.name+" on process A after its switch"); code != contracts.DocumentSchemaCloseCode {
			t.Errorf("process A ended the socket of %s with %d after it installed version %d, want %d, the close the page reads as the schema moving", page.name, code, bumps.criterion, contracts.DocumentSchemaCloseCode)
		}
	}
	if served := pair.b.servedSchema(t); served.Version != bumps.decision || servedType(served, "criterion") {
		t.Fatalf("process B, whose listener is held, serves version %d with the types %v, want the version before the bump, %d: the hold kept nothing back",
			served.Version, typeNames(served), bumps.decision)
	}

	// refusedOn drives the editor of the decision types, with the copy its browser kept, to
	// process: it is admitted read-only, a page that cannot represent what a live process creates,
	// and its write of a criterion stores nothing. While some live process has not installed the
	// version - installing is what every stage but the restart holds off - a page of either build
	// that connects is admitted read-only too.
	stages := 0
	refusedOn := func(t *testing.T, process *dispatchProcess, stage string, uninstalled bool) {
		t.Helper()
		stages++
		if uninstalled {
			for _, page := range []schemaPage{decisions, thisBuild} {
				peer, scope := process.openSchemaPage(t, document, fragment, page, crdt.New())
				if scope != "readonly" {
					t.Errorf("%s: process %s admits a page of version %d/%d %s before every live process installed version %d, want read-only",
						stage, pair.name(process), page.version, page.creatable, scope, bumps.criterion)
				}
				peer.Close()
			}
		}
		peer, scope := process.openSchemaPage(t, document, fragment, decisions, editor.Doc)
		t.Logf("%s: process %s admits the editor of version %d %s", stage, pair.name(process), decisions.version, scope)
		before := storedUpdates(t, pair.database, document)
		writeCriterion(t, peer, "k"+strconv.Itoa(stages))
		if storedWithin(t, pair.database, document, before, 3*time.Second) {
			t.Errorf("%s: process %s accepted the open editor's write of a criterion, a type it does not hold: the room stored %d updates past the %d before it (admitted %s)",
				stage, pair.name(process), storedUpdates(t, pair.database, document)-before, before, scope)
		}
		peer.Close()
	}
	refusedOn(t, pair.b, "while process B's notify is held", true)

	// The second process opens its listener again, and its new LISTEN waits on the hold.
	listens := hold.cut()
	hold.awaitListen(t, listens, "process B opens its listener again")
	refusedOn(t, pair.b, "while process B reconnects its listener", true)

	// The second process restarts, with the hold still held.
	pair.b.Terminate(t)
	pair.b.WaitExit(t, "SIGTERM")
	pair.b = pair.startHeldProcess(t, hold)
	pair.b.waitHealthy(t)
	refusedOn(t, pair.b, "after process B restarts", false)

	if text := documentText(t, pair.a, document); strings.Contains(text, ":::criterion") {
		t.Errorf("after the refused writes the document reads\n%s\nwant no criterion stored", text)
	}

	// The hold released, both processes answer the new version, and only then is the page of this
	// build read connecting again.
	hold.release()
	pair.b.waitForServedSchema(t, "process B installs the criterion type once its notify is released", func(served pmdoc.BlockSchema) bool {
		return served.Version == bumps.criterion && servedType(served, "criterion")
	})
	awaitFleet(t, pair.database, 2, bumps.criterion, bumps.criterion, "every live process installs the new version")
	for index, process := range []*dispatchProcess{pair.a, pair.b} {
		peer, scope := process.openSchemaPage(t, document, fragment, thisBuild, built.Doc)
		if scope != "read-write" {
			t.Errorf("once every live process installed version %d, process %s admits the page of this build, which connected again, %s, want read-write: the switch leaves no open editor read-only",
				bumps.criterion, pair.name(process), scope)
			peer.Close()
			continue
		}
		before := storedUpdates(t, pair.database, document)
		writeCriterion(t, peer, "admitted"+strconv.Itoa(index))
		if !storedWithin(t, pair.database, document, before, 10*time.Second) {
			t.Errorf("once every live process installed version %d, process %s stored no row of the page's criterion", bumps.criterion, pair.name(process))
		}
		peer.Close()
	}
}
