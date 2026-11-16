//go:build natslive

package natsgrant_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// The live half of WithinProjects' tests: measurement M5 of dispatch://INFRA-3884's plan, rebuilt on
// the image the hub runs (testnats.HubImage) behind an auth callout, each daemon's grant arriving
// in the user JWT of the callout's answer as the auth service sends it (natscallout's
// encodeAnswer). A PUB daemon on today's grant (natsgrant.Daemon) is the control: it subscribes
// _INBOX.> and must receive what a PRIV daemon pulls from its consumer, or the measurement cannot
// see a leak at all. A PUB daemon on WithinProjects(Daemon(), PUB) then makes the calls the Legion
// daemon makes under it (t-c-10): its consumers created with one filter each, their info, their
// pull with Consume and Fetch, and the acknowledgements; it receives none of PRIV's messages, and
// the server refuses each call that would reach PRIV.

const (
	livePUBIdentity  = "legion-daemon-PUB"
	livePRIVIdentity = "legion-daemon-PRIV"
	liveWait         = 10 * time.Second
	// liveRefusalWait bounds a call the server refuses: nats.go's request waits for an answer
	// the server never sends after a publish violation, so the call ends at this deadline.
	liveRefusalWait = 3 * time.Second
	// largestRepositories is more than twice the repositories Dispatch maps to its largest
	// project: INFRA, 14 repositories, one of them renamed, so 15 names in its scope, read
	// from GET /api/v1/settings/repo-projects at 2026-11-16T04:08:48Z.
	largestRepositories = 32
)

