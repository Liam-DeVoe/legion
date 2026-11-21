//go:build natslive

package core_test

// A prototype, not a change to keep: rows that measure one private project, VLX, in a NATS account
// of its own on the private render (core's Render with privateProjects, JetStream in JETSTREAM and
// every client in ENVOY). VLX gets the same pair: VLX_JETSTREAM, with JetStream on and no user,
// whose exports and imports are copied line for line from the render's JETSTREAM and ENVOY, and
// VLX, where its clients sit. VLX_JETSTREAM also imports the GitHub events from the account the
// GitHub publisher writes in (PROTO_VLX_GITHUB_FROM, ENVOY by default, or JETSTREAM), so VLX's
// own copy of the notification stream stores them.
//
// Each row starts nats-server v2.15.1 in this process on that text, with a stand-in auth service
// that places each token in the account it is given and answers its grant, as the auth service's
// answer would (the user JWT's audience names the account: nats-server server/auth_callout.go:
// 222-231). Every grant is natsgrant's own function for its class.
//
// PROTO_VLX_LAYOUT runs a row's assertion on another layout, the controls:
//   - vlx (default): the proposal;
//   - shared: the private render as cybermonklabs/legion#4851 builds it, VLX one project of ENVOY;
//   - mergebase: every user in $G, as a hub runs today (the go-live ledger's row 44c);
//   - vlx-inline: VLX with JetStream on in VLX itself, no VLX_JETSTREAM.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/core"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/natsgrant"
)

const (
	vlxJetStreamAccount = "VLX_JETSTREAM"
	vlxClientAccount    = "VLX"
	vlxOperatorToken    = "vlx-operator"
	// VLX's issue subjects and INFRA's, with no project in the name as the bus has them today; the
	// key's hyphen is a dot here, as #4851's rows write theirs, so no line of this file is a key.
	vlxIssue   = "notifications.dispatch.issue.VLX.1."
	infraIssue = "notifications.dispatch.issue.INFRA.12."
	gitHubAll  = "notifications.github.>"
)

var everything = natsgrant.Permissions{Publish: []string{">"}, Subscribe: []string{">"}}

type vlxLayout string

const (
	layoutVLX       vlxLayout = "vlx"
	layoutShared    vlxLayout = "shared"
	layoutMergeBase vlxLayout = "mergebase"
	layoutVLXInline vlxLayout = "vlx-inline"
)

// vlxOptions are a hub's layout: where VLX sits, the account its GitHub events come from and the
// subject that import names, and a last edit of the text.
type vlxOptions struct {
	layout        vlxLayout
	githubFrom    string
	githubSubject string
	edit          func(t *testing.T, text string) string
	// unnamedUsers has the stand-in auth service leave the user JWT's name empty, where the auth
	// service names it (natscallout callout.go:697).
	unnamedUsers bool
}

func defaultVLXOptions() vlxOptions {
	return vlxOptions{layout: layoutVLX, githubFrom: core.ClientAccount, githubSubject: gitHubAll}
}

// vlxOptionsFromEnv are the default options with PROTO_VLX_LAYOUT and PROTO_VLX_GITHUB_FROM applied.
func vlxOptionsFromEnv(t *testing.T) vlxOptions {
	t.Helper()
	o := defaultVLXOptions()
	if v := os.Getenv("PROTO_VLX_LAYOUT"); v != "" {
		o.layout = vlxLayout(v)
	}
	if v := os.Getenv("PROTO_VLX_GITHUB_FROM"); v != "" {
		o.githubFrom = v
	}
	require.Contains(t, []vlxLayout{layoutVLX, layoutShared, layoutMergeBase, layoutVLXInline}, o.layout)
	require.Contains(t, []string{core.ClientAccount, core.JetStreamAccount}, o.githubFrom)
	t.Logf("layout %s; VLX's GitHub events imported from %s", o.layout, o.githubFrom)
	return o
}

// side is which party a client is: a client of the shared projects, a client of VLX (Dispatch's
// VLX connection, the edge listener), or the operator of the account VLX's stream sits in.
type side int

const (
	sideEnvoy side = iota
	sideVLX
	sideVLXStore
)

// account is the account the layout places a client of side in.
func (o vlxOptions) account(s side) string {
	switch o.layout {
	case layoutMergeBase:
		return natsserver.DEFAULT_GLOBAL_ACCOUNT
	case layoutShared:
		if s == sideVLXStore {
			return core.JetStreamAccount
		}
		return core.ClientAccount
	case layoutVLXInline:
		if s == sideEnvoy {
			return core.ClientAccount
		}
		return vlxClientAccount
	default:
		switch s {
		case sideEnvoy:
			return core.ClientAccount
		case sideVLX:
			return vlxClientAccount
		default:
			return vlxJetStreamAccount
		}
	}
}

// vlxHasOwnStore reports whether VLX's stream is a stream of its own, apart from the shared one.
func (o vlxOptions) vlxHasOwnStore() bool {
	return o.layout == layoutVLX || o.layout == layoutVLXInline
}

// vlxClient is one token the stand-in auth service admits: its side and its grant.
type vlxClient struct {
	side  side
	grant natsgrant.Permissions
}

// usersList is one account's users list as the render writes it.
var usersList = regexp.MustCompile(`(?s)    users: \[\n.*?\n    \]\n`)

func replaceOnce(t *testing.T, text, old, replacement string) string {
	t.Helper()
	require.Equal(t, 1, strings.Count(text, old), "the text holds %q %d times:\n%s", old, strings.Count(text, old), text)
	return strings.Replace(text, old, replacement, 1)
}

// accountsEnd is the offset in text just past the last account block: where a block is added.
func accountsEnd(t *testing.T, text string) int {
	t.Helper()
	end := strings.Index(text, "\n}\nauthorization {")
	require.Positive(t, end, "the text has no accounts block:\n%s", text)
	return end + 1
}

func streamImport(from, subject string) string {
	return fmt.Sprintf(`{"stream": {"account": %q, "subject": %q}}`, from, subject)
}

// exportGitHub adds the export of o.githubSubject from o.githubFrom to the account to.
func exportGitHub(t *testing.T, text string, o vlxOptions, to string) string {
	t.Helper()
	line := fmt.Sprintf("      {\"accounts\": [%q], \"stream\": %q}\n", to, o.githubSubject)
	if os.Getenv("PROTO_VLX_GITHUB_PUBLIC") != "" {
		// The export with no accounts list: any account may import it.
		line = fmt.Sprintf("      {\"stream\": %q}\n", o.githubSubject)
	}
	if o.githubFrom == core.JetStreamAccount {
		const last = "      {\"stream\": \"_INBOX_envoy.>\"}\n"
		return replaceOnce(t, text, last, last+line)
	}
	return replaceOnce(t, text, "  "+core.ClientAccount+" {\n", "  "+core.ClientAccount+" {\n    exports: [\n"+line+"    ]\n")
}

// withVLXPair is text with VLX's pair: the render's JETSTREAM and ENVOY blocks copied without their
// users and renamed, and the import of the GitHub events into VLX_JETSTREAM.
func withVLXPair(t *testing.T, text string, o vlxOptions) string {
	t.Helper()
	start := strings.Index(text, "  "+core.JetStreamAccount+" {\n")
	require.GreaterOrEqual(t, start, 0, "the render has no %s block:\n%s", core.JetStreamAccount, text)
	pair := usersList.ReplaceAllString(text[start:accountsEnd(t, text)], "")
	pair = replaceOnce(t, pair, "  "+core.JetStreamAccount+" {\n    jetstream: enabled\n",
		"  "+vlxJetStreamAccount+" {\n    jetstream: enabled\n    imports: [\n      "+streamImport(o.githubFrom, o.githubSubject)+"\n    ]\n")
	pair = replaceOnce(t, pair, "  "+core.ClientAccount+" {\n", "  "+vlxClientAccount+" {\n")
	pair = strings.ReplaceAll(pair, `"account": "`+core.JetStreamAccount+`"`, `"account": "`+vlxJetStreamAccount+`"`)
	require.NotContains(t, pair, `"`+core.JetStreamAccount+`"`)
	text = exportGitHub(t, text, o, vlxJetStreamAccount)
	end := accountsEnd(t, text)
	return text[:end] + pair + text[end:]
}

// withVLXInline is text with VLX as one account with JetStream on, importing the GitHub events.
func withVLXInline(t *testing.T, text string, o vlxOptions) string {
	t.Helper()
	text = exportGitHub(t, text, o, vlxClientAccount)
	block := "  " + vlxClientAccount + " {\n    jetstream: enabled\n    imports: [\n      " + streamImport(o.githubFrom, o.githubSubject) + "\n    ]\n  }\n"
	end := accountsEnd(t, text)
	return text[:end] + block + text[end:]
}

// vlxConfigText is the authorization text of layout o.
func vlxConfigText(t *testing.T, keys testnats.CalloutKeys, operatorKey string, o vlxOptions) string {
	t.Helper()
	text, err := hubAuthorization(keys, operatorKey, o.layout != layoutMergeBase).Render(nil)
	require.NoError(t, err)
	switch o.layout {
	case layoutVLX:
		text = withVLXPair(t, text, o)
	case layoutVLXInline:
		text = withVLXInline(t, text, o)
	}
	if o.edit != nil {
		text = o.edit(t, text)
	}
	return text
}

