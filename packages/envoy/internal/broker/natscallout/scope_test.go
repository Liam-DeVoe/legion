package natscallout

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// The accounts of two daemons held to their exchange projects: PUB's, and one whose exchange
// entry names a project Dispatch is slow to answer about.
const (
	pubDaemonAccount  = "system:serviceaccount:legion-pub:legion-daemon"
	slowDaemonAccount = "system:serviceaccount:legion-slow:legion-daemon"
)

// exchangeOf is a Deps.Exchange that gives each account its projects, and no entry to any other.
func exchangeOf(projects map[string][]string) func(string) ([]string, bool) {
	return func(account string) ([]string, bool) {
		listed, ok := projects[account]
		return listed, ok
	}
}

// dispatchAnswers is a Deps.Access standing in for Dispatch: each project's answer, or
// projectaccess.ErrProjectNotFound for one it does not hold, counting the asks about each.
type dispatchAnswers struct {
	mu      sync.Mutex
	answers map[string][]string // project → repositories
	failing error
	asked   map[string]int
	// blockAfter is how many asks about a project answer at once; each later ask blocks for
	// block, or until its context ends. A project in blockAlways blocks on every ask.
	blockAfter  int
	block       time.Duration
	blockAlways map[string]bool
}

func (d *dispatchAnswers) access(ctx context.Context, project, person string) (projectaccess.Answer, error) {
	d.mu.Lock()
	if d.asked == nil {
		d.asked = map[string]int{}
	}
	d.asked[project]++
	asks, repositories, held, failing := d.asked[project], d.answers[project], false, d.failing
	_, held = d.answers[project]
	blocks := d.blockAlways[project] || (d.block > 0 && asks > d.blockAfter)
	d.mu.Unlock()
	if person != "" {
		return projectaccess.Answer{}, fmt.Errorf("the auth service asked about person %q", person)
	}
	if blocks {
		select {
		case <-ctx.Done():
			return projectaccess.Answer{}, ctx.Err()
		case <-time.After(d.block):
		}
	}
	switch {
	case failing != nil:
		return projectaccess.Answer{}, failing
	case !held:
		return projectaccess.Answer{}, fmt.Errorf("%w: %s", projectaccess.ErrProjectNotFound, project)
	}
	return projectaccess.Answer{Project: project, Visibility: "public", Visible: true, Repositories: slices.Clone(repositories)}, nil
}

func (d *dispatchAnswers) set(project string, repositories ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answers[project] = repositories
}

func (d *dispatchAnswers) fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failing = err
}

// heldDeps are Deps whose ServiceToken verifies tokens, whose exchange list is exchange, and
// whose Dispatch is dispatch, with the answers about every project services can name asked for
// once, as Start's first ask does.
func heldDeps(t *testing.T, tokens map[string]platformToken, exchange map[string][]string, dispatch *dispatchAnswers, services []ServiceEntry) Deps {
	t.Helper()
	deps := Deps{ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(exchange), Access: dispatch.access}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held, "no service entry names a project to ask Dispatch about")
	deps.held.refresh(t.Context())
	return deps
}

// requireRefused requires err to be a refusal of a service whose reason begins reason.
func requireRefused(t *testing.T, err error, reason string) {
	t.Helper()
	var refused *Refusal
	require.True(t, errors.As(err, &refused), "the connection was not refused: %v", err)
	require.Equal(t, credentialService, refused.Kind)
	require.True(t, strings.HasPrefix(refused.Reason, reason), "%q does not begin %q", refused.Reason, reason)
}

