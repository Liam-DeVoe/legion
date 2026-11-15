package docs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/encoding"
	ygsync "github.com/reearth/ygo/sync"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/docs/docstest"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// fleetProcess writes the server_processes row of another server process straight into the
// registry: one that reads schema version and creates up to creatable, whose last heartbeat is
// age before the database's now(). A version of 0 is the row of a build that predates the fleet
// columns.
func fleetProcess(t *testing.T, service *Service, version, creatable int, age time.Duration) {
	t.Helper()
	if _, err := service.store.Pool.Exec(context.Background(), `
		insert into server_processes (instance, signing_keys, schema_version, schema_creatable, heartbeat_at)
		values (gen_random_uuid(), '{fingerprint}', $1, $2, now() - make_interval(secs => $3))`,
		version, creatable, age.Seconds()); err != nil {
		t.Fatalf("add a server process reading schema version %d: %v", version, err)
	}
}

// page is the query a page presents on its document socket: schema_version, and schema_creatable
// unless creatable is negative, as a page that sends no schema_creatable does.
func page(version, creatable int) url.Values {
	query := url.Values{"schema_version": {strconv.Itoa(version)}}
	if creatable >= 0 {
		query.Set("schema_creatable", strconv.Itoa(creatable))
	}
	return query
}

// dialPage opens a document socket to artifactID's room as alice's page presenting query.
func dialPage(serverURL, artifactID string, query url.Values) (*gws.Conn, *http.Response, error) {
	address := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws/doc/" + artifactID + "?" + query.Encode()
	return gws.DefaultDialer.Dial(address, http.Header{"X-Dispatch-User": []string{"alice"}})
}