// vlxServerLog keeps every line the server logs.
type vlxServerLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *vlxServerLog) add(level, format string, v []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, v...))
}
func (l *vlxServerLog) Noticef(format string, v ...any) { l.add("[INF]", format, v) }
func (l *vlxServerLog) Warnf(format string, v ...any)   { l.add("[WRN]", format, v) }
func (l *vlxServerLog) Fatalf(format string, v ...any)  { l.add("[FTL]", format, v) }
func (l *vlxServerLog) Errorf(format string, v ...any)  { l.add("[ERR]", format, v) }
func (l *vlxServerLog) Debugf(format string, v ...any)  { l.add("[DBG]", format, v) }
func (l *vlxServerLog) Tracef(format string, v ...any)  { l.add("[TRC]", format, v) }

func (l *vlxServerLog) mark() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *vlxServerLog) since(mark int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines[mark:]...)
}

// placed is where the stand-in auth service puts a token: its account and its grant.
type placed struct {
	account string
	grant   natsgrant.Permissions
}

// placingCallout is the stand-in auth service: each token is admitted to its own account, and
// each authorization request it answers is counted by token.
type placingCallout struct {
	mu       sync.Mutex
	tokens   map[string]placed
	requests map[string]int
	named    bool
}

func (p *placingCallout) set(token string, at placed) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens[token] = at
}

func (p *placingCallout) lookup(token string) (placed, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.tokens[token]
	return at, ok
}

func (p *placingCallout) ask(token string) (placed, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests[token]++
	at, ok := p.tokens[token]
	return at, ok
}

func (p *placingCallout) asked(token string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[token]
}

// serve connects the stand-in as the rendered service user and answers each authorization
// request, as serveCallout does, with the user JWT's audience the token's own account.
func (p *placingCallout) serve(t *testing.T, url string, keys testnats.CalloutKeys) {
	t.Helper()
	issuer, err := nkeys.FromSeed([]byte(keys.IssuerSeed))
	require.NoError(t, err)
	curve, err := nkeys.FromSeed([]byte(keys.XKeySeed))
	require.NoError(t, err)
	service, err := testnats.ConnectSeed(t, url, keys.ServiceSeed)
	require.NoError(t, err, "the auth service's user, an auth user, connects without a callout")
	t.Cleanup(service.Close)
	_, err = service.Subscribe("$SYS.REQ.USER.AUTH", func(m *natsgo.Msg) {
		opened, err := curve.Open(m.Data, m.Header.Get("Nats-Server-Xkey"))
		if err != nil {
			t.Errorf("open the sealed authorization request: %v", err)
			return
		}
		request, err := jwt.DecodeAuthorizationRequestClaims(string(opened))
		if err != nil {
			t.Errorf("decode the authorization request: %v", err)
			return
		}
		response := jwt.NewAuthorizationResponseClaims(request.UserNkey)
		response.Audience = request.Server.ID
		token := request.ConnectOptions.Token
		if at, ok := p.ask(token); ok {
			user := jwt.NewUserClaims(request.UserNkey)
			user.Audience = at.account
			if p.named {
				user.Name = token
			}
			user.Pub.Allow.Add(at.grant.Publish...)
			user.Sub.Allow.Add(at.grant.Subscribe...)
			user.Pub.Deny.Add(at.grant.PublishDeny...)
			user.Sub.Deny.Add(at.grant.SubscribeDeny...)
			if response.Jwt, err = user.Encode(issuer); err != nil {
				t.Errorf("sign the user: %v", err)
				return
			}
		} else {
			response.Error = "example: no grant for this token"
		}
		answer, err := response.Encode(issuer)
		if err != nil {
			t.Errorf("sign the answer: %v", err)
			return
		}
		sealed, err := curve.Seal([]byte(answer), request.Server.XKey)
		if err != nil {
			t.Errorf("seal the answer: %v", err)
			return
		}
		if err := m.Respond(sealed); err != nil {
			t.Errorf("answer the authorization request: %v", err)
		}
	})
	require.NoError(t, err)
	require.NoError(t, service.Flush())
}

// vlxHub is a running hub on one layout, with the operator of the shared stream and the operator
// of the account VLX's stream sits in (the same account where VLX has no store of its own).
type vlxHub struct {
	opts         vlxOptions
	server       *natsserver.Server
	url          string
	configPath   string
	head         string
	keys         testnats.CalloutKeys
	operatorKey  string
	operatorSeed string
	log          *vlxServerLog
	callout      *placingCallout
	shared       natsgo.JetStreamContext
	sharedConn   *natsgo.Conn
	vlx          natsgo.JetStreamContext
	vlxConn      *natsgo.Conn
}

func (h *vlxHub) writeConfig(t *testing.T, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(h.configPath, []byte(h.head+text), 0o600))
}

// startVLXHub starts nats-server on layout o, the stand-in auth service placing each of clients by
// its side, and connects both operators.
func startVLXHub(t *testing.T, o vlxOptions, clients map[string]vlxClient) *vlxHub {
	t.Helper()
	keys := testnats.NewCalloutKeys(t)
	operatorSeed, operatorKey := testnats.User(t)
	dir := t.TempDir()
	h := &vlxHub{
		opts: o, keys: keys, operatorKey: operatorKey, operatorSeed: operatorSeed, log: &vlxServerLog{},
		callout:    &placingCallout{tokens: map[string]placed{}, requests: map[string]int{}, named: !o.unnamedUsers},
		configPath: filepath.Join(dir, "nats-server.conf"),
		head:       fmt.Sprintf("listen: \"127.0.0.1:-1\"\njetstream: {store_dir: %q}\n", filepath.Join(dir, "jetstream")),
	}
	h.writeConfig(t, vlxConfigText(t, keys, operatorKey, o))
	for token, c := range clients {
		h.callout.set(token, placed{o.account(c.side), c.grant})
	}
	h.callout.set(vlxOperatorToken, placed{o.account(sideVLXStore), everything})
	h.launch(t)
	return h
}

// launch starts nats-server on the hub's configuration file and its JetStream store, the stand-in
// auth service, and both operators' connections.
func (h *vlxHub) launch(t *testing.T) {
	t.Helper()
	opts, err := natsserver.ProcessConfigFile(h.configPath)
	require.NoError(t, err, "nats-server refuses the configuration")
	opts.NoSigs, opts.NoLog = true, true
	server, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	server.SetLogger(h.log, false, false)
	go server.Start()
	t.Cleanup(func() {
		server.Shutdown()
		server.WaitForShutdown()
	})
	require.True(t, server.ReadyForConnections(10*time.Second), "nats-server did not start")
	h.server, h.url = server, server.ClientURL()
	h.callout.serve(t, h.url, h.keys)
	h.sharedConn, err = testnats.ConnectSeed(t, h.url, h.operatorSeed)
	require.NoError(t, err)
	t.Cleanup(h.sharedConn.Close)
	h.shared, err = h.sharedConn.JetStream()
	require.NoError(t, err)
	h.vlxConn, _ = h.connect(t, vlxOperatorToken)
	h.vlx, err = h.vlxConn.JetStream()
	require.NoError(t, err)
}

// restart stops the server and starts it again on the same configuration file and store.
func (h *vlxHub) restart(t *testing.T) {
	t.Helper()
	h.server.Shutdown()
	h.server.WaitForShutdown()
	h.launch(t)
}

// connect admits token, with the inbox prefix its grant subscribes and its token as its name.
func (h *vlxHub) connect(t *testing.T, token string, extra ...natsgo.Option) (*natsgo.Conn, *publishRefusals) {
	t.Helper()
	conn, refused, err := h.tryConnect(t, token, extra...)
	require.NoError(t, err, "the auth service did not admit %s", token)
	return conn, refused
}

func (h *vlxHub) tryConnect(t *testing.T, token string, extra ...natsgo.Option) (*natsgo.Conn, *publishRefusals, error) {
	t.Helper()
	at, _ := h.callout.lookup(token)
	refused := &publishRefusals{}
	options := []natsgo.Option{natsgo.Token(token), natsgo.Name(token), natsgo.CustomInboxPrefix(inboxPrefix(at.grant)),
		natsgo.NoReconnect(), natsgo.Timeout(5 * time.Second), natsgo.ErrorHandler(refused.handler)}
	conn, err := natsgo.Connect(h.url, append(options, extra...)...)
	if err == nil {
		t.Cleanup(conn.Close)
	}
	return conn, refused, err
}

// accountOf is the account the server reports token's connection in (Connz).
func (h *vlxHub) accountOf(t *testing.T, token string) string {
	t.Helper()
	connz, err := h.server.Connz(&natsserver.ConnzOptions{Limit: 1024, Username: true})
	require.NoError(t, err)
	for _, c := range connz.Conns {
		if c.Name == token {
			if c.Account == "" {
				return natsserver.DEFAULT_GLOBAL_ACCOUNT
			}
			return c.Account
		}
	}
	return "(no connection named " + token + ")"
}

// setUp makes the notification stream and the two buckets in the shared JetStream account and,
// where VLX has a store of its own, the same names there; and stores one public project's event on
// the shared stream, so a push consumer there has a message to deliver (as #4851's setUp does).
func (h *vlxHub) setUp(t *testing.T) {
	t.Helper()
	stores := []natsgo.JetStreamContext{h.shared}
	if h.opts.vlxHasOwnStore() {
		stores = append(stores, h.vlx)
	}
	for _, js := range stores {
		_, err := js.AddStream(&natsgo.StreamConfig{Name: bus.Stream, Subjects: bus.StreamCarriedSubjects()})
		require.NoError(t, err)
		for _, bucket := range []string{bus.InterestBucket, sessionConsumersBucket} {
			_, err := js.CreateKeyValue(&natsgo.KeyValueConfig{Bucket: bucket})
			require.NoError(t, err)
		}
	}
	_, err := h.shared.Publish("notifications.dispatch.issue.PUB.1.created", []byte(`{"event":1}`))
	require.NoError(t, err)
}