// A daemon whose entry takes its exchange projects ("-") is answered with the daemon's grant held
// to the projects the broker's exchange list gives its account and the repositories Dispatch maps
// to them: PUB's subjects, its repository's lane and a reply prefix of its own, and nothing of
// PRIV, which its account's entry does not list. Through the hub, its subscribe to PUB's role
// exceptions is served and PRIV's is refused by name, as is a subscribe to every reply inbox.
func TestADaemonEntryIsHeldToItsExchangeProjects(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	exchange := map[string][]string{pubDaemonAccount: {"PUB"}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}, "PRIV": {"acme/priv"}}}

	decision, err := Decide(t.Context(), heldDeps(t, tokens, exchange, dispatch, services), services, serviceRequest("pub-daemon-token"))
	require.NoError(t, err)
	want, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PUB"}, Repositories: []string{"acme/pub"}, Identity: pubDaemonAccount})
	require.NoError(t, err)
	require.Equal(t, want, decision.Grant)
	require.Equal(t, "daemon/"+pubDaemonAccount, decision.Name)
	pubExceptions := bus.ExceptionTopicPrefix + contracts.RoleTopicPrefix + "PUB.>"
	require.Contains(t, decision.Grant.Subscribe, pubExceptions)
	require.Contains(t, decision.Grant.Subscribe, natsgrant.InboxPrefix(pubDaemonAccount)+".>")
	require.NotContains(t, decision.Grant.Subscribe, nats.InboxPrefix+">")
	for _, entry := range slices.Concat(decision.Grant.Publish, decision.Grant.Subscribe) {
		require.NotContains(t, strings.ToLower(entry), "priv", "the PUB daemon's grant names PRIV: %s", entry)
	}

	// The same, through the hub, with the asks to Dispatch the service makes itself.
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	startServiceOf(t, url, keys, services, Deps{ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(exchange), Access: dispatch.access})
	errs := make(chan error, 16)
	var nc *nats.Conn
	testwait.Eventually(t, "the hub to admit the PUB daemon", func() bool {
		var err error
		nc, err = nats.Connect(url, nats.UserInfo(natscred.ServiceUser, "pub-daemon-token"), nats.NoReconnect(),
			nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
				select {
				case errs <- err:
				default:
				}
			}))
		return err == nil
	})
	t.Cleanup(nc.Close)
	_, err = nc.SubscribeSync(pubExceptions)
	require.NoError(t, err)
	_, err = nc.SubscribeSync(bus.ExceptionTopicPrefix + contracts.RoleTopicPrefix + "PRIV.>")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	awaitViolation(t, errs, bus.ExceptionTopicPrefix+contracts.RoleTopicPrefix+"PRIV.>")
	_, err = nc.SubscribeSync(nats.InboxPrefix + ">")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	awaitViolation(t, errs, nats.InboxPrefix+">")
	select {
	case err := <-errs:
		require.NotContains(t, err.Error(), pubExceptions, "the hub refused PUB's own subscribe")
	default:
	}
}

// No admission waits on Dispatch: the service asks about every project at its start and every
// 20 s after, and a decision reads only the answers it holds. Dispatch here answers its first ask
// about PUB at once and blocks three seconds on every later one, and never answers about SLOW
// within the test. With a second round of asks blocked, PUB's daemon is admitted in under 100 ms
// from the held answer, and SLOW's is refused as not yet known, as fast. The control reads Access
// inside the decision: the admission waits the three seconds, and this test fails naming how long
// it took.
func TestAnAdmissionNeverWaitsOnDispatch(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{
		"pub-daemon-token":  {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
		"slow-daemon-token": {account: slowDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
	}
	dispatch := &dispatchAnswers{
		answers:    map[string][]string{"PUB": {"acme/pub"}, "SLOW": {"acme/slow"}},
		blockAfter: 1, block: 3 * time.Second, blockAlways: map[string]bool{"SLOW": true},
	}
	deps := Deps{ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}, slowDaemonAccount: {"SLOW"}}), Access: dispatch.access}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	asks, stop := context.WithCancel(t.Context())
	t.Cleanup(stop)
	go deps.held.refresh(asks)
	testwait.Eventually(t, "the first answer about PUB", func() bool { _, known := deps.held.answer("PUB"); return known })
	go deps.held.refresh(asks) // its ask about PUB blocks three seconds
	testwait.Eventually(t, "a second ask about PUB in flight", func() bool {
		dispatch.mu.Lock()
		defer dispatch.mu.Unlock()
		return dispatch.asked["PUB"] >= 2
	})

	began := time.Now()
	decision, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	took := time.Since(began)
	require.NoError(t, err)
	require.Less(t, took, 100*time.Millisecond, "the PUB daemon's admission took %s: it waited on Dispatch", took)
	require.Contains(t, decision.Grant.Subscribe, bus.ExceptionTopicPrefix+contracts.RoleTopicPrefix+"PUB.>")

	began = time.Now()
	_, err = Decide(t.Context(), deps, services, serviceRequest("slow-daemon-token"))
	took = time.Since(began)
	requireRefused(t, err, "project access not yet known: SLOW")
	require.Less(t, took, 100*time.Millisecond, "the SLOW daemon's refusal took %s: it waited on Dispatch", took)
}