// mustDialPage is dialPage for a socket that must be accepted. It closes when the test ends.
func mustDialPage(t *testing.T, serverURL, artifactID string, query url.Values) *gws.Conn {
	t.Helper()
	connection, response, err := dialPage(serverURL, artifactID, query)
	if err != nil {
		t.Fatalf("connect a page presenting %v: response=%#v err=%v", query, response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

// sendProbe sends an update the room does not hold: a key of its own in a map no document uses.
// The room answers it with a SyncStatus, 1 when it applied it and 0 when the socket is read-only.
func sendProbe(t *testing.T, connection *gws.Conn, artifactID string) {
	t.Helper()
	doc := crdt.New()
	var update []byte
	doc.OnUpdate(func(u []byte, _ any) { update = append([]byte(nil), u...) })
	probe := doc.GetMap("admission-probe")
	doc.Transact(func(txn *crdt.Transaction) { probe.Set(txn, "probe", "value") })
	frame := encoding.EncodeBytes(func(encoder *encoding.Encoder) {
		encoder.WriteVarString(artifactID)
		encoder.WriteVarUint(0) // Hocuspocus sync message.
	})
	if err := connection.WriteMessage(gws.BinaryMessage, append(frame, ygsync.EncodeUpdate(update)...)); err != nil {
		t.Fatalf("send the probe update: %v", err)
	}
}

// sendToken sends token in a Hocuspocus authentication message, as the provider does once its
// socket opens.
func sendToken(t *testing.T, connection *gws.Conn, artifactID, token string) {
	t.Helper()
	frame := encoding.EncodeBytes(func(encoder *encoding.Encoder) {
		encoder.WriteVarString(artifactID)
		encoder.WriteVarUint(2) // Hocuspocus authentication message.
		encoder.WriteVarUint(0) // Token authentication payload.
		encoder.WriteVarString(token)
	})
	if err := connection.WriteMessage(gws.BinaryMessage, frame); err != nil {
		t.Fatalf("send the token: %v", err)
	}
}

// readUntil reads the room's messages, for up to 5 s, until it sends one of kind (Hocuspocus's
// message type), and returns the rest of that message.
func readUntil(t *testing.T, connection *gws.Conn, kind uint64) *encoding.Decoder {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = connection.SetReadDeadline(time.Time{}) }()
	for {
		_, message, err := connection.ReadMessage()
		if err != nil {
			t.Fatalf("read the room's messages for one of type %d: %v", kind, err)
		}
		decoder := encoding.NewDecoder(message)
		if _, err := decoder.ReadVarString(); err != nil {
			t.Fatalf("read the Hocuspocus document name: %v", err)
		}
		got, err := decoder.ReadVarUint()
		if err != nil {
			t.Fatalf("read the Hocuspocus message type: %v", err)
		}
		if got == kind {
			return decoder
		}
	}
}

// sendSyncStep1 asks the room for its whole document, the sync step 1 a provider sends as its
// socket opens.
func sendSyncStep1(t *testing.T, connection *gws.Conn, artifactID string) {
	t.Helper()
	frame := encoding.EncodeBytes(func(encoder *encoding.Encoder) {
		encoder.WriteVarString(artifactID)
		encoder.WriteVarUint(0) // Hocuspocus sync message.
	})
	if err := connection.WriteMessage(gws.BinaryMessage, append(frame, ygsync.EncodeSyncStep1(crdt.New())...)); err != nil {
		t.Fatalf("ask the room for the document: %v", err)
	}
}

// probeWritable sends a probe update and reports whether the room applied it.
func probeWritable(t *testing.T, connection *gws.Conn, artifactID string) bool {
	t.Helper()
	sendProbe(t, connection, artifactID)
	flag, err := readUntil(t, connection, 8).ReadVarUint() // Hocuspocus SyncStatus.
	if err != nil {
		t.Fatalf("read the SyncStatus: %v", err)
	}
	return flag == 1
}

// tokenWritable sends token and reports whether the room authenticated the socket read-write.
func tokenWritable(t *testing.T, connection *gws.Conn, artifactID, token string) bool {
	t.Helper()
	sendToken(t, connection, artifactID, token)
	decoder := readUntil(t, connection, 2) // Hocuspocus authentication.
	subtype, err := decoder.ReadVarUint()
	if err != nil {
		t.Fatalf("read the authentication subtype: %v", err)
	}
	scope, err := decoder.ReadVarString()
	if err != nil || subtype != 2 {
		t.Fatalf("authentication = subtype %d scope %q (%v), want Authenticated", subtype, scope, err)
	}
	return scope == "read-write"
}

// admission is what a page presenting query, and token once its socket opens, is admitted as: by
// the socket's acceptance, which the room's answer to an update sent before the token shows, and
// by the token's answer.
func admission(t *testing.T, serverURL, artifactID string, query url.Values, token string) (socket, authenticated bool) {
	t.Helper()
	connection := mustDialPage(t, serverURL, artifactID, query)
	socket = probeWritable(t, connection, artifactID)
	authenticated = tokenWritable(t, connection, artifactID, token)
	_ = connection.Close()
	return socket, authenticated
}

// newAdmissionServer is a document service on its own database, with a document on an open issue,
// served over HTTP. Its process stages one typed block, "future", read from one version above the
// shipped manifest and not creatable (UseStagedBlockForTest), so the process reads V one above
// the shipped version and creates up to R, the shipped version: the cases below need a process
// that creates less than it reads. With future, the document holds a "future" block too, so the
// highest since it holds is that V.
//
// The order the two manifests are staged in is what keeps those swaps off a running service.
// pmdoc's readers do not lock the manifest (pmdoc.UseManifestForTest), so none may be installed
// while a service's room, settlement or flush goroutines read it: each is a data race, and one of
// them took the race lane red (a settlement's renderTreeWithOffsets against a restore's install).
// So the document is seeded by a service of its own, under the creatable manifest, and that
// service is shut down - its settlement and flush goroutines with it - before the uncreatable
// manifest is staged; the serving service is built after that staging, and its shutdown is
// registered after both restores. Cleanups run last in first out, so they run the server's close,
// that shutdown, then the two restores, each with no service left to read the manifest.
func newAdmissionServer(t *testing.T, future bool) (*Service, string, *httptest.Server) {
	t.Helper()
	restoreCreatable, err := pmdoc.UseStagedBlockForTest("future", true)
	if err != nil {
		t.Fatalf("stage the future block as creatable: %v", err)
	}
	t.Cleanup(restoreCreatable)
	markdown := "# First\n"
	if future {
		markdown += "\n:::future{#future-1}\nBody.\n:::\n"
	}
	database := storetest.Open(t)
	artifactID := createDocument(t, database, "# First")
	seeding := New(testServiceDeps(database))
	seedServiceText(t, seeding, artifactID, markdown)
	if err := seeding.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown the seeding document service: %v", err)
	}
	restoreUncreatable, err := pmdoc.UseStagedBlockForTest("future", false)
	if err != nil {
		t.Fatalf("stage the future block as not creatable: %v", err)
	}
	t.Cleanup(restoreUncreatable)
	service := newTestServiceOn(t, database)
	server := httptest.NewServer(http.HandlerFunc(service.ServeHTTP))
	t.Cleanup(server.Close)
	return service, artifactID, server
}

// A page is writable on an open issue when every live server process reads what the page
// creates (its schema_creatable is at most each process's schema version), some live process
// creates what the page creates (its schema_creatable is at most the highest creatable version),
// and the page reads what every live process creates and what the document holds (its
// schema_version is at least each process's creatable version and the highest since in the
// document). This process counts as one of them, and so does a process of a build that predates
// the fleet columns: its row holds no schema numbers, so it counts as reading version 0, which
// no page's schema_creatable is at or under, and every page is read-only while it lives. A
// process whose heartbeat lapsed counts for nothing. So after a rollback, when no live process
// creates what a newer page creates, that page is read-only and an older page stays writable. A
// page that sends no schema_creatable is a page from before the manifest, every element of which
// has since 1, so it creates up to 1. The socket's acceptance and its token's answer agree,
// since the provider takes the second as the socket's scope; the token is the page's
// schema_version alone, as the provider sends it.
//
// The cases are written in the shipped manifest's numbers. The process they run against stages
// one element it reads and cannot create (newAdmissionServer), so it reads current, one version
// above the shipped version, and creates up to creates, the shipped creatable version. A page one
// version older is the shipped build's page, and a live process of the build after this one reads
// one version above current and creates up to current.
func TestSchemaAdmissionFollowsTheLiveFleet(t *testing.T) {
	v, r := pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()
	if r < 1 || r > v {
		t.Fatalf("this build reads schema version %d and creates up to %d; the cases below need it to create at least 1, what a page from before the manifest creates, and at most what it reads, so a page one version older than the process they run against, which reads %d and creates up to %d (newAdmissionServer), represents everything that process creates",
			v, r, v+1, r)
	}
	type process struct {
		version, creatable int
		age                time.Duration
	}
	current, creates := v+1, r
	older, newer := current-1, current+1
	nextBuild := process{version: newer, creatable: current}
	lapsedNextBuild := process{version: newer, creatable: current, age: time.Hour}
	firstVersion := process{version: 1, creatable: 1}
	predatesFleetColumns := process{}
	for _, c := range []struct {
		name     string
		future   bool
		fleet    []process
		query    url.Values
		writable bool
	}{
		{name: "a page one version newer beside a live process of its build", fleet: []process{nextBuild}, query: page(newer, current), writable: true},
		{name: "after a rollback, a page one version newer creating what no live process creates", query: page(newer, current)},
		{name: "a page creating above every live process's schema version", query: page(newer, newer)},
		{name: "a page one version newer that creates only what every live process reads", query: page(newer, creates), writable: true},
		{name: "after a rollback, a page one version older that represents everything the fleet creates", query: page(older, creates), writable: true},
		{name: "a page that cannot represent an element the document holds", future: true, query: page(older, creates)},
		{name: "a page of this version beside the element it represents", future: true, query: page(current, creates), writable: true},
		{name: "a live process creating what an older page cannot represent", fleet: []process{nextBuild}, query: page(older, creates)},
		{name: "a live process that cannot read what a newer page creates", fleet: []process{firstVersion}, query: page(newer, current)},
		{name: "a process that predates the fleet columns reads version 0, which no page creates at or under", fleet: []process{predatesFleetColumns}, query: page(current, creates)},
		{name: "a page from before the manifest beside a process that predates the fleet columns", fleet: []process{predatesFleetColumns}, query: page(current, -1)},
		{name: "a page claiming to create nothing beside a process that predates the fleet columns", fleet: []process{predatesFleetColumns}, query: page(current, 0)},
		{name: "a page claiming a creatable version below zero", query: url.Values{"schema_version": {strconv.Itoa(current)}, "schema_creatable": {"-2"}}},
		{name: "a lapsed process is left out", fleet: []process{lapsedNextBuild}, query: page(older, creates), writable: true},
		{name: "a page that sends no schema_creatable on this version", query: page(current, -1), writable: true},
		{name: "a page that sends no schema_creatable one version newer creates what the first version reads", query: page(newer, -1), writable: true},
		{name: "a page that sends no schema_creatable beside a live process that reads only the first version", fleet: []process{firstVersion}, query: page(current, -1), writable: true},
		{name: "a page presenting no number", query: url.Values{"schema_version": {"two"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			service, artifactID, server := newAdmissionServer(t, c.future)
			for _, p := range c.fleet {
				fleetProcess(t, service, p.version, p.creatable, p.age)
			}
			token := c.query.Get("schema_version")
			socket, authenticated := admission(t, server.URL, artifactID, c.query, token)
			if socket != c.writable || authenticated != c.writable {
				t.Errorf("a page presenting %v and token %q is admitted writable=%t by its socket and writable=%t by its token, want %t for both",
					c.query, token, socket, authenticated, c.writable)
			}
		})
	}
}

// A closed issue's document is read-only to every page, by the socket and by its token alike.
func TestSchemaAdmissionOfAClosedIssueIsReadOnly(t *testing.T) {
	service, artifactID, server := newAdmissionServer(t, false)
	if _, err := service.store.Pool.Exec(context.Background(), `update issues set closed_at = now() where key = 'DOC-1'`); err != nil {
		t.Fatalf("close the document's issue: %v", err)
	}
	if socket, authenticated := admission(t, server.URL, artifactID, page(pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()), strconv.Itoa(pmdoc.SchemaVersion())); socket || authenticated {
		t.Errorf("a current page on a closed issue is admitted writable=%t by its socket and %t by its token, want read-only", socket, authenticated)
	}
}

// The provider authenticates with its schema version alone, so a token names the page that
// presented it and never the socket that sent it: the answer is the admission that socket's own
// request context carries (authorizeSchemaVersion). A context carrying no admission is read-only;
// a socket's own token is answered with that socket's admission, as often as it asks; and a token
// its page never presented is read-only.
func TestATokenIsAnsweredFromTheAdmissionItsSocketCarries(t *testing.T) {
	service, artifactID, server := newAdmissionServer(t, false)
	version := strconv.Itoa(pmdoc.SchemaVersion())
	if config, err := service.authorizeSchemaVersion(context.Background(), artifactID, version); err != nil || !config.ReadOnly {
		t.Errorf("a token on a context carrying no admission = %#v, %v; want read-only", config, err)
	}

	connection := mustDialPage(t, server.URL, artifactID, page(pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()))
	if !tokenWritable(t, connection, artifactID, version) {
		t.Fatal("a current page's first token was answered read-only, want read-write")
	}
	if !tokenWritable(t, connection, artifactID, version) {
		t.Error("the same socket's token, sent again, was answered read-only; its admission has not changed")
	}
	other := mustDialPage(t, server.URL, artifactID, page(pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()))
	if tokenWritable(t, other, artifactID, "1") {
		t.Error("a token naming a version the socket's page never presented was answered read-write, want read-only")
	}
}

// Every live window each process checks its writable document sockets against the fleet again.
// When a process that creates what an older page cannot represent joins, the older page's socket
// is closed with the admission-revoked code, on which the provider reconnects, and its reconnect
// is read-only; a current page's socket stays open and writable. This process reads version and
// creates up to creatable (newAdmissionServer), so the older page is one version below it, and
// the process that joins is of the build after this one: it reads one version above version and
// creates up to version.
func TestAdmissionRecheckClosesASocketTheFleetNoLongerAdmits(t *testing.T) {
	service, artifactID, server := newAdmissionServer(t, false)
	version, creatable := pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()
	olderPage := page(version-1, creatable)
	older := mustDialPage(t, server.URL, artifactID, olderPage)
	if !tokenWritable(t, older, artifactID, olderPage.Get("schema_version")) {
		t.Fatal("an older page that represents everything the fleet creates was authenticated read-only")
	}
	current := mustDialPage(t, server.URL, artifactID, page(version, creatable))
	if !tokenWritable(t, current, artifactID, strconv.Itoa(version)) {
		t.Fatal("a current page was authenticated read-only")
	}

	fleetProcess(t, service, version+1, version, 0)
	if err := service.recheckSchemaAdmissions(context.Background()); err != nil {
		t.Fatalf("re-check the sockets' admission: %v", err)
	}

	requireRevokedClose(t, older, "the older page's socket")
	if socket, authenticated := admission(t, server.URL, artifactID, olderPage, olderPage.Get("schema_version")); socket || authenticated {
		t.Errorf("the older page's reconnect was admitted writable=%t by its socket and %t by its token, want read-only", socket, authenticated)
	}
	if !probeWritable(t, current, artifactID) {
		t.Error("the current page's socket no longer applies its updates after the re-check")
	}
}

// After a rollback no live process creates what a newer page creates, so the next re-check closes
// that page's socket with the admission-revoked code, and its reconnect is read-only. A current
// page's socket stays open and writable. This process reads version and creates up to creatable
// (newAdmissionServer), and the newer page is of the build after this one, beside a live process
// of that build: both read one version above version and create up to version.
func TestAdmissionRecheckClosesANewerPageOnceNoLiveProcessCreatesWhatItCreates(t *testing.T) {
	service, artifactID, server := newAdmissionServer(t, false)
	version, creatable := pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()
	fleetProcess(t, service, version+1, version, 0)
	newerPage := page(version+1, version)
	newer := mustDialPage(t, server.URL, artifactID, newerPage)
	if !tokenWritable(t, newer, artifactID, newerPage.Get("schema_version")) {
		t.Fatal("a newer page beside a live process of its build was authenticated read-only")
	}
	current := mustDialPage(t, server.URL, artifactID, page(version, creatable))
	if !tokenWritable(t, current, artifactID, strconv.Itoa(version)) {
		t.Fatal("a current page was authenticated read-only")
	}

	// The rollback stops the newer build's process.
	if _, err := service.store.Pool.Exec(context.Background(), `delete from server_processes`); err != nil {
		t.Fatalf("stop the newer build's process: %v", err)
	}
	if err := service.recheckSchemaAdmissions(context.Background()); err != nil {
		t.Fatalf("re-check the sockets' admission: %v", err)
	}

	requireRevokedClose(t, newer, "the newer page's socket")
	if socket, authenticated := admission(t, server.URL, artifactID, newerPage, newerPage.Get("schema_version")); socket || authenticated {
		t.Errorf("the newer page's reconnect was admitted writable=%t by its socket and %t by its token, want read-only", socket, authenticated)
	}
	if !probeWritable(t, current, artifactID) {
		t.Error("the current page's socket no longer applies its updates after the re-check")
	}
}

// requireRevokedClose reads connection until it ends, and fails the test unless the server closed
// it with the admission-revoked code within five seconds.
func requireRevokedClose(t *testing.T, connection *gws.Conn, what string) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := connection.ReadMessage()
		if err == nil {
			continue
		}
		var closed *gws.CloseError
		if !errors.As(err, &closed) || closed.Code != contracts.DocumentAdmissionRevokedCloseCode {
			t.Fatalf("%s ended with %v, want a close with %d", what, err, contracts.DocumentAdmissionRevokedCloseCode)
		}
		return
	}
}