func storedIn(t *testing.T, js natsgo.JetStreamContext, stream, subject string) uint64 {
	t.Helper()
	info, err := js.StreamInfo(stream, &natsgo.StreamInfoRequest{SubjectsFilter: subject})
	require.NoError(t, err)
	return info.State.Subjects[subject]
}

// subjectsIn are the subjects stream holds, with their counts, as js reads them.
func subjectsIn(t *testing.T, js natsgo.JetStreamContext, stream string) map[string]uint64 {
	t.Helper()
	info, err := js.StreamInfo(stream, &natsgo.StreamInfoRequest{SubjectsFilter: ">"})
	require.NoError(t, err)
	return info.State.Subjects
}

// fence returns once each stream of js's account named here has stored everything that reached
// it before this call: a stream stores what it takes in in order, so its acknowledgement of a
// publish of the operator's own comes after every message queued before it.
func fence(t *testing.T, js natsgo.JetStreamContext) {
	t.Helper()
	_, err := js.Publish("notifications.dispatch.issue.FENCE.1.fence", []byte("fence"))
	require.NoError(t, err)
	kv, err := js.KeyValue(sessionConsumersBucket)
	require.NoError(t, err)
	_, err = kv.Put("fence", []byte("fence"))
	require.NoError(t, err)
}

// observer connects token and returns what it has seen on each subject it subscribes.
func (h *vlxHub) observer(t *testing.T, token string, subjects ...string) func(subject string) *natsgo.Msg {
	t.Helper()
	conn, _ := h.connect(t, token)
	var mu sync.Mutex
	seen := map[string]*natsgo.Msg{}
	for _, subject := range subjects {
		_, err := conn.Subscribe(subject, func(m *natsgo.Msg) {
			mu.Lock()
			defer mu.Unlock()
			seen[m.Subject] = m
		})
		require.NoError(t, err)
	}
	require.NoError(t, conn.Flush())
	return func(subject string) *natsgo.Msg {
		mu.Lock()
		defer mu.Unlock()
		return seen[subject]
	}
}

// storedReading is one probe's target: the class that aimed an answer there, how, and the VLX
// stream and subject it was aimed at.
type storedReading struct {
	class, route, stream, subject string
	stored                        uint64
}

// probeVLXTargets runs #4851's probes of one class, connected in the shared projects' account,
// with every answer aimed at a subject VLX's stream or bucket captures: its issue subject, a key
// of its session-consumer bucket and a GitHub subject its stream imports; the DIRECT.GET classes'
// chosen bytes; and a push consumer's delivery. It returns the targets once each answer is seen
// sent, in the observer's account.
func probeVLXTargets(t *testing.T, hub *vlxHub, seen func(string) *natsgo.Msg, class grantClass) []storedReading {
	t.Helper()
	conn, refused := hub.connect(t, class.name)
	lower := strings.ToLower(class.name)
	kvStream := "KV_" + sessionConsumersBucket
	ordinary := "$JS.API.STREAM.INFO." + bus.Stream
	if !permits(class.grant, ordinary) {
		require.NoError(t, conn.PublishRequest(ordinary, vlxIssue+lower, nil))
		require.NoError(t, conn.Flush())
		testwait.Eventually(t, class.name+"'s request refused", func() bool { return refused.saw(ordinary) })
		return nil
	}
	answer, err := conn.Request(ordinary, nil, 5*time.Second)
	require.NoError(t, err, "%s's ordinary request, which its grant permits, was not answered", class.name)
	var info struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(answer.Data, &info))
	require.Equal(t, "io.nats.jetstream.api.v1.stream_info_response", info.Type, "%s: %s", class.name, answer.Data)

	targets := []storedReading{
		{class.name, "reply", bus.Stream, vlxIssue + lower, 0},
		{class.name, "reply", kvStream, "$KV." + sessionConsumersBucket + "." + lower, 0},
		{class.name, "reply", bus.Stream, "notifications.github.acme.one." + lower, 0},
	}
	for _, target := range targets {
		require.NoError(t, conn.PublishRequest(ordinary, target.subject, nil))
	}
	require.NoError(t, conn.Flush())
	for _, target := range targets {
		testwait.Eventually(t, "the answer to "+class.name+"'s request sent to "+target.subject, func() bool { return seen(target.subject) != nil })
	}

	direct := "$JS.API.DIRECT.GET.KV_" + bus.InterestBucket + ".$KV." + bus.InterestBucket + ".chosen-" + lower
	if permits(class.grant, "$KV."+bus.InterestBucket+".chosen-"+lower) && permits(class.grant, direct) {
		js, err := conn.JetStream()
		require.NoError(t, err)
		kv, err := js.KeyValue(bus.InterestBucket)
		require.NoError(t, err)
		_, err = kv.Put("chosen-"+lower, []byte(`{"forged":"`+class.name+`"}`))
		require.NoError(t, err, "%s writes a key it holds", class.name)
		chosen := []storedReading{
			{class.name, "direct-get", bus.Stream, vlxIssue + "d" + lower, 0},
			{class.name, "direct-get", bus.Stream, "notifications.github.acme.one.d" + lower, 0},
		}
		for _, target := range chosen {
			require.NoError(t, conn.PublishRequest(direct, target.subject, nil))
		}
		require.NoError(t, conn.Flush())
		for _, target := range chosen {
			testwait.Eventually(t, "the DIRECT.GET answer sent to "+target.subject, func() bool { return seen(target.subject) != nil })
		}
		targets = append(targets, chosen...)
	}

	if stream, name, create := consumerCreate(class.grant, class.name); create != "" {
		target := storedReading{class.name, "push", kvStream, "$KV." + sessionConsumersBucket + ".p" + lower, 0}
		if stream != bus.Stream {
			target = storedReading{class.name, "push", bus.Stream, "notifications.github.acme.one.p" + lower, 0}
		}
		// As #4851's row: a push consumer delivers only while its deliver subject has a literal
		// subscriber in its own account, so the shared operator subscribes it there.
		delivered, err := hub.sharedConn.SubscribeSync(target.subject)
		require.NoError(t, err)
		require.NoError(t, hub.sharedConn.Flush())
		body, err := json.Marshal(map[string]any{"stream_name": stream, "config": map[string]any{
			"durable_name": name, "deliver_subject": target.subject, "ack_policy": "none", "deliver_policy": "all",
		}})
		require.NoError(t, err)
		created, err := conn.Request(create, body, 5*time.Second)
		require.NoError(t, err, "%s's consumer create was not answered", class.name)
		require.NotContains(t, string(created.Data), `"error"`, "%s's consumer create: %s", class.name, created.Data)
		_, err = delivered.NextMsg(testwait.Bounded(t, time.Minute, time.Second))
		require.NoError(t, err, "%s's push consumer delivered nothing to %s", class.name, target.subject)
		require.NoError(t, delivered.Unsubscribe())
		// Deleted at once: two classes' probes may name the same listener durable.
		require.NoError(t, hub.shared.DeleteConsumer(stream, name))
		targets = append(targets, target)
	}
	return targets
}

// TestNoJetStreamAnswerIsStoredOnAVLXSubject: row 44c from outside VLX. Each grant class of
// #4851's row connects in the shared projects' account and aims every answer it can cause at a
// subject VLX's stream or bucket captures; the auth service's own user tries the same request.
// After the answers are seen sent and VLX's store is fenced, no VLX subject holds one.
//
// Control: PROTO_VLX_LAYOUT=mergebase (every user in $G, the accepted day-one defect), under which
// the answers are stored and the row fails naming each target; PROTO_VLX_LAYOUT=shared reads the
// same probe on #4851's layout.
func TestNoJetStreamAnswerIsStoredOnAVLXSubject(t *testing.T) {
	o := vlxOptionsFromEnv(t)
	classes := grantClasses(t)
	clients := map[string]vlxClient{"observer": {sideEnvoy, everything}}
	for _, class := range classes {
		clients[class.name] = vlxClient{sideEnvoy, class.grant}
	}
	hub := startVLXHub(t, o, clients)
	hub.setUp(t)
	seen := hub.observer(t, "observer", vlxIssue+">", "$KV."+sessionConsumersBucket+".>", gitHubAll)

	var readings []storedReading
	refusals := 0
	for _, class := range classes {
		got := probeVLXTargets(t, hub, seen, class)
		if got == nil {
			refusals++
			t.Logf("%s: holds no JetStream API subject; its request was refused", class.name)
		}
		readings = append(readings, got...)
	}

	// The auth service's own user: its grant publishes only $SYS._INBOX.>, so its request is refused.
	service, err := testnats.ConnectSeed(t, hub.url, hub.keys.ServiceSeed)
	require.NoError(t, err)
	t.Cleanup(service.Close)
	serviceRefused := &publishRefusals{}
	service.SetErrorHandler(serviceRefused.handler)
	ordinary := "$JS.API.STREAM.INFO." + bus.Stream
	require.NoError(t, service.PublishRequest(ordinary, vlxIssue+"callout", nil))
	require.NoError(t, service.Flush())
	testwait.Eventually(t, "the auth service user's request refused", func() bool { return serviceRefused.saw(ordinary) })
	t.Logf("the auth service's user: its request with the reply aimed at %scallout was refused: %v", vlxIssue, serviceRefused.seen)

	require.NotEmpty(t, readings, "UNKNOWN: no class aimed an answer at a VLX subject")
	fence(t, hub.vlx)
	// The route control and the GitHub targets' fence: a plain publish of the shared projects' on a
	// GitHub subject, sent after every answer was seen, does reach VLX's stream through the import,
	// so a GitHub target that holds nothing is not a route that carries nothing.
	if o.vlxHasOwnStore() {
		control := "notifications.github.acme.one.routecontrol"
		observer, _ := hub.connect(t, "observer")
		require.NoError(t, observer.Publish(control, []byte("route control")))
		require.NoError(t, observer.Flush())
		testwait.Eventually(t, "the route control's plain publish stored in VLX's stream", func() bool {
			return storedIn(t, hub.vlx, bus.Stream, control) == 1
		})
		t.Logf("route control: a plain publish in %s on %s is stored in VLX's stream", o.account(sideEnvoy), control)
	}
	var storedOn []string
	for i := range readings {
		r := &readings[i]
		r.stored = storedIn(t, hub.vlx, r.stream, r.subject)
		t.Logf("reading %-12s %-10s %-26s %-50s stored %d", r.class, r.route, r.stream, r.subject, r.stored)
		if r.stored > 0 {
			storedOn = append(storedOn, fmt.Sprintf("%s's %s answer stored on %s (%s): %d", r.class, r.route, r.subject, r.stream, r.stored))
		}
	}
	t.Logf("%d readings, %d refusal-only classes, %d targets stored", len(readings), refusals, len(storedOn))
	require.Empty(t, storedOn, "a JetStream answer is stored on a VLX subject in layout %s", o.layout)
}