func TestWithinProjectsHoldsADaemonToItsProjectsOnTheHub(t *testing.T) {
	pubScope := natsgrant.Scope{Projects: []string{"PUB"}, Repositories: []string{"acme/widgets"}, Identity: livePUBIdentity}
	privScope := natsgrant.Scope{Projects: []string{"PRIV"}, Repositories: []string{"acme/secret"}, Identity: livePRIVIdentity}
	largest := natsgrant.Scope{Projects: []string{"INFRA"}, Identity: "legion-daemon-INFRA"}
	for i := range largestRepositories {
		largest.Repositories = append(largest.Repositories, fmt.Sprintf("example-owner/repository-%02d", i))
	}
	grants := map[string]natsgrant.Permissions{
		"today-pub":     natsgrant.Daemon(),
		"today-priv":    natsgrant.Daemon(),
		"narrowed-pub":  within(t, pubScope),
		"narrowed-priv": within(t, privScope),
		"largest":       within(t, largest),
	}

	keys := testnats.NewCalloutKeys(t)
	keys.XKeySeed, keys.XKeyPublic = "", "" // requests in the clear: the sealing is t-e-3's to test
	adminSeed, adminPublic := testnats.User(t)
	url := testnats.StartImageWithCallout(t, keys, testnats.StaticUser{NKey: adminPublic, Publish: []string{">"}, Subscribe: []string{">"}})
	answers := serveCallout(t, url, keys, grants)

	admin, err := testnats.ConnectSeed(t, url, adminSeed)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	adminJS, err := jetstream.New(admin)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	_, err = adminJS.CreateStream(ctx, jetstream.StreamConfig{Name: bus.Stream, Subjects: bus.StreamCarriedSubjects()})
	require.NoError(t, err)
	publish := func(subject string, bodies ...string) {
		t.Helper()
		for _, body := range bodies {
			_, err := adminJS.Publish(ctx, subject, []byte(body))
			require.NoError(t, err, "publish %s", subject)
		}
	}
	privIssue := "notifications.dispatch.issue.PRIV.PRIV-1.comment.created"
	publish("notifications.dispatch.issue.PUB.PUB-1.comment.created", "PUB-1", "PUB-2", "PUB-3", "PUB-4", "PUB-5")
	publish("notifications.github.acme.widgets.pr.1", "PUB-repository")
	publish("notifications.github.acme.secret.pr.1", "PRIV-repository")
	publish(privIssue, "PRIV-secret-1", "PRIV-secret-2", "PRIV-secret-3", "PRIV-secret-4", "PRIV-secret-5")

	// The control: today's grant receives every reply on the server.
	todayPub := connectAs(t, url, "today-pub")
	// The control counts only the PRIV messages themselves: the replies it also receives on
	// _INBOX.>, the PRIV daemon's consumer create among them, name PRIV too.
	controlSeen := countPRIV(t, todayPub.conn, func(body string) bool { return strings.HasPrefix(body, "PRIV-secret-") }, "_INBOX.>")
	todayPriv := connectAs(t, url, "today-priv")
	pullPRIV(ctx, t, todayPriv.conn, 5)
	require.Eventually(t, func() bool { return controlSeen.count() >= 5 }, liveWait, 50*time.Millisecond,
		"the control, today's daemon grant, saw %d of the 5 PRIV messages the PRIV daemon pulled: this measurement cannot see a leak", controlSeen.count())
	t.Logf("control: a PUB daemon on today's grant received %d of the 5 PRIV messages the PRIV daemon pulled, on _INBOX.>", controlSeen.count())

	// The narrowed PUB daemon subscribes every inbox it can name, its own included, and the role
	// exceptions of PUB and of PRIV.
	narrowedPub := connectAs(t, url, "narrowed-pub", natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(livePUBIdentity)))
	// The narrowed PUB daemon counts anything that names PRIV at all.
	narrowedSeen := countPRIV(t, narrowedPub.conn, func(body string) bool { return strings.Contains(body, "PRIV") },
		natsgrant.InboxPrefix(livePUBIdentity)+".>", "_INBOX.>", natsgrant.InboxPrefix(livePRIVIdentity)+".>",
		"notifications.envoy.exceptions.notifications.role.PUB.*", "notifications.envoy.exceptions.notifications.role.PRIV.*")
	for _, subject := range []string{"_INBOX.>", natsgrant.InboxPrefix(livePRIVIdentity) + ".>", "notifications.envoy.exceptions.notifications.role.PRIV.*"} {
		narrowedPub.requireRefused(t, "subscription", subject)
	}
	// Both admitted observers must see a body the PRIV predicate accepts before a clean result
	// means anything. Keep these controls in the final expected set rather than clearing a count.
	observerControls := []string{"PRIV-inbox-observer-control", "PRIV-role-observer-control"}
	require.NoError(t, admin.Publish(natsgrant.InboxPrefix(livePUBIdentity)+".observer-control", []byte(observerControls[0])))
	require.NoError(t, admin.Publish("notifications.envoy.exceptions.notifications.role.PUB.observer-control", []byte(observerControls[1])))
	require.NoError(t, admin.Flush())
	require.Eventually(t, func() bool { return narrowedSeen.count() == len(observerControls) }, liveWait, 50*time.Millisecond,
		"the narrowed connection's own inbox and role observers must detect the PRIV controls")

	// PRIV pulls again, on today's grant (the default inbox) and on its own narrowed grant (its
	// own prefix).
	publish(privIssue, "PRIV-secret-6", "PRIV-secret-7", "PRIV-secret-8", "PRIV-secret-9", "PRIV-secret-10")
	pullPRIV(ctx, t, todayPriv.conn, 5)
	narrowedPriv := connectAs(t, url, "narrowed-priv", natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(livePRIVIdentity)))
	publish(privIssue, "PRIV-secret-11", "PRIV-secret-12", "PRIV-secret-13", "PRIV-secret-14", "PRIV-secret-15")
	pullPRIV(ctx, t, narrowedPriv.conn, 5)
	require.Eventually(t, func() bool { return controlSeen.count() >= 10 }, liveWait, 50*time.Millisecond,
		"the control saw %d PRIV messages after today's PRIV pulled 10", controlSeen.count())

	// The narrowed PUB daemon's own work: each consumer t-c-10 opens, created with its one filter,
	// read and pulled; the Dispatch consumer through Consume, as the daemon pulls, every message
	// acknowledged.
	pubJS, err := jetstream.New(narrowedPub.conn)
	require.NoError(t, err)
	dispatch := openConsumer(ctx, t, pubJS, "legion-go-PUB-dispatch", "notifications.dispatch.issue.PUB.>")
	got := make(chan string, 16)
	consuming, err := dispatch.Consume(func(msg jetstream.Msg) {
		if err := msg.DoubleAck(ctx); err != nil {
			got <- "acknowledgement failed: " + err.Error()
			return
		}
		got <- string(msg.Data())
	})
	require.NoError(t, err)
	var bodies []string
	for len(bodies) < 5 {
		select {
		case body := <-got:
			bodies = append(bodies, body)
		case <-time.After(liveWait):
			t.Fatalf("the narrowed PUB daemon pulled %v of its 5 messages", bodies)
		}
	}
	consuming.Stop()
	require.Equal(t, []string{"PUB-1", "PUB-2", "PUB-3", "PUB-4", "PUB-5"}, bodies)
	info, err := dispatch.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), info.AckFloor.Consumer, "the acknowledgements of the narrowed PUB daemon")
	require.Zero(t, info.NumPending+uint64(info.NumAckPending), "the narrowed PUB daemon's Dispatch consumer has nothing left")

	for name, filter := range map[string]string{
		"legion-go-PUB-mapping": "notifications.dispatch.project.PUB.settings.repo_project.updated",
		"legion-go-PUB-wake":    "notifications.legion.pub.wake",
		"legion-go-PUB-ready":   bus.LegionWakeTopic,
	} {
		consumer := openConsumer(ctx, t, pubJS, name, filter)
		batch, err := consumer.FetchNoWait(1)
		require.NoError(t, err, name)
		for msg := range batch.Messages() {
			t.Fatalf("%s pulled %q from an empty lane", name, msg.Data())
		}
		require.NoError(t, batch.Error(), name)
	}
	github := openConsumer(ctx, t, pubJS, "legion-go-PUB-github-0", "notifications.github.acme.widgets.>")
	batch, err := github.Fetch(2, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	var repository []string
	for msg := range batch.Messages() {
		repository = append(repository, string(msg.Data()))
		require.NoError(t, msg.DoubleAck(ctx))
	}
	require.Equal(t, []string{"PUB-repository"}, repository)

	// What the server refuses the narrowed PUB daemon: each call fails at the client, and the
	// server reports a publish violation naming the call's subject.
	refused := func(subject string, call func(ctx context.Context) error) {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, liveRefusalWait)
		defer cancel()
		require.Error(t, call(callCtx), subject)
		narrowedPub.requireRefused(t, "publish", subject)
	}
	refusedCreate := func(subject string, config jetstream.ConsumerConfig) {
		t.Helper()
		refused(subject, func(ctx context.Context) error {
			_, err := pubJS.CreateOrUpdateConsumer(ctx, bus.Stream, config)
			return err
		})
	}
	const create = "$JS.API.CONSUMER.CREATE." + bus.Stream + "."
	refusedCreate(create+"legion-go-PUB-dispatch.notifications.dispatch.issue.PRIV.>",
		jetstream.ConsumerConfig{Durable: "legion-go-PUB-dispatch", FilterSubject: "notifications.dispatch.issue.PRIV.>", AckPolicy: jetstream.AckExplicitPolicy})
	refusedCreate(create+"legion-go-PUB-dispatch",
		jetstream.ConsumerConfig{Durable: "legion-go-PUB-dispatch", FilterSubjects: []string{"notifications.dispatch.issue.PUB.>", "notifications.dispatch.issue.PRIV.>"}, AckPolicy: jetstream.AckExplicitPolicy})
	refusedCreate(create+"legion-go-PUB-github-0.notifications.github.acme.secret.>",
		jetstream.ConsumerConfig{Durable: "legion-go-PUB-github-0", FilterSubject: "notifications.github.acme.secret.>", AckPolicy: jetstream.AckExplicitPolicy})
	refused("$JS.API.STREAM.INFO."+bus.Stream, func(ctx context.Context) error {
		_, err := pubJS.Stream(ctx, bus.Stream)
		return err
	})
	refused("$JS.API.CONSUMER.INFO."+bus.Stream+".legion-go-PRIV-dispatch", func(ctx context.Context) error {
		_, err := pubJS.Consumer(ctx, bus.Stream, "legion-go-PRIV-dispatch")
		return err
	})
	for subject, body := range map[string]string{
		"$JS.API.STREAM.MSG.GET." + bus.Stream:                                 `{"seq":1}`,
		"$JS.API.CONSUMER.MSG.NEXT." + bus.Stream + ".legion-go-PRIV-dispatch": `{"batch":1,"no_wait":true}`,
	} {
		refused(subject, func(ctx context.Context) error {
			_, err := narrowedPub.conn.RequestWithContext(ctx, subject, []byte(body))
			return err
		})
	}

	// A create whose subject names PUB's filter while its body asks for PRIV's is refused by the
	// server itself, so the filter a create subject names is the filter the consumer gets.
	mismatched, err := json.Marshal(map[string]any{
		"stream_name": bus.Stream,
		"config":      map[string]any{"durable_name": "legion-go-PUB-dispatch", "filter_subject": "notifications.dispatch.issue.PRIV.>", "ack_policy": "explicit"},
	})
	require.NoError(t, err)
	reply, err := narrowedPub.conn.Request(create+"legion-go-PUB-dispatch.notifications.dispatch.issue.PUB.>", mismatched, liveWait)
	require.NoError(t, err)
	var answer struct {
		Error *struct {
			Code    int `json:"code"`
			ErrCode int `json:"err_code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(reply.Data, &answer), string(reply.Data))
	require.NotNil(t, answer.Error, "the server took a create whose body's filter is not its subject's: %s", reply.Data)
	require.Equal(t, 10131, answer.Error.ErrCode, string(reply.Data))
	t.Logf("a create naming PUB's filter with PRIV's in its body: %s", reply.Data)

	// Only the ten PRIV messages pulled after these subscriptions started can be observed here.
	// Flush waits for a protocol reply, not the asynchronous subscription callbacks; the barrier
	// must finish those callbacks before the final snapshot can establish that no message leaked.
	require.NoError(t, narrowedPub.conn.Flush())
	callbacksDone := make(chan struct{})
	require.NoError(t, narrowedPub.conn.Barrier(func() { close(callbacksDone) }))
	select {
	case <-callbacksDone:
	case <-time.After(liveWait):
		t.Fatal("the narrowed connection's observation callbacks did not finish")
	}
	require.ElementsMatch(t, observerControls, narrowedSeen.bodies(), "the narrowed PUB daemon received PRIV's messages")
	narrowedPub.requireNotRefused(t, "notifications.envoy.exceptions.notifications.role.PUB.*")
	t.Logf("narrowed: a PUB daemon on WithinProjects(Daemon(), PUB) received 0 of 10 PRIV messages pulled while it observed; the control received %d", controlSeen.count())

	// The largest project's grant fits in the answer the server takes: the connection is admitted
	// and its last GitHub consumer opens on the first repository.
	largestConn := connectAs(t, url, "largest", natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(largest.Identity)))
	largestJS, err := jetstream.New(largestConn.conn)
	require.NoError(t, err)
	openConsumer(ctx, t, largestJS, fmt.Sprintf("legion-go-INFRA-github-%d", largestRepositories-1), "notifications.github.example-owner.repository-00.>")
	for _, name := range []string{"today-pub", "narrowed-pub", "largest"} {
		grant := grants[name]
		t.Logf("auth response for %s: %d bytes, %d publish and %d subscribe entries", name, answers.size(name), len(grant.Publish), len(grant.Subscribe))
	}
}

// within is WithinProjects(Daemon(), scope), which must not refuse.
func within(t *testing.T, scope natsgrant.Scope) natsgrant.Permissions {
	t.Helper()
	grant, err := natsgrant.WithinProjects(natsgrant.Daemon(), scope)
	require.NoError(t, err)
	return grant
}

// calloutAnswers records the size of each answer the stand-in auth service sent, by user.
type calloutAnswers struct {
	mu    sync.Mutex
	sizes map[string]int
}

func (a *calloutAnswers) size(user string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sizes[user]
}

// serveCallout answers the server's authorization requests as the auth service does: a client
// whose user name grants names is admitted in the global account with that grant as its user
// JWT's allow lists, signed by the issuer; any other is refused.
func serveCallout(t *testing.T, url string, keys testnats.CalloutKeys, grants map[string]natsgrant.Permissions) *calloutAnswers {
	t.Helper()
	issuer, err := nkeys.FromSeed([]byte(keys.IssuerSeed))
	require.NoError(t, err)
	conn, err := testnats.ConnectSeed(t, url, keys.ServiceSeed)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	answers := &calloutAnswers{sizes: map[string]int{}}
	_, err = conn.Subscribe("$SYS.REQ.USER.AUTH", func(msg *natsgo.Msg) {
		request, err := jwt.DecodeAuthorizationRequestClaims(string(msg.Data))
		if err != nil {
			return
		}
		response := jwt.NewAuthorizationResponseClaims(request.UserNkey)
		response.Audience = request.Server.ID
		name := request.ConnectOptions.Username
		if grant, ok := grants[name]; ok {
			user := jwt.NewUserClaims(request.UserNkey)
			user.Audience = "$G"
			user.Name = name
			user.Expires = time.Now().Add(time.Hour).Unix()
			user.Pub.Allow.Add(grant.Publish...)
			user.Sub.Allow.Add(grant.Subscribe...)
			if response.Jwt, err = user.Encode(issuer); err != nil {
				response.Error = err.Error()
			}
		} else {
			response.Error = "no grant for " + name
		}
		encoded, err := response.Encode(issuer)
		if err != nil {
			return
		}
		answers.mu.Lock()
		answers.sizes[name] = len(encoded)
		answers.mu.Unlock()
		_ = msg.Respond([]byte(encoded))
	})
	require.NoError(t, err)
	require.NoError(t, conn.Flush())
	return answers
}

// liveClient is a connection the callout admitted, with every permissions violation the server
// reported to it.
type liveClient struct {
	conn       *natsgo.Conn
	mu         sync.Mutex
	violations []string
}

// connectAs connects as user, whom the stand-in auth service admits with grants[user].
func connectAs(t *testing.T, url, user string, options ...natsgo.Option) *liveClient {
	t.Helper()
	client := &liveClient{}
	options = append([]natsgo.Option{
		natsgo.UserInfo(user, "service-token"),
		natsgo.Timeout(5 * time.Second),
		natsgo.NoReconnect(),
		natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, err error) {
			client.mu.Lock()
			client.violations = append(client.violations, err.Error())
			client.mu.Unlock()
		}),
	}, options...)
	conn, err := natsgo.Connect(url, options...)
	require.NoError(t, err, "connect as %s", user)
	t.Cleanup(conn.Close)
	client.conn = conn
	return client
}

// refused reports whether the server reported a permissions violation of kind (publish or
// subscription) naming subject.
func (c *liveClient) refused(kind, subject string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := strings.ToLower("permissions violation for " + kind + " to \"" + subject + "\"")
	for _, violation := range c.violations {
		if strings.Contains(strings.ToLower(violation), want) {
			return true
		}
	}
	return false
}

func (c *liveClient) requireRefused(t *testing.T, kind, subject string) {
	t.Helper()
	require.Eventually(t, func() bool { return c.refused(kind, subject) }, liveWait, 50*time.Millisecond,
		"the server reported no %s violation naming %s; it reported %v", kind, subject, c.reported())
}

func (c *liveClient) requireNotRefused(t *testing.T, subject string) {
	t.Helper()
	for _, kind := range []string{"publish", "subscription"} {
		require.False(t, c.refused(kind, subject), "the server refused %s %s", kind, subject)
	}
}

func (c *liveClient) reported() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.violations)
}

// seenPRIV is every message body naming PRIV that one connection's subscriptions received.
type seenPRIV struct {
	mu   sync.Mutex
	seen []string
}

func (s *seenPRIV) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func (s *seenPRIV) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// countPRIV subscribes conn to each subject and records each message body names reports true for
// that arrives on any of them.
func countPRIV(t *testing.T, conn *natsgo.Conn, names func(body string) bool, subjects ...string) *seenPRIV {
	t.Helper()
	seen := &seenPRIV{}
	for _, subject := range subjects {
		_, err := conn.Subscribe(subject, func(msg *natsgo.Msg) {
			if names(string(msg.Data)) {
				seen.mu.Lock()
				seen.seen = append(seen.seen, string(msg.Data))
				seen.mu.Unlock()
			}
		})
		require.NoError(t, err, subject)
	}
	require.NoError(t, conn.Flush())
	return seen
}

// pullPRIV pulls n messages from PRIV's Dispatch consumer on conn, acknowledging each.
func pullPRIV(ctx context.Context, t *testing.T, conn *natsgo.Conn, n int) {
	t.Helper()
	js, err := jetstream.New(conn)
	require.NoError(t, err)
	consumer := openConsumer(ctx, t, js, "legion-go-PRIV-dispatch", "notifications.dispatch.issue.PRIV.>")
	batch, err := consumer.Fetch(n, jetstream.FetchMaxWait(liveWait))
	require.NoError(t, err)
	pulled := 0
	for msg := range batch.Messages() {
		require.NoError(t, msg.DoubleAck(ctx))
		pulled++
	}
	require.NoError(t, batch.Error())
	require.Equal(t, n, pulled, "PRIV's pull")
}

// openConsumer creates or updates the durable consumer name on the notification stream with the
// one filter the daemon gives it, as t-c-10's daemon opens each (never js.Stream, which sends the
// stream's info), and reads it back through its info.
func openConsumer(ctx context.Context, t *testing.T, js jetstream.JetStream, name, filter string) jetstream.Consumer {
	t.Helper()
	_, err := js.CreateOrUpdateConsumer(ctx, bus.Stream, jetstream.ConsumerConfig{
		Durable: name, FilterSubject: filter, AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err, "create %s on %s", name, filter)
	consumer, err := js.Consumer(ctx, bus.Stream, name)
	require.NoError(t, err, "read %s", name)
	return consumer
}