// paragraphMarkdown is a document of paragraphs paragraphs, each of 161 characters whose every
// word is "document ", the word an edit replaces to tell its own text from the seed's.
func paragraphMarkdown(paragraphs int) string {
	var markdown strings.Builder
	markdown.WriteString("# First\n")
	for paragraph := range paragraphs {
		fmt.Fprintf(&markdown, "\nParagraph %d: %s\n", paragraph, strings.Repeat("document ", 16))
	}
	return markdown.String()
}

// syncMessageBytesPerParagraph is how many bytes one of paragraphMarkdown's paragraphs adds to
// what a room sends a page that asks for the whole document: its 161 characters and the block the
// document's tree holds them in. Measured at 257 on documents of 200 to 1,600 paragraphs, whose
// sync messages ran from 51 KB to 407 KB, before ygo cut that answer into frames of at most 64 KiB
// (documentFrameBytes), which adds a few bytes of header a frame. It is used only to size a
// document to a socket's buffers; the tests below log what the server really wrote, so a build
// that encodes a paragraph differently shows it there.
const syncMessageBytesPerParagraph = 257

// documentFrameBytes is the most one frame of a room's answer to a page's sync step 1 carries:
// ygo sends the document as sync Updates of at most this many bytes a frame, each integrating on
// its own, and then the SyncStep2 that ends the answer (sjawhar/ygo provider/websocket/peer.go,
// stateFrameBytes and sendSyncSeries).
const documentFrameBytes = 64 << 10

// sizedMarkdown is a document the room's answer to a page's sync step 1 carries in about size
// bytes.
func sizedMarkdown(size int) string {
	return paragraphMarkdown(max(size/syncMessageBytesPerParagraph, 1))
}

// receiveBufferControl is a net.Dialer's Control that asks for request as the socket's SO_RCVBUF
// before it connects, so the receive window the connection opens with is the one that buffer
// allows.
func receiveBufferControl(request int) func(network, address string, raw syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var setErr error
		if err := raw.Control(func(fd uintptr) {
			setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, request)
		}); err != nil {
			return err
		}
		return setErr
	}
}

// receiveBufferGrant is the receive buffer the kernel grants a page that asks for request before it
// connects (receiveBufferControl), measured on a loopback connection of this test's own before
// the document is seeded, as sendBufferGrant measures the server's send buffer.
func receiveBufferGrant(t *testing.T, request int) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen to measure the receive buffer the kernel grants: %v", err)
	}
	defer listener.Close()
	dialed, err := (&net.Dialer{Control: receiveBufferControl(request)}).Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("connect asking for a receive buffer of %d bytes: %v", request, err)
	}
	defer dialed.Close()
	tcp, ok := dialed.(*net.TCPConn)
	if !ok {
		t.Fatalf("the loopback dial made a %T, want a TCP connection whose receive buffer this can read", dialed)
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		t.Fatalf("reach the loopback connection's socket: %v", err)
	}
	var granted int
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		granted, readErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil || readErr != nil {
		t.Fatalf("read back the receive buffer of %d bytes asked for: %v %v", request, err, readErr)
	}
	return granted
}