// TestVLXNeedsAJetStreamAccountOfItsOwn: row 44c inside VLX. The edge listener's user, on the
// widest listener grant (ListenerHost), sits in VLX and aims a JetStream answer at VLX's own issue
// subject. With VLX's pair (VLX_JETSTREAM beside VLX) nothing is stored; with JetStream on in VLX
// itself (vlx-inline), the answer is stored, so the pair is what closes it.
func TestVLXNeedsAJetStreamAccountOfItsOwn(t *testing.T) {
	for _, layout := range []struct {
		layout vlxLayout
		want   uint64
	}{{layoutVLX, 0}, {layoutVLXInline, 1}} {
		t.Run(string(layout.layout), func(t *testing.T) {
			o := defaultVLXOptions()
			o.layout = layout.layout
			hub := startVLXHub(t, o, map[string]vlxClient{
				"edge":     {sideVLX, natsgrant.ListenerHost("edge")},
				"observer": {sideVLX, everything},
			})
			hub.setUp(t)
			seen := hub.observer(t, "observer", vlxIssue+">")
			edge, _ := hub.connect(t, "edge")
			target := vlxIssue + "edge"
			require.NoError(t, edge.PublishRequest("$JS.API.STREAM.INFO."+bus.Stream, target, nil))
			require.NoError(t, edge.Flush())
			testwait.Eventually(t, "the answer sent to "+target, func() bool { return seen(target) != nil })
			fence(t, hub.vlx)
			got := storedIn(t, hub.vlx, bus.Stream, target)
			t.Logf("%s: the edge's answer aimed at %s: stored %d (the answer was %d bytes)", layout.layout, target, got, len(seen(target).Data))
			require.Equal(t, layout.want, got)
		})
	}
}

// edgeReach is what the edge listener's user reached of the shared projects, each entry naming
// the route and the subject: the O-16 reading.
type edgeReach struct {
	leaks   []string
	notes   []string
	vlxSeen uint64
	repos   map[string]uint64
}

// edgeReaches runs O-16's probe on hub: the edge listener's user, on the widest listener grant,
// as a client of VLX; Dispatch publishing INFRA's event on its shared connection and VLX's on its
// VLX connection; the shared listener publishing two repositories' GitHub events.
func edgeReaches(t *testing.T, hub *vlxHub) edgeReach {
	t.Helper()
	reach := edgeReach{repos: map[string]uint64{}}
	edge, _ := hub.connect(t, "edge")
	ejs, err := edge.JetStream()
	require.NoError(t, err)
	coreSub, err := edge.SubscribeSync("notifications.>")
	require.NoError(t, err)
	watcher, _ := hub.connect(t, "watcher")
	agentSeen, err := watcher.SubscribeSync("notifications.agent.>")
	require.NoError(t, err)
	require.NoError(t, edge.Flush())
	require.NoError(t, watcher.Flush())

	consumers := map[string]string{
		"edge-vlx":    vlxIssue + ">",
		"edge-infra":  infraIssue + ">",
		"edge-github": gitHubAll,
		"edge-all":    "",
	}
	for name, filter := range consumers {
		_, err := ejs.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{Durable: name, FilterSubject: filter, AckPolicy: natsgo.AckExplicitPolicy})
		require.NoError(t, err, "the edge's consumer %s on %q", name, filter)
	}
	_, err = ejs.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{Durable: "edge-wide", FilterSubject: "notifications.>", AckPolicy: natsgo.AckExplicitPolicy})
	reach.notes = append(reach.notes, fmt.Sprintf("a consumer on notifications.> (beyond the stream's subjects): err=%v", err))

	dispatchVLX, _ := hub.connect(t, "dispatch-vlx")
	vjs, err := dispatchVLX.JetStream()
	require.NoError(t, err)
	_, err = vjs.Publish(vlxIssue+"comment.created", []byte(`{"project":"VLX"}`))
	require.NoError(t, err)

	github, _ := hub.connect(t, "github")
	gjs, err := github.JetStream()
	require.NoError(t, err)
	for _, repo := range []string{"acme.one", "acme.two"} {
		_, err := gjs.Publish("notifications.github."+repo+".push", []byte(repo))
		require.NoError(t, err)
	}
	dispatch, _ := hub.connect(t, "dispatch")
	djs, err := dispatch.JetStream()
	require.NoError(t, err)
	_, err = djs.Publish(infraIssue+"comment.created", []byte(`{"project":"INFRA"}`))
	require.NoError(t, err)
	// The fence of the import route: after INFRA's event, on the same connection, a GitHub event
	// that does reach VLX. Once the edge's GitHub consumer holds it, what Dispatch's connection sent
	// before has been through every route it could take.
	_, err = djs.Publish("notifications.github.acme.two.fence", []byte("fence"))
	require.NoError(t, err)
	testwait.Eventually(t, "the edge's GitHub consumer holds three events (two repositories and the fence)", func() bool {
		info, err := ejs.ConsumerInfo(bus.Stream, "edge-github")
		return err == nil && info.NumPending >= 3
	})

	// The old-form agent send: from the edge, to an INFRA session's subject.
	agent := "notifications.agent.infra-s1"
	require.NoError(t, edge.Publish(agent, []byte("from the edge")))
	require.NoError(t, edge.Flush())
	require.NoError(t, watcher.Flush())
	require.NoError(t, edge.Flush())

	if n, _, _ := agentSeen.Pending(); n > 0 {
		reach.leaks = append(reach.leaks, fmt.Sprintf("agent send %s received by a subscriber of the shared projects' account (%d)", agent, n))
	}
	if hub.opts.vlxHasOwnStore() {
		_, err = hub.shared.Publish("notifications.dispatch.issue.FENCE.1.fence", []byte("fence"))
		require.NoError(t, err)
		if n := storedIn(t, hub.shared, bus.Stream, agent); n > 0 {
			reach.leaks = append(reach.leaks, fmt.Sprintf("agent send %s stored in the shared stream (%d)", agent, n))
		}
	}

	// The core subscription on notifications.>.
	for {
		m, err := coreSub.NextMsg(0)
		if err != nil {
			break
		}
		if !strings.HasPrefix(m.Subject, vlxIssue) && !strings.HasPrefix(m.Subject, "notifications.github.") && m.Subject != agent {
			reach.leaks = append(reach.leaks, "core subscription notifications.> received "+m.Subject)
		}
	}
	// The consumers, as the server counts them.
	for name := range consumers {
		info, err := ejs.ConsumerInfo(bus.Stream, name)
		require.NoError(t, err)
		reach.notes = append(reach.notes, fmt.Sprintf("consumer %s (filter %q): num_pending %d", name, consumers[name], info.NumPending))
		switch name {
		case "edge-vlx":
			reach.vlxSeen = info.NumPending
		case "edge-infra":
			if info.NumPending > 0 {
				reach.leaks = append(reach.leaks, fmt.Sprintf("consumer on %s holds %d", infraIssue+">", info.NumPending))
			}
		}
	}
	// Every subject the edge's own stream holds, read with the edge's own STREAM.INFO.
	for subject, n := range subjectsIn(t, ejs, bus.Stream) {
		switch {
		case strings.HasPrefix(subject, "notifications.github."):
			parts := strings.Split(subject, ".")
			reach.repos[parts[2]+"/"+parts[3]] += n
		case strings.HasPrefix(subject, vlxIssue), subject == agent:
		default:
			reach.leaks = append(reach.leaks, fmt.Sprintf("the edge's stream holds %s (%d)", subject, n))
		}
	}
	// A stored-message get of INFRA's subject.
	if msg, err := ejs.GetLastMsg(bus.Stream, infraIssue+"comment.created"); err == nil {
		reach.leaks = append(reach.leaks, fmt.Sprintf("STREAM.MSG.GET of %s answered %s", msg.Subject, msg.Data))
	} else {
		reach.notes = append(reach.notes, fmt.Sprintf("STREAM.MSG.GET of %s: %v", infraIssue+"comment.created", err))
	}
	sort.Strings(reach.leaks)
	sort.Strings(reach.notes)
	return reach
}

func edgeClients() map[string]vlxClient {
	return map[string]vlxClient{
		"edge":         {sideVLX, natsgrant.ListenerHost("edge")},
		"dispatch":     {sideEnvoy, natsgrant.Dispatch("dispatch")},
		"dispatch-vlx": {sideVLX, natsgrant.Dispatch("dispatch")},
		"github":       {sideEnvoy, natsgrant.Listener("gke")},
		"watcher":      {sideEnvoy, natsgrant.Host("infra-host")},
	}
}

