package testnats

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	natsgo "github.com/nats-io/nats.go"
)

// The servers a package's tests share (URL): each test gets a JetStream account no earlier test
// used, on servers an account server starts as tests ask. A server whose accounts are all handed out
// is followed by a new one, and no server another test uses is ever reloaded (Server.Reload refuses
// one, by the shared mark an account server sets on each server it starts).

var (
	mainRuns      bool
	sharedOnce    sync.Once
	shared        *accountServer
	sharedCleanup *mainCleanups
)

// accountsPerServer is how many accounts each of a package's shared servers declares. A test that
// asks for an account once every account of the newest server has been handed out gets the first
// account of a new server of the same size, so every start costs what the first did: a server's
// start time grows faster than its account count (0.9 s for 1,000 on a loaded devbox, 26 s for
// 5,000).
//
// The servers are never reloaded to declare more (Server.Reload refuses one): a reload holds the
// clients of every test on the server still.
const accountsPerServer = 256

// Main runs the package's tests, then shuts the shared servers down and removes their stores if a
// test started them, and returns the exit code. A package whose tests use URL calls it from
// TestMain: os.Exit(testnats.Main(m)).
func Main(m *testing.M) int {
	mainRuns = true
	code := m.Run()
	if sharedCleanup != nil {
		sharedCleanup.run()
		if sharedCleanup.failed && code == 0 {
			code = 1
		}
	}
	return code
}

// URL returns one of the package's shared NATS servers as the user of a JetStream account no
// earlier test used, so none of a previous test's streams, KV buckets, consumers or messages is
// there, and no stream name another test deleted can be recovered under it. A test gets one
// account however often it asks, so every connection it makes reaches the same streams; each
// subtest is a test of its own and gets its own account, since subtests that each delete and
// recreate one name in a shared account would race as tests did on a shared server.
// When the test ends, its account's streams are deleted to free their storage; nothing creates a
// stream in that account again, so a delete that leaves files behind reaches no test.
func URL(t testing.TB) string {
	t.Helper()
	if !mainRuns {
		t.Fatal("testnats: the package's TestMain must return testnats.Main(m), which shuts the shared NATS servers down")
	}
	sharedOnce.Do(func() {
		sharedCleanup = &mainCleanups{}
		shared = newAccountServer(sharedCleanup, accountsPerServer)
	})
	return shared.url(t)
}

// serverOwner is what removes the servers an account server starts, and hears what each removal
// reports: the test, for an account server of a test's own, or Main, for the package's shared one.
type serverOwner interface {
	Cleanup(func())
	Errorf(format string, args ...any)
}

// mainCleanups is the owner of the package's shared servers. They outlive every test, so Main runs
// their cleanups after the last test, and what a cleanup reports goes to stderr and the exit code:
// a t.Errorf on a test that has already ended panics.
type mainCleanups struct {
	cleanups []func()
	failed   bool
}

func (m *mainCleanups) Cleanup(clean func()) { m.cleanups = append(m.cleanups, clean) }

func (m *mainCleanups) Errorf(format string, args ...any) {
	m.failed = true
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// run runs the cleanups, the most recent first, as testing does.
func (m *mainCleanups) run() {
	for i := len(m.cleanups) - 1; i >= 0; i-- {
		m.cleanups[i]()
	}
}

// ownedServer is the test that asks an account server for an account, as the server the request
// starts sees it: a failure to start the server fails that test, but the server outlives the test,
// so its cleanup, and what the cleanup reports, belong to the account server's owner.
type ownedServer struct {
	testing.TB
	owner serverOwner
}

func (o ownedServer) Cleanup(clean func()) { o.owner.Cleanup(clean) }

func (o ownedServer) Errorf(format string, args ...any) { o.owner.Errorf(format, args...) }

// accountServer hands each test a JetStream account no earlier test used, on NATS servers it
// starts as tests ask, each declaring perServer accounts and run with args, and its owner removes
// them.
type accountServer struct {
	owner     serverOwner
	perServer int
	args      []string

	mu sync.Mutex
	// newest is the server the next account comes from, nil until a test first asks, host the
	// host and port its URL names, and handedOut how many of its accounts it has handed to a test.
	// An account is never handed out twice, and a server whose accounts are all handed out hands
	// out no more.
	newest    *Server
	host      string
	handedOut int
	// startFailed is set while a start runs and stays set when the start fails the test that asked
	// for it, so each later test fails at once rather than wait out a start of its own: a start
	// that cannot succeed would otherwise cost every test of the package its connectTimeout.
	startFailed bool
	// holders maps each test holding an account to that account's URL.
	holders map[string]string
}

// newAccountServer returns an account server whose servers declare perServer accounts each, run
// with JetStream, and are removed by owner. It starts its first server when a test first asks for
// an account.
func newAccountServer(owner serverOwner, perServer int) *accountServer {
	return &accountServer{owner: owner, perServer: perServer, args: []string{"-js"}, holders: map[string]string{}}
}

// url is URL on s.
func (s *accountServer) url(t testing.TB) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if uri, ok := s.holders[t.Name()]; ok {
		return uri
	}
	if s.newest == nil || s.handedOut == s.perServer {
		if s.startFailed {
			t.Fatal("testnats: the account server's last start of a NATS server failed, and the test that asked for it reports why; no later test is handed an account")
		}
		// start fails the asking test with t.Fatal when the server does not start, which leaves
		// startFailed set.
		s.startFailed = true
		s.newest = start(ownedServer{TB: t, owner: s.owner}, accountsConfig(s.perServer), s.args)
		s.startFailed = false
		s.newest.shared = true
		s.host = strings.TrimPrefix(s.newest.URL(), "nats://")
		s.handedOut = 0
	}
	s.handedOut++
	account := s.handedOut
	// The account's one user, whose password is its name (accountsConfig). The URL is built with
	// url.UserPassword, as elsewhere in this module, so no line spells a URL that carries a password.
	user := fmt.Sprintf("t%d", account)
	uri := (&url.URL{Scheme: "nats", User: url.UserPassword(user, user), Host: s.host}).String()
	// Wait for the account to answer, after the start that declared it.
	Connect(t, uri).Close()
	name := t.Name()
	s.holders[name] = uri
	t.Cleanup(func() {
		s.mu.Lock()
		delete(s.holders, name)
		s.mu.Unlock()
		deleteStreams(t, uri)
	})
	return uri
}

// accountsConfig declares accounts T1 to Tn, each with JetStream and one user, tn, whose password
// is its name.
func accountsConfig(n int) string {
	var config strings.Builder
	config.WriteString("accounts {\n")
	for account := 1; account <= n; account++ {
		fmt.Fprintf(&config, "  T%d: { jetstream: enabled, users: [ { user: t%d, password: t%d } ] }\n", account, account, account)
	}
	config.WriteString("}\n")
	return config.String()
}

// deleteStreams deletes every stream in the account uri names, as its test ends.
func deleteStreams(t testing.TB, uri string) {
	t.Helper()
	conn := Connect(t, uri)
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Errorf("open JetStream to delete the test's streams: %v", err)
		return
	}
	var streams []string
	for name := range js.StreamNames() {
		streams = append(streams, name)
	}
	for _, name := range streams {
		if err := js.DeleteStream(name); err != nil && !errors.Is(err, natsgo.ErrStreamNotFound) {
			t.Errorf("delete the test's stream %s: %v", name, err)
		}
	}
}