// An answer once held is kept while Dispatch cannot be reached, and the next answer Dispatch
// gives replaces it.
func TestAHeldAnswerFollowsDispatchAndOutlivesItsOutage(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}}, dispatch, services)
	// githubCreate is the create of repository's GitHub consumer under PUB, its subject naming
	// the consumer's one filter.
	githubCreate := func(repository string) string {
		return "$JS.API.CONSUMER.CREATE." + bus.Stream + "." + natsgrant.GitHubConsumerName("PUB", repository) + "." + natsgrant.GitHubFilter(repository)
	}

	dispatch.fail(errors.New("dial tcp: connection refused"))
	deps.held.refresh(t.Context())
	decision, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	require.NoError(t, err, "an outage of Dispatch dropped the answer held about PUB")
	require.Contains(t, decision.Grant.Publish, githubCreate("acme/pub"))

	dispatch.fail(nil)
	dispatch.set("PUB", "acme/renamed")
	deps.held.refresh(t.Context())
	decision, err = Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	require.NoError(t, err)
	require.Contains(t, decision.Grant.Publish, githubCreate("acme/renamed"))
	require.NotContains(t, decision.Grant.Publish, githubCreate("acme/pub"))
}

// A service held to its exchange projects whose account no exchange entry lists is refused,
// naming the account: there is no list of projects to hold it to, and holding it to none is not
// what its entry says.
func TestAnAccountWithNoExchangeEntryIsRefused(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{"slow-daemon-token": {account: slowDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}}, dispatch, services)

	_, err := Decide(t.Context(), deps, services, serviceRequest("slow-daemon-token"))
	requireRefused(t, err, "account has no exchange entry: "+slowDaemonAccount)
}

// A project the exchange list gives an account and Dispatch holds no answer for, because it holds
// no such project, refuses the connection naming the project.
func TestAnUnknownProjectRefusesTheConnection(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB", "GONE"}}, dispatch, services)

	_, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	requireRefused(t, err, "project not found: GONE")
}