// TestTheEdgeInVLXReachesNothingOfTheSharedProjects: O-16's check in the account layout. The
// edge listener's user, on the widest listener grant, sits in VLX. Its consumer on VLX's issue
// subject holds VLX's event; its consumer on INFRA's issue subject, its consumer with no filter,
// its core subscription on notifications.>, its stream's subjects and its stored-message get of
// INFRA's event reach nothing of INFRA; its old-form agent send to an INFRA session reaches no
// subscriber of the shared projects' account and is stored in no shared stream.
//
// Control: PROTO_VLX_LAYOUT=shared (the edge's user in ENVOY, today's layout), under which the
// edge reaches INFRA's event and the row fails naming the subject.
func TestTheEdgeInVLXReachesNothingOfTheSharedProjects(t *testing.T) {
	o := vlxOptionsFromEnv(t)
	hub := startVLXHub(t, o, edgeClients())
	hub.setUp(t)
	reach := edgeReaches(t, hub)
	for _, note := range reach.notes {
		t.Logf("server: %s", note)
	}
	t.Logf("GitHub events in the edge's stream by repository: %v", reach.repos)
	t.Logf("the edge's user is in %s", hub.accountOf(t, "edge"))
	require.Equal(t, uint64(1), reach.vlxSeen, "UNKNOWN: the edge's consumer on VLX's issue subject does not hold VLX's event")
	require.Empty(t, reach.leaks, "the edge's user, in %s, reached the shared projects", hub.accountOf(t, "edge"))
}

// TestAGitHubImportCarriesEveryRepositoryItsSubjectNames: measurement (d). The shared listener
// publishes two repositories' GitHub events in ENVOY; VLX's stream holds each repository its
// import's subject names: both under notifications.github.>, one under an import narrowed to
// acme/one.
func TestAGitHubImportCarriesEveryRepositoryItsSubjectNames(t *testing.T) {
	for _, row := range []struct {
		subject string
		want    map[string]uint64
	}{
		{gitHubAll, map[string]uint64{"acme/one": 1, "acme/two": 1}},
		{"notifications.github.acme.one.>", map[string]uint64{"acme/one": 1}},
	} {
		t.Run(row.subject, func(t *testing.T) {
			o := vlxOptionsFromEnv(t)
			o.githubSubject = row.subject
			hub := startVLXHub(t, o, map[string]vlxClient{"github": {sideEnvoy, natsgrant.Listener("gke")}})
			hub.setUp(t)
			github, _ := hub.connect(t, "github")
			gjs, err := github.JetStream()
			require.NoError(t, err)
			for _, repo := range []string{"acme.one", "acme.two"} {
				_, err := gjs.Publish("notifications.github."+repo+".push", []byte(repo))
				require.NoError(t, err)
			}
			// The fence: a third event, on acme/one, which every import here carries, published last.
			_, err = gjs.Publish("notifications.github.acme.one.fence", []byte("fence"))
			require.NoError(t, err)
			testwait.Eventually(t, "VLX's stream holding the fence", func() bool {
				return storedIn(t, hub.vlx, bus.Stream, "notifications.github.acme.one.fence") == 1
			})
			repos := map[string]uint64{}
			for subject, n := range subjectsIn(t, hub.vlx, bus.Stream) {
				if parts := strings.Split(subject, "."); strings.HasPrefix(subject, "notifications.github.") && parts[4] == "push" {
					repos[parts[2]+"/"+parts[3]] += n
				}
			}
			t.Logf("import %s: VLX's stream holds the push events of %v", row.subject, repos)
			require.Equal(t, row.want, repos)
		})
	}
}

// TestOneConnectionWorksInOneAccount: measurement (c). A session whose token lists VLX and INFRA
// is admitted once and placed in one account. Placed in VLX, its connection receives the frame the
// edge relays on its delivery subject and not the one the shared listener relays, and a daemon
// grant placed there reads VLX's event and not INFRA's; placed in ENVOY, the reverse.
//
// Control: PROTO_VLX_LAYOUT=shared, where both placements are ENVOY: the VLX placement then
// receives INFRA's frame and the row fails naming it.
func TestOneConnectionWorksInOneAccount(t *testing.T) {
	session, err := natsgrant.Session("s1")
	require.NoError(t, err)
	for _, placement := range []struct {
		name       string
		side       side
		own, other string
	}{
		{"placed in VLX", sideVLX, "VLX", "INFRA"},
		{"placed in ENVOY", sideEnvoy, "INFRA", "VLX"},
	} {
		t.Run(placement.name, func(t *testing.T) {
			o := vlxOptionsFromEnv(t)
			clients := edgeClients()
			clients["listener"] = vlxClient{sideEnvoy, natsgrant.Listener("gke")}
			clients["both-session"] = vlxClient{placement.side, session}
			clients["both-daemon"] = vlxClient{placement.side, natsgrant.Daemon()}
			hub := startVLXHub(t, o, clients)
			hub.setUp(t)
			conn, _ := hub.connect(t, "both-session")
			frames, err := conn.SubscribeSync(bus.DeliverSubject("s1"))
			require.NoError(t, err)
			require.NoError(t, conn.Flush())
			listener, _ := hub.connect(t, "listener")
			edge, _ := hub.connect(t, "edge")
			require.NoError(t, listener.Publish(bus.DeliverSubject("s1"), []byte("INFRA")))
			require.NoError(t, edge.Publish(bus.DeliverSubject("s1"), []byte("VLX")))
			require.NoError(t, listener.Flush())
			require.NoError(t, edge.Flush())
			require.NoError(t, conn.Flush())
			got := map[string]bool{}
			for {
				m, err := frames.NextMsg(0)
				if err != nil {
					break
				}
				got[string(m.Data)] = true
			}

			dispatch, _ := hub.connect(t, "dispatch")
			djs, err := dispatch.JetStream()
			require.NoError(t, err)
			_, err = djs.Publish(infraIssue+"comment.created", []byte("INFRA"))
			require.NoError(t, err)
			dispatchVLX, _ := hub.connect(t, "dispatch-vlx")
			vjs, err := dispatchVLX.JetStream()
			require.NoError(t, err)
			_, err = vjs.Publish(vlxIssue+"comment.created", []byte("VLX"))
			require.NoError(t, err)
			daemon, _ := hub.connect(t, "both-daemon")
			daemonJS, err := daemon.JetStream()
			require.NoError(t, err)
			read := map[string]bool{}
			for project, subject := range map[string]string{"INFRA": infraIssue + "comment.created", "VLX": vlxIssue + "comment.created"} {
				msg, err := daemonJS.GetLastMsg(bus.Stream, subject)
				read[project] = err == nil && string(msg.Data) == project
				t.Logf("%s: the daemon grant's stored-message get of %s: err=%v", placement.name, subject, err)
			}
			t.Logf("%s (server: %s): frames received %v; stored events read %v", placement.name, hub.accountOf(t, "both-session"), got, read)
			require.True(t, got[placement.own] && read[placement.own], "UNKNOWN: the connection placed for %s reached nothing of %s", placement.own, placement.own)
			require.False(t, got[placement.other], "the session's one connection received %s's frame on %s", placement.other, bus.DeliverSubject("s1"))
			require.False(t, read[placement.other], "the daemon grant's one connection read %s's stored event", placement.other)
		})
	}
}