// dialUnreadPage is dialPage on a connection that asks for receiveBuffer as its SO_RCVBUF before
// it connects (receiveBufferControl), which bounds what the page's kernel takes off the wire while
// the page reads nothing, so the server's write of a document bigger than its buffers stops part
// way. The socket must be accepted, and closes when the test ends.
func dialUnreadPage(t *testing.T, serverURL, artifactID string, query url.Values, receiveBuffer int) *gws.Conn {
	t.Helper()
	dialer := gws.Dialer{NetDialContext: (&net.Dialer{Control: receiveBufferControl(receiveBuffer)}).DialContext}
	address := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws/doc/" + artifactID + "?" + query.Encode()
	connection, response, err := dialer.Dial(address, http.Header{"X-Dispatch-User": []string{"alice"}})
	if err != nil {
		t.Fatalf("connect a page presenting %v with a receive buffer of %d bytes: response=%#v err=%v", query, receiveBuffer, response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	// ygo sends a joining peer the room's state only in answer to that peer's own sync step 1
	// (sjawhar/ygo v1.55.1-sami.1, provider/websocket/server.go, reearth/ygo#342), so a page that
	// asks for nothing is owed nothing and the writes below would never fill its buffer. The
	// token follows it, as a provider sends it once its socket opens, so the room's answer to it
	// is queued behind the document: a frame after the answer's SyncStep2 says the server has
	// moved on from writing the document (waitForTheDocumentOnTheWire).
	sendSyncStep1(t, connection, artifactID)
	sendToken(t, connection, artifactID, query.Get("schema_version"))
	return connection
}

// inFlightPageReceiveBuffer is the SO_RCVBUF the page of the test below asks for before it
// connects. Its grant bounds what that page's kernel takes while the page reads nothing, so the
// document can be sized past it and the server's send buffer together (inFlightDocumentSize). It
// is also far past the slow page's 4096 bytes on purpose: once the page reads, the frame in flight
// has to go out within ygo's one-second wait for it, and a 4096-byte buffer drained a revoked
// socket at about 41 KB/s on a Linux runner
// (docs/solutions/testing/never-time-a-clients-read-of-a-socket-the-server-closed-assert-on-the-bytes-the-server-wrote-pr2461.md),
// while a grant of four of loopback's 64 KiB segments leaves the window room for whole segments.
const inFlightPageReceiveBuffer = 128 << 10

// inFlightDocumentSize is how big the test below makes the document, given the send buffer the
// kernel grants the server's socket and the receive buffer it grants the page's: past what the two
// hold together by four of ygo's frames, so that whatever the kernel's accounting the server's
// writer stops inside the room's answer with frames of the document still to write.
func inFlightDocumentSize(sendGrant, receiveGrant int) int {
	return sendGrant + receiveGrant + 4*documentFrameBytes
}

// writeStalledAfter is how long a write to a page that reads nothing has to have run, without
// returning, for the test to call the server's writer stalled on it: on loopback the kernel copies
// a frame in microseconds while it has room, so a write this old is one it has no room for.
const writeStalledAfter = 100 * time.Millisecond

// documentStall is the server's writer stopped inside the document it owes a page that reads
// nothing: write is the index, in the socket's record, of the write that has not returned, since
// when it began, before is what the writes ahead of it put on the wire, and took is how long
// after the dial the stall was seen.
type documentStall struct {
	write  int
	since  time.Time
	before serverStream
	took   time.Duration
}

// revokeInFlightBound is how soon after the write in flight began the test below must take the
// revoke: a quarter of ygoPeerWriteTimeout, the deadline ygo set on that write, so an end the test
// accepts is the revoke's work and not that deadline's.
const revokeInFlightBound = ygoPeerWriteTimeout / 4

// revokedPageReadsAfter is how long the page of the test below goes on reading nothing once the
// revoke is taken: a quarter of ygoCloseFrameTimeout, so a close that does not wait for the frame
// in flight finds it stalled and cuts it, while three quarters of ygo's wait remain for the page to
// take that frame once it reads.
const revokedPageReadsAfter = ygoCloseFrameTimeout / 4

// waitForAStalledDocumentWrite waits until the server's writer to socket has stopped inside the
// room's answer to the page's sync step 1: at least one frame of the document out whole, not the
// SyncStep2 that ends the answer, and a write that has run writeStalledAfter without returning.
// Loading the room and writing the document to the page is the room's own work, so it fails once
// roomWait has passed since dialed; no deadline of ygo's runs from the dial, since the page's
// sync step 1 is the first read ygo's handshake deadline waits for.
func waitForAStalledDocumentWrite(t *testing.T, socket *serverSocket, dialed time.Time) documentStall {
	t.Helper()
	for {
		writes, _ := socket.record()
		last := len(writes) - 1
		read, err := readServerStream(streamOf(writes))
		if last >= 0 && writes[last].returnedAt.IsZero() && time.Since(writes[last].startedAt) >= writeStalledAfter &&
			err == nil && read.documentFrames > 0 && !read.answered {
			return documentStall{write: last, since: writes[last].startedAt, before: read, took: time.Since(dialed)}
		}
		if took := time.Since(dialed); took > roomWait(t) {
			t.Fatalf("%v after the dial the server's writer had not stalled inside the document it owes the page: %d writes, the last of them returned=%t, %d whole frames of the document, its SyncStep2 among them=%t, reading the stream gave %v. This test needs a frame of the document in flight when the revoke arrives",
				took, len(writes), last >= 0 && !writes[last].returnedAt.IsZero(), read.documentFrames, read.answered, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A revoke ends the socket with a close frame of ygo's own, which gorilla's WriteControl puts on
// the connection between two of ygo's frames: it waits up to a second for the frame already going
// out and never cuts into one (cmd/dispatch/README.md, "Two builds at once"). ygo sends a page the
// document in frames of at most 64 KiB, so here the document is sized past what the server's send
// buffer and the page's receive buffer hold together, and the page reads nothing until the
// re-check has revoked its socket: the server's writer stops inside a frame of the document, and
// the revoke arrives while that frame is going out. The page goes on reading nothing for a quarter
// of ygo's one-second wait, so the revoke has that frame stalled in front of it, and then reads:
// every frame whole, that one included, and the close with the admission-revoked code after them -
// never a protocol error from a frame cut in half, and never the bare 1006 of a connection closed
// without a frame. The socket's record shows the revoke did arrive while that frame was going out:
// the write carrying it began before the revoke and returned after it.
func TestARevokeWaitsForTheMessageTheServerHasInFlight(t *testing.T) {
	// Both buffers are read back before anything is seeded, and the document is sized from them.
	sendGrant := sendBufferGrant(t)
	receiveGrant := receiveBufferGrant(t, inFlightPageReceiveBuffer)
	size := inFlightDocumentSize(sendGrant, receiveGrant)
	t.Logf("the kernel grants the server a send buffer of %d bytes and the page a receive buffer of %d, so the page is owed a document of about %d bytes",
		sendGrant, receiveGrant, size)
	// The process stages one element it reads and cannot create, so it reads version, creates up
	// to creatable, less than it reads, and a page one version older is still writable
	// (newAdmissionServer).
	restore, err := pmdoc.UseStagedBlockForTest("future", false)
	if err != nil {
		t.Fatalf("stage the future block as not creatable: %v", err)
	}
	t.Cleanup(restore)
	version, creatable := pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()
	service, artifactID := newTestService(t)
	seedServiceText(t, service, artifactID, sizedMarkdown(size))
	// The server accepts through a listener that gives every socket that send buffer and records
	// what the server writes to it (recordingListener).
	server := httptest.NewUnstartedServer(http.HandlerFunc(service.ServeHTTP))
	accepted := &recordingListener{Listener: server.Listener}
	server.Listener = accepted
	server.Start()
	t.Cleanup(server.Close)

	// A page one version older represents everything this fleet creates, so its socket is
	// admitted writable; it reads nothing, so the server's writer stops inside the document.
	dialed := time.Now()
	reader := dialUnreadPage(t, server.URL, artifactID, page(version-1, creatable), inFlightPageReceiveBuffer)
	serverConn := accepted.find(reader.LocalAddr().String())
	if serverConn == nil {
		t.Fatalf("no accepted connection has the page's local address %s, so nothing records what the server writes to it", reader.LocalAddr())
	}
	stall := waitForAStalledDocumentWrite(t, serverConn, dialed)

	// A process of the build after this one, which creates up to version, what this page cannot
	// represent, joins, and the re-check revokes it.
	fleetProcess(t, service, version+1, version, 0)
	revokedAt := time.Now()
	if err := service.recheckSchemaAdmissions(context.Background()); err != nil {
		t.Fatalf("re-check the sockets' admission: %v", err)
	}
	if took := revokedAt.Sub(stall.since); took > revokeInFlightBound {
		t.Fatalf("the revoke came %v after the server began the write in flight, past %v: a run this slow cannot tell the revoke's close from ygo's own %v deadline on that write",
			took, revokeInFlightBound, ygoPeerWriteTimeout)
	}
	// The page reads nothing a while longer, so the frame stays stalled with the revoke waiting on
	// it: a close that did not wait for it would cut it now.
	time.Sleep(revokedPageReadsAfter)

	documentFrames := 0
	_ = reader.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, message, err := reader.ReadMessage()
		if err != nil {
			var closed *gws.CloseError
			if !errors.As(err, &closed) || closed.Code != contracts.DocumentAdmissionRevokedCloseCode {
				t.Fatalf("the revoked page's socket ended with %v after %d whole frames of the document, want a close with %d once the frame in flight at the revoke is out, which ygo waits up to %v for and the page began taking %v after the revoke",
					err, documentFrames, contracts.DocumentAdmissionRevokedCloseCode, ygoCloseFrameTimeout, revokedPageReadsAfter)
			}
			break
		}
		if document, _ := documentFrame(message); document {
			documentFrames++
		}
	}
	writes, _ := serverConn.record()
	if inFlight := writes[stall.write]; !inFlight.returnedAt.After(revokedAt) {
		t.Fatalf("the write the server had in flight on the page's socket, stalled %v after the dial, returned=%t at %v, not after the revoke at %v: the revoke came between two frames, and this test needs it to arrive while one is going out",
			stall.took, !inFlight.returnedAt.IsZero(), inFlight.returnedAt, revokedAt)
	}
	if want := stall.before.documentFrames + 1; documentFrames < want {
		t.Fatalf("the page read %d whole frames of the document before the close, want at least %d: the %d the server had written before the revoke and the one it had in flight",
			documentFrames, want, stall.before.documentFrames)
	}
	t.Logf("the server's writer stalled %v after the dial and the revoke came %v after the write in flight began, with %d frames of the document written whole and %d bytes of the next recorded on the wire; the page read %d frames of the document whole, then the admission-revoked close",
		stall.took, revokedAt.Sub(stall.since), stall.before.documentFrames, stall.before.cutHeld, documentFrames)
}

// ygoHandshakeTimeout is the deadline ygo's ServeHTTP sets on a new socket's first read, after
// which it ends a connection whose page has sent nothing of its own; Dispatch leaves it at ygo's
// default (sjawhar/ygo provider/websocket/server.go, defaultHandshakeTimeout and the
// SetReadDeadline ServeHTTP sets before the first message it reads). The slow page below sends
// nothing, so this deadline ends its socket by itself 30 s after it connects, revoke or no
// revoke.
const ygoHandshakeTimeout = 30 * time.Second

// ygoPeerWriteTimeout is the deadline ygo sets on each write to a peer, after which it gives up
// on that write and closes the peer (sjawhar/ygo provider/websocket/server.go, writeTimeout, set
// by peer.writeToConn). The slow page below reads nothing, so the server's write of the document
// to it is stalled from the moment it connects and this deadline runs from there.
const ygoPeerWriteTimeout = 10 * time.Second

// ygoCloseFrameTimeout is how long ygo waits for a revoked socket's close frame to go out before
// it closes the connection without it (sjawhar/ygo provider/websocket/server.go,
// closeFrameTimeout, the deadline on ServeHTTP's WriteControl). The slow page below reads
// nothing, so that wait runs out in full and the close follows it.
const ygoCloseFrameTimeout = time.Second

// revokeAfterDialBound is how soon after the slow page connects the revoke must be taken, a
// quarter of the earlier of the two deadlines ygo would end that socket on by itself, and
// revokedServerEndBound is how long after the revoke the server's side of that socket must end:
// ygo's wait for the close frame, and a second and a half for a loaded runner to then close the
// connection and return from ServeHTTP, which is work of a few milliseconds. Together they put
// the server's end at most 5 s after the page connected, half of ygoPeerWriteTimeout and a sixth
// of ygoHandshakeTimeout, so an end this test accepts is the revoke's work and neither of theirs.
//
// Both are bounds on the server's own side of the socket, which is all the revoke answers for.
// Nothing bounds, or waits for, what the page then takes off its end: the test below says why.
const (
	revokeAfterDialBound  = min(ygoPeerWriteTimeout, ygoHandshakeTimeout) / 4
	revokedServerEndBound = ygoCloseFrameTimeout + 1500*time.Millisecond
)

// postRevokeMarker is the word the edit made straight after the revoke writes into every
// paragraph, so that edit is told apart from the seeded text in both places it matters: the bytes
// the server writes to the revoked socket, where it must never appear, and the document the
// page's reconnect syncs, where it must.
const postRevokeMarker = "afterrevoke"

// A page on a slow link takes nothing from its socket for longer than the revoked close waits, so
// it reads the connection's end as 1006 rather than 4412 and loses nothing by it; the rule is in
// packages/envoy/cmd/dispatch/README.md, "Two builds at once". This pins what the revoke promises
// a page owed a document far bigger than its buffers, on real sockets, and nothing else. The
// server accepts every socket with the send buffer serverSendBufferRequest asks for, and the
// document is seeded at a size that grant holds whole (slowPageDocumentSize): past 64 KB, so ygo
// sends it in several of its frames of at most 64 KiB (documentFrameBytes), and hundreds of times
// what the slow page's 4096-byte receive buffer takes. That page connects and never reads a byte,
// so when the re-check revokes its socket the whole document is queued on the wire to a page that
// has taken none of it, and the server's writes of it have returned, so every byte of it is in the
// record this test reads. Of the revoke alone, this requires:
//
//   - the re-check returns that page's admission and no other, and the revoke is taken within
//     revokeAfterDialBound of the dial;
//   - the server closes its side of that socket, and its ServeHTTP returns, within
//     revokedServerEndBound of the revoke;
//   - no document byte reaches that socket after the revoke: one more edit goes into the room
//     straight after it, and no write that began after the revoke carries a byte that is not the
//     admission-revoked close frame, nor does any write carry a byte once the socket is closed. A
//     write that began before the revoke and returned after it is the message already in flight,
//     which the close waits for;
//   - the bytes the server wrote to that socket are whole websocket frames throughout, and end
//     either on that close frame or at a cut the page reads as 1006 - never a frame cut in half
//     and resumed, never another close code, and never a byte after a close;
//   - that stream holds the whole document, past 64 KB in several frames up to the SyncStep2
//     that ends ygo's answer, so this is a page that really was owed more than its buffers could
//     take;
//   - the page's reconnect syncs the server's whole document, the edit made after the revoke
//     included.
//
// The end is read off the bytes the server wrote, never off the page's socket, and the page's own
// drain is never timed. The server closes the socket at once, so its kernel holds what it had
// already queued as an orphaned FIN-WAIT-1 socket, draining into a receive window the page's
// 4096-byte SO_RCVBUF clamped when it connected and no later SetReadBuffer reopens: a few
// kilobytes per zero-window probe, some 200 ms apart on a loaded runner, so a page owed a
// hundred kilobytes still needs many seconds to reach the FIN, and timing that would measure the
// runner's TCP pacing and nothing of the revoke. TCP delivers the server's bytes in order and
// then that FIN, so the stream checked here is exactly what the page reads.
func TestARevokedSlowPageEndsOnACodeItReconnectsOnAndSyncsWhole(t *testing.T) {
	// The document is cut to the send buffer this host's kernel grants, read back before anything
	// is seeded: the server's writes of it have to return with every byte queued, since a write
	// that blocked would have put nothing in the record below.
	grant := sendBufferGrant(t)
	size := slowPageDocumentSize(t, grant)
	markdown := sizedMarkdown(size)
	t.Logf("the kernel grants a send buffer of %d bytes, so the slow page is owed a document of about %d bytes", grant, size)
	// The process stages one element it reads and cannot create, so it reads version and creates
	// up to creatable, less than it reads, and the slow page, one version older, is admitted
	// writable (newAdmissionServer).
	restore, err := pmdoc.UseStagedBlockForTest("future", false)
	if err != nil {
		t.Fatalf("stage the future block as not creatable: %v", err)
	}
	t.Cleanup(restore)
	version, creatable := pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()
	slowPage := schemaPage{version: version - 1, creatable: creatable, valid: true}
	slowQuery := page(slowPage.version, slowPage.creatable)
	service, artifactID := newTestService(t)
	seedServiceText(t, service, artifactID, markdown)
	// The server accepts through a listener that hands out connections recording what the server
	// writes to them, so the slow page's own stream can be read below: httptest's Start takes
	// server.URL from this listener, and gorilla's Hijack hands ygo the very connection it
	// returned.
	server := httptest.NewUnstartedServer(http.HandlerFunc(service.ServeHTTP))
	accepted := &recordingListener{Listener: server.Listener}
	server.Listener = accepted
	server.Start()
	t.Cleanup(server.Close)

	// The writer presents this build's numbers, which the fleet below still admits writable, so
	// the re-check leaves its socket open. It stays connected to make the edit after the revoke.
	writer := connectBrowser(t, server.URL, artifactID, "bob")

	// The slow page connects last and reads nothing, so the whole document the room holds is owed
	// to it. ygo queues that document to a new peer at once (the frames of its answer to the
	// page's sync step 1, the last a SyncStep2), and the revoke is taken once that document is on
	// the wire and unread, not after a fixed wait: a run that revoked before it would be revoking a
	// socket the page was owed nothing on.
	dialed := time.Now()
	reader := dialUnreadPage(t, server.URL, artifactID, slowQuery, slowPageReceiveBuffer)
	serverConn := accepted.find(reader.LocalAddr().String())
	if serverConn == nil {
		t.Fatalf("no accepted connection has the slow page's local address %s, so nothing records what the server writes to it", reader.LocalAddr())
	}
	onTheWire := waitForTheDocumentOnTheWire(t, serverConn, dialed)
	// A process of the build after this one joins: it creates up to version, which the slow page
	// cannot represent, and reads one version above it.
	fleetProcess(t, service, version+1, version, 0)
	fleet, err := readSchemaFleet(context.Background(), service.store.Pool)
	if err != nil {
		t.Fatalf("read the live fleet: %v", err)
	}
	// The re-check's own revoke (recheckSchemaAdmissions), taken here so the admissions it closed
	// are read: this fleet still admits the writer's page, so the slow page's is the only socket
	// it closes, and the end below is that revoke's and no other's.
	revokedAt := time.Now()
	revoked := service.schemaAdmissions.revokeUnless(fleet)
	if len(revoked) != 1 {
		t.Fatalf("the re-check revoked %d sockets, want the slow page's alone", len(revoked))
	}
	if got := revoked[0]; got.key.room != artifactID || got.page != slowPage {
		t.Fatalf("the re-check revoked room %q presenting %+v, want room %q presenting %+v, the slow page's",
			got.key.room, got.page, artifactID, slowPage)
	}
	if took := revokedAt.Sub(dialed); took > revokeAfterDialBound {
		t.Fatalf("the revoke came %v after the slow page connected, past %v: a run this slow cannot tell the revoke's close from ygo's own %v deadline on the stalled write",
			took, revokeAfterDialBound, ygoPeerWriteTimeout)
	}

	// One more edit, straight after the revoke. The room broadcasts each update to every socket it
	// still serves, so a revoke that has not ended this one sends this edit down it too.
	editAsBrowser(t, service, artifactID, writer,
		strings.ReplaceAll(markdown, "document ", postRevokeMarker+" "))

	// The revoke's own work, and all of it: the server has closed that socket and its ServeHTTP
	// has returned. Within revokedServerEndBound of the revoke this is the revoke's doing, since
	// both deadlines ygo would end this socket on by itself fall far later.
	requireRevokedSocketEnded(t, &service.schemaAdmissions, revoked[0], revokedAt)
	writes, closedAt := serverConn.record()
	if closedAt.IsZero() {
		t.Fatal("the server never closed its side of the revoked page's socket, though its ServeHTTP returned")
	}
	if took := closedAt.Sub(revokedAt); took > revokedServerEndBound {
		t.Fatalf("the server closed its side of the revoked page's socket %v after the revoke, past %v", took, revokedServerEndBound)
	}
	requireNothingWrittenAfterTheRevoke(t, writes, revokedAt, closedAt)

	// What the page reads, read off the bytes the server wrote to it: TCP delivers exactly these,
	// in this order, and then the connection's end.
	read, err := readServerStream(streamOf(writes))
	if err != nil {
		t.Fatalf("the bytes the server wrote to the revoked page are not an end its provider reconnects on: %v", err)
	}
	if read.documentFrames < 2 || read.documentBytes <= 64*1024 {
		t.Fatalf("the server wrote the revoked page the document in %d frames carrying %d bytes; this test needs a page owed more than its buffers can take, past 64 KB and so in several of ygo's frames",
			read.documentFrames, read.documentBytes)
	}
	t.Logf("the server had the document on the wire %v after the dial; the revoked page's socket ends %s, after the document in %d frames carrying %d bytes in a send buffer of %d",
		onTheWire, read.end, read.documentFrames, read.documentBytes, grant)

	want, err := service.Text(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the server's document: %v", err)
	}
	if !strings.Contains(want, postRevokeMarker) {
		t.Fatalf("the server's document does not hold the edit made after the revoke (%q), so the reconnect below would prove nothing about it", postRevokeMarker)
	}
	// The reconnect is that page on a new socket of the same room, read-only now: the fleet that
	// revoked it admits it no other way. What it syncs must be the whole document.
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/doc/" + artifactID + "?" + slowQuery.Encode()
	reconnected := docstest.Dial(t, address, http.Header{"X-Dispatch-User": []string{"alice"}}, artifactID, crdt.New())
	if err := reconnected.WaitForText(peerMarkdown, want, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

// waitForTheDocumentOnTheWire waits until ygo's handshake to a slow page is out: the document is
// on the wire whole, in the frames of ygo's answer to the page's sync step 1 up to the SyncStep2
// that ends it, with the frame ygo queues behind that answer (its answer to the page's token)
// written too, and the page has read none of it. It returns how long after the dial that took,
// and fails once revokeAfterDialBound has passed, which is the bound the revoke itself has to be
// taken inside. Revoking before this point would revoke a socket the page was owed nothing on;
// revoking before the handshake's last frame is out would leave the server a frame still to write
// that is not the close.
func waitForTheDocumentOnTheWire(t *testing.T, socket *serverSocket, dialed time.Time) time.Duration {
	t.Helper()
	for {
		writes, _ := socket.record()
		read, err := readServerStream(streamOf(writes))
		if err == nil && read.answered && read.framesAfter > 0 {
			return time.Since(dialed)
		}
		if took := time.Since(dialed); took > revokeAfterDialBound {
			t.Fatalf("%v after the dial the server had not finished writing the slow page the document: %d writes, the last of them returned=%t, %d whole frames of the document carrying %d bytes, its closing SyncStep2 among them=%t, %d whole frames after that SyncStep2, reading the stream gave %v. A revoke taken past %v could not be told from ygo's own %v deadline on the stalled write",
				took, len(writes), len(writes) > 0 && !writes[len(writes)-1].returnedAt.IsZero(), read.documentFrames, read.documentBytes, read.answered, read.framesAfter, err, revokeAfterDialBound, ygoPeerWriteTimeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requireRevokedSocketEnded waits for the server's side of admission's socket to end, and fails
// if it is still open revokedServerEndBound after revokedAt. The socket has ended once its
// ServeHTTP has returned, which is when websocket.go removes the admission, so this reads the
// admissions under their own lock, as every other reader of them does.
func requireRevokedSocketEnded(t *testing.T, admissions *schemaAdmissions, admission *schemaAdmission, revokedAt time.Time) {
	t.Helper()
	deadline := revokedAt.Add(revokedServerEndBound)
	for {
		admissions.mu.Lock()
		_, open := admissions.sockets[admission]
		admissions.mu.Unlock()
		if !open {
			return
		}
		if now := time.Now(); now.After(deadline) {
			t.Fatalf("the revoked page's socket was still open on the server %v after the revoke, past %v: the revoke cancels that socket's request context, on which ygo waits at most %v for the close frame to go out, closes the connection and returns from ServeHTTP",
				now.Sub(revokedAt), revokedServerEndBound, ygoCloseFrameTimeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requireNothingWrittenAfterTheRevoke fails unless every write to a revoked socket that began
// after revokedAt carried nothing but, at most, one admission-revoked close frame, and no write at
// all carried a byte once the server had closed the socket at closedAt. A write that began before
// the revoke and returned after it is the message the close waits for, and is left alone. A write
// with no bytes recorded was still running when the record was taken, which cannot happen here:
// the caller reads the record once ServeHTTP has returned and the connection is closed.
func requireNothingWrittenAfterTheRevoke(t *testing.T, writes []socketWrite, revokedAt, closedAt time.Time) {
	t.Helper()
	closes := 0
	for i, write := range writes {
		if len(write.bytes) == 0 {
			continue
		}
		if write.startedAt.After(closedAt) {
			t.Fatalf("write %d of %d to the revoked page's socket carried %d bytes, having begun %v after the server closed that socket",
				i+1, len(writes), len(write.bytes), write.startedAt.Sub(closedAt))
		}
		if !write.startedAt.After(revokedAt) {
			continue
		}
		code, isClose := closeFrameCode(write.bytes)
		if !isClose {
			t.Fatalf("write %d of %d to the revoked page's socket began %v after the revoke and carried %d bytes that are not a close frame (first bytes %x): the revoke must end that socket before the room's next update reaches it",
				i+1, len(writes), write.startedAt.Sub(revokedAt), len(write.bytes), write.bytes[:min(len(write.bytes), 16)])
		}
		if code != contracts.DocumentAdmissionRevokedCloseCode {
			t.Fatalf("write %d of %d to the revoked page's socket began %v after the revoke and carried a close frame with code %d, want the admission-revoked %d, which the provider reconnects on",
				i+1, len(writes), write.startedAt.Sub(revokedAt), code, contracts.DocumentAdmissionRevokedCloseCode)
		}
		if closes++; closes > 1 {
			t.Fatalf("the server wrote %d close frames to the revoked page's socket after the revoke, want at most one", closes)
		}
	}
}

// admitSchemaSocket is one document socket of room, admitted as presented and writable, on a
// request context of its own, as Service.ServeHTTP and authorize make it: the admission holds
// that context's cancel, and the context carries the admission.
func admitSchemaSocket(t *testing.T, service *Service, room, token string, presented schemaPage, writable bool) (context.Context, *schemaAdmission) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	admission := &schemaAdmission{
		key:      schemaAdmissionKey{room: room, token: token},
		page:     presented,
		writable: writable,
		cancel:   cancel,
	}
	service.schemaAdmissions.admit(admission)
	return context.WithValue(ctx, connectionContextKey{}, &connectionState{admission: admission}), admission
}

// Two sockets of one room can present one token - the page's token is its schema_version alone -
// so a token's answer is the admission of the socket that sent it, which that socket's own request
// context carries, and never the admission of another socket of the same room and token. Here a
// page creating up to 1 and a page creating up to 2 are both admitted writable while the fleet
// reads version 2 and creates up to 2; the re-check under a fleet that creates up to 1 revokes the
// page creating up to 2, and the one creating up to 1 is still admitted. Each socket's token is
// then answered from its own admission - the live socket read-write and the revoked one read-only
// - whichever of the two asks first. Matching a token to a socket by its key, or by the order the
// two arrive in, answers one of these orders wrongly, since neither says which socket sent it.
func TestATokenIsAnsweredFromItsOwnSocketInEitherOrder(t *testing.T) {
	for _, order := range []struct {
		name         string
		revokedFirst bool
	}{
		{name: "the revoked socket's token first", revokedFirst: true},
		{name: "the live socket's token first"},
	} {
		t.Run(order.name, func(t *testing.T) {
			const room, token = "room", "2"
			admitting := schemaFleet{minVersion: 2, maxCreatable: 2}
			live := schemaPage{version: 2, creatable: 1, valid: true}
			losing := schemaPage{version: 2, creatable: 2, valid: true}
			for name, presented := range map[string]schemaPage{"creating up to 1": live, "creating up to 2": losing} {
				if !admitting.admits(presented, 0) {
					t.Fatalf("the page %s is not admitted writable by the fleet both sockets opened under", name)
				}
			}
			service := &Service{}
			liveCtx, liveAdmission := admitSchemaSocket(t, service, room, token, live, true)
			revokedCtx, revokedAdmission := admitSchemaSocket(t, service, room, token, losing, true)

			closed := service.schemaAdmissions.revokeUnless(schemaFleet{minVersion: 2, maxCreatable: 1})
			if len(closed) != 1 || closed[0] != revokedAdmission {
				t.Fatalf("the rollback revoked %d sockets, want the page creating up to 2 alone", len(closed))
			}
			if cause := context.Cause(revokedCtx); !errors.Is(cause, errSchemaRevoked) {
				t.Errorf("the revoked socket's context ended with %v, want the revoke's cause", cause)
			}
			if liveCtx.Err() != nil {
				t.Error("the socket the fleet still admits was closed with the revoked one")
			}

			answers := []struct {
				what string
				ctx  context.Context
				want bool
			}{
				{what: "the revoked socket's token", ctx: revokedCtx},
				{what: "the live socket's token", ctx: liveCtx, want: true},
			}
			if !order.revokedFirst {
				answers[0], answers[1] = answers[1], answers[0]
			}
			for _, answer := range answers {
				config, err := service.authorizeSchemaVersion(answer.ctx, room, token)
				if err != nil || config.ReadOnly == answer.want {
					t.Errorf("%s = %#v, %v; want read-write=%t", answer.what, config, err, answer.want)
				}
			}
			if liveAdmission.revoked {
				t.Error("answering the tokens revoked the socket the fleet still admits")
			}
		})
	}
}

// A revoked socket's page reconnects on the close, and that reconnect is admitted: nothing of the
// revoked socket stands in its way, however late the revoked socket's own token arrives. Here the
// revoked socket is still open and its token still unanswered, and the reconnect - read-only now,
// since the fleet no longer admits the page - is admitted and answered read-only, while the
// revoked socket's late token is answered read-only too.
func TestARevokedPagesReconnectIsAdmittedBesideItsLateToken(t *testing.T) {
	const room, token = "room", "2"
	service := &Service{}
	revokedCtx, revoked := admitSchemaSocket(t, service, room, token, schemaPage{version: 2, creatable: 2, valid: true}, true)
	if closed := service.schemaAdmissions.revokeUnless(schemaFleet{minVersion: 2, maxCreatable: 1}); len(closed) != 1 || closed[0] != revoked {
		t.Fatalf("the rollback revoked %d sockets, want the newer page's alone", len(closed))
	}

	reconnectCtx, _ := admitSchemaSocket(t, service, room, token, schemaPage{version: 2, creatable: 2, valid: true}, false)
	for _, answer := range []struct {
		what string
		ctx  context.Context
	}{
		{what: "the revoked socket's late token", ctx: revokedCtx},
		{what: "the read-only reconnect's token", ctx: reconnectCtx},
	} {
		if config, err := service.authorizeSchemaVersion(answer.ctx, room, token); err != nil || !config.ReadOnly {
			t.Errorf("%s = %#v, %v; want read-only", answer.what, config, err)
		}
	}
}

// serverSendBufferRequest is the send buffer the tests ask the kernel for on every connection the
// server accepts: stock Linux's default net.core.wmem_max. A stock kernel caps a SO_SNDBUF
// request at that value and grants twice what it kept; a kernel whose cap is raised grants twice
// this request. Both therefore grant 425984 bytes, so no host runs this test on a buffer of its
// own choosing, and the test reads back what it was granted rather than trusting either (an
// explicit SO_SNDBUF also turns the kernel's own send-buffer tuning off, so the grant is all the
// socket ever holds).
const serverSendBufferRequest = 212992

// setSendBuffer asks for serverSendBufferRequest on tcp and returns the SO_SNDBUF the kernel
// granted, which is how many bytes that socket holds for a page that reads nothing.
func setSendBuffer(tcp *net.TCPConn) (int, error) {
	if err := tcp.SetWriteBuffer(serverSendBufferRequest); err != nil {
		return 0, err
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return 0, err
	}
	var granted int
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		granted, readErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	}); err != nil {
		return 0, err
	}
	return granted, readErr
}

// sendBufferGrant is the send buffer the kernel grants a connection the server accepts, measured
// on a loopback connection of this test's own: a test's document is sized from it before it is
// seeded - the slow page's to fit it whole, since a write the buffer cannot take blocks and a
// blocked write has put no bytes on the wire for the test to read, and the in-flight page's to
// overrun it.
func sendBufferGrant(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen to measure the send buffer the kernel grants: %v", err)
	}
	defer listener.Close()
	dialed, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("connect to measure the send buffer the kernel grants: %v", err)
	}
	defer dialed.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept to measure the send buffer the kernel grants: %v", err)
	}
	defer accepted.Close()
	tcp, ok := accepted.(*net.TCPConn)
	if !ok {
		t.Fatalf("the loopback listener accepted a %T, want a TCP connection whose send buffer this can set", accepted)
	}
	granted, err := setSendBuffer(tcp)
	if err != nil {
		t.Fatalf("set and read back a send buffer of %d bytes: %v", serverSendBufferRequest, err)
	}
	return granted
}

// slowPageReceiveBuffer is the SO_RCVBUF the slow page asks for before it connects: far less than
// any document the test sends it, so what it is owed stays queued on the server's side.
const slowPageReceiveBuffer = 4096

// slowPageLeastDocument is the smallest document the slow-page test works with: past the 64 KB it
// requires of the document it watches go out, so ygo sends it in more than one frame, and far past
// the slow page's 4096-byte receive buffer.
const slowPageLeastDocument = 96 << 10

// slowPageDocumentSize is how big that test makes the document on a socket whose send buffer the
// kernel granted grant bytes: a quarter of the buffer, and never under slowPageLeastDocument. The
// server's writes of the document's frames then return with every byte of them queued while the
// page reads none, and the buffer still has room for the edit the test makes after the revoke,
// which a revoke that has not ended the socket sends down it. A grant too small to hold such a
// document with as much again free is reported rather than worked around.
func slowPageDocumentSize(t *testing.T, grant int) int {
	t.Helper()
	size := max(grant/4, slowPageLeastDocument)
	if 2*size > grant {
		t.Fatalf("the kernel granted a send buffer of %d bytes, too small for a document of %d bytes - the least this test needs, past the 64 KB a page's buffers cannot take - with as much again free for the edit after the revoke",
			grant, size)
	}
	return size
}

// recordingListener hands every accepted connection out as a serverSocket, so a test can find
// the one belonging to a page it dialled and read exactly what the server wrote to it. gorilla's
// Hijack hands ygo that same connection.
type recordingListener struct {
	net.Listener
	mu       sync.Mutex
	accepted []*serverSocket
}

func (l *recordingListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := connection.(*net.TCPConn); ok {
		if _, err := setSendBuffer(tcp); err != nil {
			_ = tcp.Close()
			return nil, err
		}
	}
	socket := &serverSocket{Conn: connection}
	l.mu.Lock()
	l.accepted = append(l.accepted, socket)
	l.mu.Unlock()
	return socket, nil
}

// find is the accepted connection whose remote address is remote, a page's local address, or nil
// when no connection has it.
func (l *recordingListener) find(remote string) *serverSocket {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, socket := range l.accepted {
		if socket.RemoteAddr().String() == remote {
			return socket
		}
	}
	return nil
}

// socketWrite is one Write to a server socket: when it began, when it returned (zero while it has
// not), and the bytes the connection accepted of it - the n it returned - which are the bytes that
// went on the wire.
type socketWrite struct {
	startedAt  time.Time
	returnedAt time.Time
	bytes      []byte
}

// serverSocket is the server's end of one accepted connection, recording every write and when
// the server first closed it. The writes are recorded in the order they began, which is the order
// their bytes went out in: ygo writes to a peer from one goroutine of its own, and gorilla
// serialises that writer against the WriteControl a revoke's close frame goes out on.
type serverSocket struct {
	net.Conn
	mu       sync.Mutex
	writes   []socketWrite
	closedAt time.Time
}

func (s *serverSocket) Write(payload []byte) (int, error) {
	s.mu.Lock()
	at := len(s.writes)
	s.writes = append(s.writes, socketWrite{startedAt: time.Now()})
	s.mu.Unlock()
	n, err := s.Conn.Write(payload)
	returnedAt := time.Now()
	s.mu.Lock()
	s.writes[at].returnedAt = returnedAt
	s.writes[at].bytes = append([]byte(nil), payload[:n]...)
	s.mu.Unlock()
	return n, err
}

func (s *serverSocket) Close() error {
	now := time.Now()
	s.mu.Lock()
	if s.closedAt.IsZero() {
		s.closedAt = now
	}
	s.mu.Unlock()
	return s.Conn.Close()
}

// record is every write this connection has taken so far and the instant the server first closed
// it, zero while it is still open.
func (s *serverSocket) record() ([]socketWrite, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socketWrite(nil), s.writes...), s.closedAt
}

// streamOf is the byte stream a socket's writes put on the wire: the bytes each write was
// accepted for, in the order the writes began.
func streamOf(writes []socketWrite) []byte {
	var stream []byte
	for _, write := range writes {
		stream = append(stream, write.bytes...)
	}
	return stream
}

// The websocket opcodes a server may write (RFC 6455, section 5.2); every other value of those
// four bits is reserved, and a stream that reads one is not a stream of whole frames.
const (
	opcodeContinuation byte = 0x0
	opcodeText         byte = 0x1
	opcodeBinary       byte = 0x2
	opcodeClose        byte = 0x8
	opcodePing         byte = 0x9
	opcodePong         byte = 0xa
)

// websocketFrame is one frame at the head of a server's byte stream: its opcode, the payload
// bytes the stream holds of it, the payload length its header declares - which is what the server
// wrote, whole or part way out - and whether the stream held the whole frame. A frame whose
// header is itself cut short declares nothing, so its length is the bytes it holds.
type websocketFrame struct {
	opcode  byte
	payload []byte
	length  int
	whole   bool
}

// maxFrameLength bounds the payload length a header may declare, far above any message ygo
// writes: a longer one is a header no server wrote, not a message to make room for.
const maxFrameLength = 1 << 31

// nextServerFrame reads the frame at the head of a server's stream, which must not be empty, and
// returns it with the bytes after it. A stream that ends inside the frame yields the payload
// bytes it holds, whole false and no bytes after. It errors on a header no server wrote: a
// reserved opcode or reserved bit, which is what a frame cut in half and resumed reads as, the
// mask only a client sets, or a payload length past maxFrameLength.
func nextServerFrame(stream []byte) (websocketFrame, []byte, error) {
	opcode := stream[0] & 0x0f
	switch opcode {
	case opcodeContinuation, opcodeText, opcodeBinary, opcodeClose, opcodePing, opcodePong:
	default:
		return websocketFrame{}, nil, fmt.Errorf("a frame whose first byte is %#02x carries the reserved opcode %#x", stream[0], opcode)
	}
	if stream[0]&0x70 != 0 {
		return websocketFrame{}, nil, fmt.Errorf("a frame whose first byte is %#02x sets a reserved bit", stream[0])
	}
	if len(stream) < 2 {
		return websocketFrame{opcode: opcode}, nil, nil
	}
	if stream[1]&0x80 != 0 {
		return websocketFrame{}, nil, fmt.Errorf("a frame whose second byte is %#02x is masked, which a server never does", stream[1])
	}
	length, header := uint64(stream[1]&0x7f), 2
	switch length {
	case 126:
		if header = 4; len(stream) < header {
			return websocketFrame{opcode: opcode}, nil, nil
		}
		length = uint64(binary.BigEndian.Uint16(stream[2:4]))
	case 127:
		if header = 10; len(stream) < header {
			return websocketFrame{opcode: opcode}, nil, nil
		}
		length = binary.BigEndian.Uint64(stream[2:10])
	}
	if length > maxFrameLength {
		return websocketFrame{}, nil, fmt.Errorf("a frame declaring %d payload bytes, past the %d no message reaches", length, maxFrameLength)
	}
	body := stream[header:]
	if length > uint64(len(body)) {
		return websocketFrame{opcode: opcode, payload: body, length: int(length)}, nil, nil
	}
	return websocketFrame{opcode: opcode, payload: body[:length], length: int(length), whole: true}, body[length:], nil
}

// closeFrameCode reads payload as one whole close frame of a server's and returns the code it
// carries. Anything else - a data frame, a frame cut short, a frame with bytes after it, a close
// with no code - is not one.
func closeFrameCode(payload []byte) (int, bool) {
	frame, after, err := nextServerFrame(payload)
	if err != nil || !frame.whole || frame.opcode != opcodeClose || len(after) > 0 || len(frame.payload) < 2 {
		return 0, false
	}
	return int(binary.BigEndian.Uint16(frame.payload)), true
}

// serverStreamEnd is how the bytes a server wrote to one socket end, which is the end its page
// reads off that socket.
type serverStreamEnd string

const (
	// endsOnTheRevokedClose: the stream's last frame is the admission-revoked close frame, which
	// the page reads as that code.
	endsOnTheRevokedClose serverStreamEnd = "on the admission-revoked close frame"
	// endsAtACut: the stream stops with no close frame, at a frame boundary or inside a frame,
	// which the page reads as 1006.
	endsAtACut serverStreamEnd = "at a cut, which the page reads as 1006"
)

// serverStream is what the bytes a server wrote to one document socket amount to: how they end,
// the frames of the room's document in them, and the frame they stop inside, if they stop inside
// one. ygo sends a page the document in answer to the page's sync step 1, as sync Updates of at
// most documentFrameBytes a frame and then the SyncStep2 that ends the answer: documentFrames
// counts those frames read whole, documentBytes is the payload they carry, answered says the
// SyncStep2 is among them, and framesAfter counts the whole frames after it, which say the server
// has moved on from writing the document. cutLength is the payload a frame the stream stops
// inside declares and cutHeld how much of it the stream holds, both 0 when it stops at a frame's
// end.
type serverStream struct {
	end            serverStreamEnd
	documentFrames int
	documentBytes  int
	answered       bool
	framesAfter    int
	cutLength      int
	cutHeld        int
}

// readServerStream reads the bytes a server wrote to one document socket - its HTTP 101 response
// and the websocket frames after it. It errors on anything the page could not read as an end its
// provider reconnects on: a frame header no server wrote, a close frame carrying any other code,
// or a byte after a close frame.
func readServerStream(stream []byte) (serverStream, error) {
	head := bytes.Index(stream, []byte("\r\n\r\n"))
	if head < 0 {
		return serverStream{}, fmt.Errorf("the %d bytes the server wrote hold no whole HTTP response head, so the socket never became a websocket", len(stream))
	}
	read := serverStream{end: endsAtACut}
	for rest := stream[head+4:]; len(rest) > 0; {
		offset := len(stream) - len(rest)
		frame, after, err := nextServerFrame(rest)
		if err != nil {
			return serverStream{}, fmt.Errorf("%d bytes in: %w", offset, err)
		}
		if !frame.whole {
			read.cutLength, read.cutHeld = frame.length, len(frame.payload)
			return read, nil
		}
		if frame.opcode == opcodeClose {
			if len(frame.payload) < 2 {
				return serverStream{}, fmt.Errorf("the close frame %d bytes in carries %d bytes, too few for a code", offset, len(frame.payload))
			}
			if code := int(binary.BigEndian.Uint16(frame.payload)); code != contracts.DocumentAdmissionRevokedCloseCode {
				return serverStream{}, fmt.Errorf("the close frame %d bytes in carries code %d, want the admission-revoked %d", offset, code, contracts.DocumentAdmissionRevokedCloseCode)
			}
			if len(after) > 0 {
				return serverStream{}, fmt.Errorf("%d bytes follow the close frame %d bytes in", len(after), offset)
			}
			read.end = endsOnTheRevokedClose
			return read, nil
		}
		if read.answered {
			read.framesAfter++
		} else if document, last := documentFrame(frame.payload); frame.opcode == opcodeBinary && document {
			read.documentFrames++
			read.documentBytes += frame.length
			read.answered = last
		}
		rest = after
	}
	return read, nil
}

// documentFrame reads payload, one whole binary message of a document socket's server, as the
// Hocuspocus framing Dispatch serves carries it (docs/service.go sets HocuspocusFraming): the
// document's name, the message type, then the sync message. It reports whether that message
// carries the room's document - a sync Update or a SyncStep2 - and whether it is that SyncStep2,
// which ends ygo's answer to a page's sync step 1.
func documentFrame(payload []byte) (document, last bool) {
	decoder := encoding.NewDecoder(payload)
	if _, err := decoder.ReadVarString(); err != nil {
		return false, false
	}
	if kind, err := decoder.ReadVarUint(); err != nil || kind != 0 { // Hocuspocus sync message.
		return false, false
	}
	switch kind, _, err := ygsync.ReadSyncMessage(decoder.RemainingBytes()); {
	case err != nil:
		return false, false
	case kind == ygsync.MsgUpdate:
		return true, false
	case kind == ygsync.MsgSyncStep2:
		return true, true
	}
	return false, false
}

// serverFrame is one unmasked, final server frame of opcode carrying payload, with the length
// encoding that payload's size calls for.
func serverFrame(opcode byte, payload []byte) []byte {
	frame := []byte{0x80 | opcode}
	switch n := len(payload); {
	case n < 126:
		frame = append(frame, byte(n))
	case n < 1<<16:
		frame = binary.BigEndian.AppendUint16(append(frame, 126), uint16(n))
	default:
		frame = binary.BigEndian.AppendUint64(append(frame, 127), uint64(n))
	}
	return append(frame, payload...)
}

// readServerStream is what the two revoke tests above read a socket's record off, so these are the
// streams it has to tell apart: the two ends the provider reconnects on, and three a revoked page
// could read as neither. The document in each crosses in frames as ygo cuts it - Updates, then the
// SyncStep2 that ends the answer - among frames that are not the document, which the reader must
// not count: the sync step 1 the server asks with and its answer to the page's token. The frames
// after the SyncStep2 are how the slow-page test knows the server has finished writing the
// document, and the frame a stream stops inside is the one the in-flight test watches go out.
func TestTheServerStreamReaderTellsARevokedPagesEndsApart(t *testing.T) {
	const head = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n"
	// hocuspocus is one Hocuspocus message of the document "document" of type kind (0 sync, 2
	// authentication), body after its type.
	hocuspocus := func(kind uint64, body func(*encoding.Encoder)) []byte {
		return encoding.EncodeBytes(func(encoder *encoding.Encoder) {
			encoder.WriteVarString("document")
			encoder.WriteVarUint(kind)
			body(encoder)
		})
	}
	// syncMessage is a Hocuspocus sync message of ygo's sync type kind carrying size bytes.
	syncMessage := func(kind uint64, size int) []byte {
		return hocuspocus(0, func(encoder *encoding.Encoder) {
			encoder.WriteVarUint(kind)
			encoder.WriteVarBytes(bytes.Repeat([]byte("d"), size))
		})
	}
	update, step2 := syncMessage(ygsync.MsgUpdate, 60<<10), syncMessage(ygsync.MsgSyncStep2, 8<<10)
	updateFrame, step2Frame := serverFrame(opcodeBinary, update), serverFrame(opcodeBinary, step2)
	step1Frame := serverFrame(opcodeBinary, syncMessage(ygsync.MsgSyncStep1, 1))
	tokenAnswer := serverFrame(opcodeBinary, hocuspocus(2, func(encoder *encoding.Encoder) {
		encoder.WriteVarUint(2) // Authenticated.
		encoder.WriteVarString("read-write")
	}))
	revokedClose := serverFrame(opcodeClose, binary.BigEndian.AppendUint16(nil, uint16(contracts.DocumentAdmissionRevokedCloseCode)))
	stream := func(parts ...[]byte) []byte {
		return bytes.Join(append([][]byte{[]byte(head)}, parts...), nil)
	}
	for _, c := range []struct {
		name    string
		stream  []byte
		want    serverStream
		wantErr string
	}{
		{
			name:   "the document's frames and the token's answer, ended by the admission-revoked close",
			stream: stream(step1Frame, updateFrame, updateFrame, step2Frame, tokenAnswer, revokedClose),
			want: serverStream{end: endsOnTheRevokedClose, documentFrames: 3, documentBytes: 2*len(update) + len(step2),
				answered: true, framesAfter: 1},
		},
		{
			name:   "a stream cut inside a frame of the document, which the page reads as 1006",
			stream: stream(step1Frame, updateFrame, updateFrame[:len(updateFrame)-1000]),
			want: serverStream{end: endsAtACut, documentFrames: 1, documentBytes: len(update),
				cutLength: len(update), cutHeld: len(update) - 1000},
		},
		{
			name:    "a close frame carrying another code",
			stream:  stream(updateFrame, serverFrame(opcodeClose, binary.BigEndian.AppendUint16(nil, 1000))),
			wantErr: "carries code 1000",
		},
		{
			name:    "a byte after the close frame",
			stream:  stream(updateFrame, revokedClose, []byte{0x81}),
			wantErr: "follow the close frame",
		},
		{
			name:    "a reserved opcode, which is what a frame cut in half and resumed reads as",
			stream:  stream(updateFrame, serverFrame(0x3, nil)),
			wantErr: "reserved opcode",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			read, err := readServerStream(c.stream)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("readServerStream = %+v, %v; want an error naming %q", read, err, c.wantErr)
				}
				return
			}
			if err != nil || read != c.want {
				t.Fatalf("readServerStream = %+v, %v; want %+v, no error", read, err, c.want)
			}
		})
	}
}