// Through the auth service, Dispatch's grant may write the private-projects bucket and a
// listener's may read it, and nothing more of it: a listener's holds no write to its keys and no
// create of its stream. The static sets a hub renders hold neither, so a hub with no auth service
// renders what it rendered before (c-hub-config-unchanged), and the measurement job's grant is
// unchanged.
func TestDispatchAndListenerGrantsCarryThePrivateProjectsBucket(t *testing.T) {
	now := time.Now()
	tokens := map[string]platformToken{
		"dispatch-token": {account: "system:serviceaccount:dispatch:dispatch", issued: now, expiry: now.Add(time.Hour)},
		"listener-token": {account: listenerAccount, issued: now, expiry: now.Add(time.Hour)},
		"daemon-token":   {account: daemonAccount, issued: now, expiry: now.Add(time.Hour)},
		"measure-token":  {account: measureAccount, google: true, issued: now, expiry: now.Add(time.Hour)},
	}
	deps := Deps{ServiceToken: serviceTokens(tokens)}
	decide := func(raw string) natsgrant.Permissions {
		decision, err := Decide(t.Context(), deps, exampleServices, serviceRequest(raw))
		require.NoError(t, err)
		return decision.Grant
	}
	bucketKeys := "$KV." + bus.PrivateProjectsBucket + ".>"
	bucketCreate := "$JS.API.STREAM.CREATE.KV_" + bus.PrivateProjectsBucket

	dispatch := decide("dispatch-token")
	require.Equal(t, dispatchCalloutGrant("example-dispatch"), dispatch)
	require.Contains(t, dispatch.Publish, bucketKeys)

	listener := decide("listener-token")
	require.Equal(t, listenerCalloutGrant("example-hub"), listener)
	require.Contains(t, listener.Publish, "$JS.API.DIRECT.GET.KV_"+bus.PrivateProjectsBucket+".>")
	require.NotContains(t, listener.Publish, bucketKeys)
	require.NotContains(t, listener.Publish, bucketCreate)

	require.Equal(t, daemonCalloutGrant(daemonAccount), decide("daemon-token"))
	require.Equal(t, natsgrant.Measure(), decide("measure-token"))
	for name, static := range map[string]natsgrant.Permissions{
		"dispatch": natsgrant.Dispatch("example-dispatch"), "listener": natsgrant.Listener("example-hub"),
		"listener host": natsgrant.ListenerHost("example-hub"), "host": natsgrant.Host("example-host"),
	} {
		for _, entry := range slices.Concat(static.Publish, static.Subscribe) {
			require.NotContains(t, entry, bus.PrivateProjectsBucket, "the static %s set a hub renders names the private-projects bucket", name)
		}
	}
}

// dispatchCalloutGrant is the grant the auth service answers Dispatch's connection with as
// machine machineID: natsgrant.Dispatch, then the private-projects bucket's write.
func dispatchCalloutGrant(machineID string) natsgrant.Permissions {
	grant := natsgrant.Dispatch(machineID)
	grant.Publish = append(grant.Publish, natsgrant.KVWrite(bus.PrivateProjectsBucket)...)
	return grant
}

// listenerCalloutGrant is the grant the auth service answers a listener's connection with as
// machine machineID: natsgrant.Listener, then the private-projects bucket's read.
func listenerCalloutGrant(machineID string) natsgrant.Permissions {
	grant := natsgrant.Listener(machineID)
	grant.Publish = append(grant.Publish, natsgrant.KVRead(bus.PrivateProjectsBucket)...)
	return grant
}

// daemonCalloutGrant is the grant the auth service answers a daemon whose entry takes the whole
// grant (*) with: natsgrant.Daemon, then the subscribe of its account's own reply prefix.
func daemonCalloutGrant(account string) natsgrant.Permissions {
	grant := natsgrant.Daemon()
	grant.Subscribe = append(grant.Subscribe, natsgrant.InboxPrefix(account)+".>")
	return grant
}

// A Legion daemon presenting its token takes its account's own reply prefix whatever its entry's
// scope (the daemon's natsauth.ConnectService), so a daemon whose entry takes the whole grant (*)
// is granted that prefix too. Through the hub, its JetStream request is answered on that prefix:
// the stream it asks about does not exist, and it reads that answer. The control drops the prefix
// from the whole grant: the hub refuses the subscription to the prefix, and the request gets no
// answer.
func TestAWholeDaemonGrantTakesItsAccountsReplyPrefix(t *testing.T) {
	now := time.Now()
	tokens := map[string]platformToken{"daemon-token": {account: daemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	startServiceOf(t, url, keys, []ServiceEntry{{Kind: KindDaemon, Account: daemonAccount, AllProjects: true}}, Deps{ServiceToken: serviceTokens(tokens)})
	nc, err := nats.Connect(url, nats.UserInfo(natscred.ServiceUser, "daemon-token"), nats.NoReconnect(),
		nats.CustomInboxPrefix(natsgrant.InboxPrefix(daemonAccount)))
	require.NoError(t, err, "the hub did not admit the daemon")
	t.Cleanup(nc.Close)
	reply, err := nc.Request("$JS.API.STREAM.INFO."+bus.Stream, nil, 5*time.Second)
	require.NoError(t, err, "the daemon's JetStream request got no answer on its own reply prefix")
	require.Contains(t, string(reply.Data), "stream not found")
}