// TestTheCalloutPlacesEachTokenInTheAccountItNames: measurement (e). One auth service places each
// token in the account its answer names, VLX or ENVOY, with allowed_accounts unset and with it
// listing both, as the server's own connection list reports; it cannot place one in an account the
// configuration lacks; the configuration cannot list one in allowed_accounts; and it can place one
// in VLX_JETSTREAM, where VLX's stream sits and row 44c is open.
func TestTheCalloutPlacesEachTokenInTheAccountItNames(t *testing.T) {
	allowed := func(t *testing.T, text string) string {
		return replaceOnce(t, text, "    account: \"ENVOY\"\n", "    account: \"ENVOY\"\n    allowed_accounts: [\"ENVOY\", \"VLX\"]\n")
	}
	for _, row := range []struct {
		name string
		edit func(t *testing.T, text string) string
	}{{"allowed_accounts unset", nil}, {"allowed_accounts lists ENVOY and VLX", allowed}} {
		t.Run(row.name, func(t *testing.T) {
			o := defaultVLXOptions()
			o.edit = row.edit
			hub := startVLXHub(t, o, map[string]vlxClient{
				"to-vlx":   {sideVLX, natsgrant.Dispatch("dispatch")},
				"to-envoy": {sideEnvoy, natsgrant.Dispatch("dispatch")},
			})
			hub.setUp(t)
			for token, subject := range map[string]string{"to-vlx": vlxIssue + "placed", "to-envoy": infraIssue + "placed"} {
				conn, _ := hub.connect(t, token)
				js, err := conn.JetStream()
				require.NoError(t, err)
				_, err = js.Publish(subject, []byte(token))
				require.NoError(t, err)
			}
			placedIn := map[string]string{"to-vlx": hub.accountOf(t, "to-vlx"), "to-envoy": hub.accountOf(t, "to-envoy")}
			inVLX := [2]uint64{storedIn(t, hub.vlx, bus.Stream, vlxIssue+"placed"), storedIn(t, hub.vlx, bus.Stream, infraIssue+"placed")}
			inShared := [2]uint64{storedIn(t, hub.shared, bus.Stream, vlxIssue+"placed"), storedIn(t, hub.shared, bus.Stream, infraIssue+"placed")}
			t.Logf("%s: server places %v; VLX's stream holds [VLX INFRA] %v; the shared stream %v; callout requests to-vlx %d to-envoy %d",
				row.name, placedIn, inVLX, inShared, hub.callout.asked("to-vlx"), hub.callout.asked("to-envoy"))
			require.Equal(t, map[string]string{"to-vlx": vlxClientAccount, "to-envoy": core.ClientAccount}, placedIn)
			require.Equal(t, [2]uint64{1, 0}, inVLX)
			require.Equal(t, [2]uint64{0, 1}, inShared)
		})
	}

	t.Run("an account the configuration lacks", func(t *testing.T) {
		hub := startVLXHub(t, defaultVLXOptions(), nil)
		hub.callout.set("to-vly", placed{"VLY", natsgrant.Dispatch("dispatch")})
		mark := hub.log.mark()
		_, _, err := hub.tryConnect(t, "to-vly")
		require.Error(t, err)
		t.Logf("client error: %v", err)
		for _, line := range hub.log.since(mark) {
			t.Logf("server log: %s", line)
		}
	})

	t.Run("allowed_accounts naming an account the configuration lacks", func(t *testing.T) {
		keys := testnats.NewCalloutKeys(t)
		_, operatorKey := testnats.User(t)
		o := defaultVLXOptions()
		o.edit = func(t *testing.T, text string) string {
			return replaceOnce(t, text, "    account: \"ENVOY\"\n", "    account: \"ENVOY\"\n    allowed_accounts: [\"ENVOY\", \"VLY\"]\n")
		}
		path := filepath.Join(t.TempDir(), "nats-server.conf")
		require.NoError(t, os.WriteFile(path, []byte(vlxConfigText(t, keys, operatorKey, o)), 0o600))
		_, err := natsserver.ProcessConfigFile(path)
		require.Error(t, err)
		t.Logf("nats-server: %v", err)
	})

	t.Run("a client placed in VLX_JETSTREAM, where VLX's stream sits", func(t *testing.T) {
		hub := startVLXHub(t, defaultVLXOptions(), map[string]vlxClient{"observer": {sideVLXStore, everything}})
		hub.callout.set("in-store", placed{vlxJetStreamAccount, natsgrant.Listener("gke")})
		hub.setUp(t)
		seen := hub.observer(t, "observer", vlxIssue+">")
		conn, _ := hub.connect(t, "in-store")
		target := vlxIssue + "instore"
		require.NoError(t, conn.PublishRequest("$JS.API.STREAM.INFO."+bus.Stream, target, nil))
		require.NoError(t, conn.Flush())
		testwait.Eventually(t, "the answer sent to "+target, func() bool { return seen(target) != nil })
		fence(t, hub.vlx)
		got := storedIn(t, hub.vlx, bus.Stream, target)
		t.Logf("server places in-store in %s; its answer aimed at %s: stored %d", hub.accountOf(t, "in-store"), target, got)
		require.Equal(t, vlxJetStreamAccount, hub.accountOf(t, "in-store"))
		require.Equal(t, uint64(1), got)
	})
}

// connWatch records a connection's disconnects and closes.
type connWatch struct {
	mu     sync.Mutex
	events []string
}

func (w *connWatch) options() []natsgo.Option {
	return []natsgo.Option{
		natsgo.DisconnectErrHandler(func(_ *natsgo.Conn, err error) { w.add(fmt.Sprintf("disconnected: %v", err)) }),
		natsgo.ClosedHandler(func(c *natsgo.Conn) { w.add(fmt.Sprintf("closed: %v", c.LastError())) }),
	}
}

func (w *connWatch) add(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *connWatch) seen() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...)
}

// reloadTo writes layout o to the hub's configuration file and reloads it, logging the server's
// answer and every line it logged.
func (h *vlxHub) reloadTo(t *testing.T, name string, o vlxOptions) error {
	t.Helper()
	h.writeConfig(t, vlxConfigText(t, h.keys, h.operatorKey, o))
	mark := h.log.mark()
	err := h.server.Reload()
	t.Logf("%s: Reload() = %v", name, err)
	for _, line := range h.log.since(mark) {
		t.Logf("%s: server log: %s", name, line)
	}
	return err
}

// watchedConn is a connection the row reads across a reload, with its disconnects and closes.
type watchedConn struct {
	conn  *natsgo.Conn
	watch *connWatch
}

func (h *vlxHub) connectWatched(t *testing.T, token string) watchedConn {
	t.Helper()
	w := &connWatch{}
	conn, _ := h.connect(t, token, w.options()...)
	return watchedConn{conn, w}
}

// kept reports, as the server answers each connection's ping, which of conns are still connected.
func (h *vlxHub) kept(t *testing.T, name string, conns map[string]watchedConn) map[string]bool {
	t.Helper()
	kept := map[string]bool{}
	for token, c := range conns {
		err := c.conn.FlushTimeout(5 * time.Second)
		kept[token] = err == nil
		t.Logf("%s: %s ping=%v connected=%v last error=%v events=%v server account=%s callout requests=%d",
			name, token, err, c.conn.IsConnected(), c.conn.LastError(), c.watch.seen(), h.accountOf(t, token), h.callout.asked(token))
	}
	return kept
}

// TestAProjectAccountIsAddedByReload: measurement (a). A hub running #4851's layout, with clients of
// ENVOY admitted through the auth service (a core subscriber, a KV watch, a pull consumer and
// Dispatch), is given VLX's pair and the GitHub import by a configuration reload. Each row reads the
// reload's answer and log lines, which clients of ENVOY the server keeps, whether those kept still
// work, and whether VLX then works for new connections. Two rows differ only in the user JWT: the
// auth service's names its user (natscallout callout.go:697, decision.Name), which nats-server
// keeps as the connection's username (server/auth_callout.go:325-334); the control names none.
// Then a reload that also changes the auth_callout block, and one that removes VLX again.
func TestAProjectAccountIsAddedByReload(t *testing.T) {
	for _, row := range []struct {
		name    string
		unnamed bool
	}{
		{"the user JWT names the user, as the auth service's does", false},
		{"control: the user JWT names no user", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			before := defaultVLXOptions()
			before.layout = layoutShared
			before.unnamedUsers = row.unnamed
			hub := startVLXHub(t, before, map[string]vlxClient{
				"host":         {sideEnvoy, natsgrant.Host("infra-host")},
				"listener":     {sideEnvoy, natsgrant.Listener("gke")},
				"daemon":       {sideEnvoy, natsgrant.Daemon()},
				"dispatch":     {sideEnvoy, natsgrant.Dispatch("dispatch")},
				"github":       {sideEnvoy, natsgrant.Listener("gke")},
				"dispatch-vlx": {sideVLX, natsgrant.Dispatch("dispatch")},
			})
			hub.setUp(t)
			conns := map[string]watchedConn{}
			for _, token := range []string{"host", "listener", "daemon", "dispatch"} {
				conns[token] = hub.connectWatched(t, token)
			}
			coreSub, err := conns["host"].conn.SubscribeSync("notifications.dispatch.>")
			require.NoError(t, err)
			require.NoError(t, conns["host"].conn.Flush())
			ljs, err := conns["listener"].conn.JetStream()
			require.NoError(t, err)
			kv, err := ljs.KeyValue(bus.InterestBucket)
			require.NoError(t, err)
			watch, err := kv.WatchAll()
			require.NoError(t, err)
			t.Cleanup(func() { _ = watch.Stop() })
			require.Nil(t, testwait.Receive(t, "the watch's end of initial values", watch.Updates()))
			djs, err := conns["daemon"].conn.JetStream()
			require.NoError(t, err)
			pull, err := djs.PullSubscribe(infraIssue+">", "legion-go-INFRA-dispatch", natsgo.BindStream(bus.Stream))
			require.NoError(t, err)
			hub.kept(t, "before the reload", conns)

			after := defaultVLXOptions()
			after.unnamedUsers = row.unnamed
			require.NoError(t, hub.reloadTo(t, "adding VLX", after), "nats-server refused the reload that adds VLX")
			kept := hub.kept(t, "after the reload", conns)

			// VLX for new connections, after the reload.
			hub.callout.set("vlx-op-new", placed{vlxJetStreamAccount, everything})
			hub.callout.set("dispatch-vlx", placed{vlxClientAccount, natsgrant.Dispatch("dispatch")})
			afterReload := hub.vlxReadings(t, "reload")
			require.NoError(t, hub.reloadTo(t, "the same file again", after))
			afterSecond := hub.vlxReadings(t, "reload2")
			t.Logf("after a second reload of the same file: %d of %d VLX readings failed", failed(afterSecond), len(afterSecond))

			if !row.unnamed {
				for token := range conns {
					require.False(t, kept[token], "%s, admitted under a named user, was kept across the reload", token)
				}
			} else {
				for token := range conns {
					require.True(t, kept[token], "%s, admitted under no user name, was closed by the reload", token)
				}
				hub.sharedStillWorks(t, conns, coreSub, watch, pull)
			}

			// The same readings after a restart on the same file and store.
			hub.restart(t)
			afterRestart := hub.vlxReadings(t, "restart")
			for what, err := range afterRestart {
				require.NoError(t, err, "after a restart: %s", what)
			}
			t.Logf("after the reload: %d of %d VLX readings failed", failed(afterReload), len(afterReload))
			// Measured on nats-server v2.15.1: after the reload VLX_JETSTREAM takes its operator's
			// stream create and the GitHub stream import, but nothing VLX's clients send through
			// VLX's service imports is answered; after a restart on the same file, all of it is.
			require.NoError(t, afterReload["VLX_JETSTREAM's stream create (or find)"])
			require.NoError(t, afterReload["the GitHub event reaches VLX's stream"])
			require.Error(t, afterReload["dispatch-vlx's $JS.API.INFO through VLX's import"])
			require.Error(t, afterReload["dispatch-vlx's publish stored"])
		})
	}
}

func failed(readings map[string]error) int {
	n := 0
	for _, err := range readings {
		if err != nil {
			n++
		}
	}
	return n
}

// vlxReadings reads whether VLX works for new connections: its operator makes (or finds) its
// stream in VLX_JETSTREAM; Dispatch's VLX connection is answered through VLX's $JS.API.> import
// and its publish is stored; and the shared listener's GitHub event reaches VLX's stream within
// 10 s, where every run that stores it does in milliseconds.
func (h *vlxHub) vlxReadings(t *testing.T, tag string) map[string]error {
	t.Helper()
	readings := map[string]error{}
	defer func() {
		for what, err := range readings {
			t.Logf("after the %s: VLX reading %q: %v", tag, what, err)
		}
	}()
	op, _, err := h.tryConnect(t, "vlx-op-new")
	if readings["the VLX_JETSTREAM operator connects"] = err; err != nil {
		return readings
	}
	ojs, err := op.JetStream()
	require.NoError(t, err)
	_, err = ojs.AddStream(&natsgo.StreamConfig{Name: bus.Stream, Subjects: bus.StreamCarriedSubjects()})
	readings["VLX_JETSTREAM's stream create (or find)"] = err
	dv, _, err := h.tryConnect(t, "dispatch-vlx")
	if readings["dispatch-vlx connects"] = err; err == nil {
		readings["dispatch-vlx's $JS.API.INFO through VLX's import"] = apiAnswer(dv.Request("$JS.API.INFO", nil, 5*time.Second))
		dvjs, err := dv.JetStream()
		require.NoError(t, err)
		_, err = dvjs.Publish(vlxIssue+tag, []byte("VLX"))
		readings["dispatch-vlx's publish stored"] = err
	}
	gh, _, err := h.tryConnect(t, "github")
	require.NoError(t, err)
	ghjs, err := gh.JetStream()
	require.NoError(t, err)
	subject := "notifications.github.acme.one." + tag
	_, err = ghjs.Publish(subject, []byte("github"))
	require.NoError(t, err, "the GitHub event's publish in ENVOY")
	readings["the GitHub event reaches VLX's stream"] = errors.New("not stored within 10 s")
	if testwait.Holds(10*time.Second, 50*time.Millisecond, func() bool {
		info, err := ojs.StreamInfo(bus.Stream, &natsgo.StreamInfoRequest{SubjectsFilter: subject})
		return err == nil && info.State.Subjects[subject] == 1
	}) {
		readings["the GitHub event reaches VLX's stream"] = nil
	}
	return readings
}

// apiAnswer is a JetStream API request's error: the transport's, or the answer's own error field.
func apiAnswer(m *natsgo.Msg, err error) error {
	if err != nil {
		return err
	}
	var answer struct {
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := json.Unmarshal(m.Data, &answer); err != nil {
		return fmt.Errorf("unreadable answer %q: %w", m.Data, err)
	}
	if answer.Error != nil {
		return errors.New(answer.Error.Description)
	}
	return nil
}

// sharedStillWorks reads that the clients of ENVOY kept across a reload still work: Dispatch's
// publish is acknowledged and reaches the core subscriber, a KV put reaches the watch, and the
// daemon's pull consumer fetches it.
func (h *vlxHub) sharedStillWorks(t *testing.T, conns map[string]watchedConn, coreSub *natsgo.Subscription, watch natsgo.KeyWatcher, pull *natsgo.Subscription) {
	t.Helper()
	{
		dispatch, err := conns["dispatch"].conn.JetStream()
		require.NoError(t, err)
		_, err = dispatch.Publish(infraIssue+"comment.created", []byte("after the reload"))
		require.NoError(t, err, "Dispatch's publish after the reload")
		m, err := coreSub.NextMsg(testwait.Bounded(t, 10*time.Second, time.Second))
		require.NoError(t, err, "the core subscriber after the reload")
		require.Equal(t, infraIssue+"comment.created", m.Subject)
		dkv, err := dispatch.KeyValue(bus.InterestBucket)
		require.NoError(t, err)
		_, err = dkv.Put("after", []byte("reload"))
		require.NoError(t, err)
		entry := testwait.Receive(t, "the KV watch's update after the reload", watch.Updates())
		require.NotNil(t, entry)
		require.Equal(t, "after", entry.Key())
		msgs, err := pull.Fetch(1, natsgo.MaxWait(10*time.Second))
		require.NoError(t, err, "the daemon's fetch after the reload")
		require.Len(t, msgs, 1)
		t.Logf("after the reload: the kept clients' publish, core delivery, KV watch and fetch all work")
	}
}

// TestAReloadOfTheSameFileAndTheCalloutsClients: measurement (a), the part that is not VLX's. A
// reload of the unchanged file reloads the authorization and the accounts on #4851's layout as it
// is (no VLX) and on the merge base's layout alike, and closes each client the auth service
// admitted under a named user (nats-server keeps the name as the client's username,
// server/auth_callout.go:325-334, and finds no such configured user, reload.go:2413-2431).
// Control: the same reload keeps a client admitted under no user name.
func TestAReloadOfTheSameFileAndTheCalloutsClients(t *testing.T) {
	for _, row := range []struct {
		layout  vlxLayout
		unnamed bool
		kept    bool
	}{{layoutShared, false, false}, {layoutMergeBase, false, false}, {layoutMergeBase, true, true}} {
		t.Run(fmt.Sprintf("%s, unnamed %v", row.layout, row.unnamed), func(t *testing.T) {
			o := defaultVLXOptions()
			o.layout = row.layout
			o.unnamedUsers = row.unnamed
			hub := startVLXHub(t, o, map[string]vlxClient{"dispatch": {sideEnvoy, natsgrant.Dispatch("dispatch")}})
			conns := map[string]watchedConn{"dispatch": hub.connectWatched(t, "dispatch")}
			require.NoError(t, hub.reloadTo(t, "the same file", o))
			require.Equal(t, row.kept, hub.kept(t, "after reloading the same file", conns)["dispatch"])
		})
	}
}

// TestAReloadThatChangesTheCalloutOrRemovesVLX: measurement (a), its edges. A reload that also
// changes the auth_callout block is refused and keeps every client; a reload that removes VLX
// again closes VLX's clients and refuses their next admission.
func TestAReloadThatChangesTheCalloutOrRemovesVLX(t *testing.T) {
	t.Run("a reload that also changes the auth_callout block", func(t *testing.T) {
		o := defaultVLXOptions()
		o.unnamedUsers = true
		hub := startVLXHub(t, o, map[string]vlxClient{"dispatch": {sideEnvoy, natsgrant.Dispatch("dispatch")}})
		conns := map[string]watchedConn{"dispatch": hub.connectWatched(t, "dispatch")}
		o.edit = func(t *testing.T, text string) string {
			return replaceOnce(t, text, "    account: \"ENVOY\"\n", "    account: \"ENVOY\"\n    allowed_accounts: [\"ENVOY\", \"VLX\"]\n")
		}
		require.Error(t, hub.reloadTo(t, "changing auth_callout", o), "nats-server took a reload that changes the auth_callout block")
		require.True(t, hub.kept(t, "after the refused reload", conns)["dispatch"])
	})

	t.Run("removing VLX again", func(t *testing.T) {
		o := defaultVLXOptions()
		o.unnamedUsers = true
		hub := startVLXHub(t, o, map[string]vlxClient{
			"dispatch":     {sideEnvoy, natsgrant.Dispatch("dispatch")},
			"dispatch-vlx": {sideVLX, natsgrant.Dispatch("dispatch")},
		})
		hub.setUp(t)
		conns := map[string]watchedConn{"dispatch": hub.connectWatched(t, "dispatch"), "dispatch-vlx": hub.connectWatched(t, "dispatch-vlx")}
		vjs, err := conns["dispatch-vlx"].conn.JetStream()
		require.NoError(t, err)
		_, err = vjs.Publish(vlxIssue+"comment.created", []byte("VLX"))
		require.NoError(t, err)
		back := o
		back.layout = layoutShared
		require.NoError(t, hub.reloadTo(t, "removing VLX", back))
		kept := hub.kept(t, "after removing VLX", conns)
		if kept["dispatch-vlx"] {
			_, err := vjs.Publish(vlxIssue+"comment.created", []byte("VLX again"))
			t.Logf("after removing VLX: dispatch-vlx's publish: %v", err)
		}
		_, _, err = hub.tryConnect(t, "dispatch-vlx")
		t.Logf("after removing VLX: a new dispatch-vlx connection: %v", err)
	})
}

// kvWatchSees reports whether js's watch of the interest bucket receives a put that put makes.
func kvWatchSees(t *testing.T, js natsgo.JetStreamContext, put natsgo.KeyValue) bool {
	t.Helper()
	kv, err := js.KeyValue(bus.InterestBucket)
	require.NoError(t, err, "the cross-account bind of the interest bucket")
	watch, err := kv.WatchAll()
	require.NoError(t, err, "the cross-account watch create")
	t.Cleanup(func() { _ = watch.Stop() })
	select {
	case entry := <-watch.Updates():
		require.Nil(t, entry)
	case <-time.After(10 * time.Second):
		t.Logf("the watch never reported the end of its initial values")
		return false
	}
	_, err = put.Put("crossed", []byte("value"))
	require.NoError(t, err)
	// As #4851's watchSees: 5 s, where every row that receives it does in milliseconds.
	select {
	case entry := <-watch.Updates():
		return entry != nil && entry.Key() == "crossed"
	case <-time.After(5 * time.Second):
		return false
	}
}

// intoList is text with line added first to the list named list ("imports" or "exports") of the
// block of account.
func intoList(t *testing.T, text, account, list, line string) string {
	t.Helper()
	at := strings.Index(text, "  "+account+" {\n")
	require.GreaterOrEqual(t, at, 0, "no block of %s", account)
	end := strings.Index(text[at:], "\n  }\n")
	open := strings.Index(text[at:], "    "+list+": [\n")
	require.True(t, open > 0 && open < end, "the block of %s has no %s list:\n%s", account, list, text[at:at+end])
	open += at + len("    "+list+": [\n")
	return text[:open] + line + text[open:]
}

// crossImports is an edit that gives importer an import of exporter's JetStream API under to and,
// when inbox is not "", of a stream of exporter's deliveries under inbox, which exporter exports.
func crossImports(importer, exporter, to, inbox string) func(t *testing.T, text string) string {
	return func(t *testing.T, text string) string {
		t.Helper()
		text = intoList(t, text, importer, "imports", fmt.Sprintf("      {\"service\": {\"account\": %q, \"subject\": \"$JS.API.>\"}, \"to\": %q}\n", exporter, to+".>"))
		if inbox != "" {
			text = intoList(t, text, exporter, "exports", "      {\"stream\": \""+inbox+".>\"}\n")
			text = intoList(t, text, importer, "imports", "      "+streamImport(exporter, inbox+".>")+"\n")
		}
		return text
	}
}

// TestAKVWatchAcrossAccounts: measurement (b). A client of ENVOY watches VLX's interest bucket
// through an import of VLX_JETSTREAM's $JS.API.> under $JS.VLX.API.> and a stream import of its
// own inbox prefix; then a client of VLX watches ENVOY's bucket the same way. Each row also reads
// what the import gives the watcher beside the watch (VLX's stored events, or INFRA's), and
// whether an answer aimed through the import at a VLX subject is stored. A last row asks whether
// ENVOY may import _INBOX.> from VLX_JETSTREAM beside JETSTREAM's.
func TestAKVWatchAcrossAccounts(t *testing.T) {
	xvlx := natsgrant.Permissions{Publish: []string{"$JS.VLX.API.>"}, Subscribe: []string{"_INBOX_xvlx.>"}}
	xenvoy := natsgrant.Permissions{Publish: []string{"$JS.ENVOY.API.>"}, Subscribe: []string{"_INBOX_xenvoy.>"}}

	t.Run("ENVOY watches VLX's bucket", func(t *testing.T) {
		for _, row := range []struct {
			name  string
			inbox string
			want  bool
		}{{"with the inbox import", "_INBOX_xvlx", true}, {"control: no inbox import", "", false}} {
			t.Run(row.name, func(t *testing.T) {
				o := defaultVLXOptions()
				o.edit = crossImports(core.ClientAccount, vlxJetStreamAccount, "$JS.VLX.API", row.inbox)
				hub := startVLXHub(t, o, map[string]vlxClient{
					"xwatch":       {sideEnvoy, xvlx},
					"dispatch-vlx": {sideVLX, natsgrant.Dispatch("dispatch")},
					"observer":     {sideEnvoy, everything},
				})
				hub.setUp(t)
				conn, refused := hub.connect(t, "xwatch")
				js, err := conn.JetStream(natsgo.APIPrefix("$JS.VLX.API"))
				require.NoError(t, err)
				put, err := hub.vlx.KeyValue(bus.InterestBucket)
				require.NoError(t, err)
				got := kvWatchSees(t, js, put)
				t.Logf("%s: the watch from ENVOY of VLX's bucket got the update: %v; refusals %v", row.name, got, refused.seen)
				require.Equal(t, row.want, got)
				if !row.want {
					return
				}
				// What the import gives beside the watch: VLX's stored events.
				dv, _ := hub.connect(t, "dispatch-vlx")
				dvjs, err := dv.JetStream()
				require.NoError(t, err)
				_, err = dvjs.Publish(vlxIssue+"comment.created", []byte(`{"text":"VLX's comment"}`))
				require.NoError(t, err)
				msg, err := js.GetLastMsg(bus.Stream, vlxIssue+"comment.created")
				require.NoError(t, err)
				t.Logf("exposure: the ENVOY client's STREAM.MSG.GET through $JS.VLX.API read %s: %s", msg.Subject, msg.Data)
				// Row 44c through the import: an answer aimed at a VLX subject.
				seen := hub.observer(t, "observer", vlxIssue+">")
				target := vlxIssue + "xwatch"
				require.NoError(t, conn.PublishRequest("$JS.VLX.API.STREAM.INFO."+bus.Stream, target, nil))
				require.NoError(t, conn.Flush())
				testwait.Eventually(t, "the answer sent to "+target+" in ENVOY", func() bool { return seen(target) != nil })
				fence(t, hub.vlx)
				t.Logf("row 44c through the import: the answer aimed at %s is stored in VLX's stream %d times", target, storedIn(t, hub.vlx, bus.Stream, target))
				require.Zero(t, storedIn(t, hub.vlx, bus.Stream, target))
			})
		}
	})

	t.Run("VLX watches ENVOY's bucket", func(t *testing.T) {
		o := defaultVLXOptions()
		o.edit = crossImports(vlxClientAccount, core.JetStreamAccount, "$JS.ENVOY.API", "_INBOX_xenvoy")
		hub := startVLXHub(t, o, map[string]vlxClient{
			"xwatch":   {sideVLX, xenvoy},
			"dispatch": {sideEnvoy, natsgrant.Dispatch("dispatch")},
		})
		hub.setUp(t)
		conn, refused := hub.connect(t, "xwatch")
		js, err := conn.JetStream(natsgo.APIPrefix("$JS.ENVOY.API"))
		require.NoError(t, err)
		put, err := hub.shared.KeyValue(bus.InterestBucket)
		require.NoError(t, err)
		got := kvWatchSees(t, js, put)
		t.Logf("the watch from VLX of ENVOY's bucket got the update: %v; refusals %v", got, refused.seen)
		require.True(t, got)
		d, _ := hub.connect(t, "dispatch")
		djs, err := d.JetStream()
		require.NoError(t, err)
		_, err = djs.Publish(infraIssue+"comment.created", []byte(`{"text":"INFRA's comment"}`))
		require.NoError(t, err)
		msg, err := js.GetLastMsg(bus.Stream, infraIssue+"comment.created")
		require.NoError(t, err)
		t.Logf("exposure: the VLX client's STREAM.MSG.GET through $JS.ENVOY.API read %s: %s", msg.Subject, msg.Data)
	})

	t.Run("a second import of _INBOX.> into ENVOY", func(t *testing.T) {
		o := defaultVLXOptions()
		o.edit = func(t *testing.T, text string) string {
			return intoList(t, text, core.ClientAccount, "imports", "      "+streamImport(vlxJetStreamAccount, "_INBOX.>")+"\n")
		}
		keys := testnats.NewCalloutKeys(t)
		_, operatorKey := testnats.User(t)
		path := filepath.Join(t.TempDir(), "nats-server.conf")
		text := vlxConfigText(t, keys, operatorKey, o)
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("listen: \"127.0.0.1:-1\"\njetstream: {store_dir: %q}\n", filepath.Join(t.TempDir(), "js"))+text), 0o600))
		_, err := natsserver.ProcessConfigFile(path)
		t.Logf("nats-server on a second _INBOX.> import into ENVOY: %v", err)
		if err != nil {
			return
		}
		hub := startVLXHub(t, o, map[string]vlxClient{
			"edge":   {sideVLX, natsgrant.ListenerHost("edge")},
			"daemon": {sideEnvoy, natsgrant.Daemon()},
			"dv":     {sideVLX, natsgrant.Dispatch("dispatch")},
		})
		hub.setUp(t)
		daemon, _ := hub.connect(t, "daemon")
		leaked, err := daemon.SubscribeSync("_INBOX.>")
		require.NoError(t, err)
		require.NoError(t, daemon.Flush())
		edge, _ := hub.connect(t, "edge")
		ejs, err := edge.JetStream()
		require.NoError(t, err)
		deliver := edge.NewInbox()
		delivered, err := edge.SubscribeSync(deliver)
		require.NoError(t, err)
		require.NoError(t, edge.Flush())
		dv, _ := hub.connect(t, "dv")
		dvjs, err := dv.JetStream()
		require.NoError(t, err)
		_, err = dvjs.Publish(vlxIssue+"comment.created", []byte(`{"text":"VLX's comment"}`))
		require.NoError(t, err)
		_, err = ejs.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{Durable: "edge-push", DeliverSubject: deliver, FilterSubject: vlxIssue + ">", AckPolicy: natsgo.AckNonePolicy})
		require.NoError(t, err)
		m, err := delivered.NextMsg(testwait.Bounded(t, 10*time.Second, time.Second))
		require.NoError(t, err, "the edge's own push delivery")
		require.NoError(t, edge.Flush())
		require.NoError(t, daemon.Flush())
		n, _, _ := leaked.Pending()
		t.Logf("the edge's push delivery of %s on %s; a daemon grant's _INBOX.> subscriber in ENVOY holds %d messages", m.Subject, deliver, n)
		for {
			got, err := leaked.NextMsg(0)
			if err != nil {
				break
			}
			t.Logf("ENVOY's _INBOX.> subscriber received %s: %s", got.Subject, got.Data)
		}
	})
}
