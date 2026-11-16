package natscallout

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/sjawhar/envoy/internal/metrics"
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

// entryPerson is the person every exchange entry in these tests acts for.
const entryPerson = "ada@example.com"

// exchangeOf is a Deps.Exchange that gives each account entryPerson and its projects, and no
// entry to any other.
func exchangeOf(projects map[string][]string) func(string) (string, []string, bool) {
	return func(account string) (string, []string, bool) {
		listed, ok := projects[account]
		if !ok {
			return "", nil, false
		}
		return entryPerson, listed, true
	}
}

// dispatchAnswers is a Deps.Access and a Deps.RepoProjects standing in for Dispatch: each
// project's answer, or projectaccess.ErrProjectNotFound for one it does not hold, counting the
// asks about each, and the repository mapping. As Dispatch answers, a project is visible to no
// person when it is private, and to entryPerson unless unseen names it.
type dispatchAnswers struct {
	mu      sync.Mutex
	answers map[string][]string // project → repositories
	private map[string]bool
	unseen  map[string]bool // private projects entryPerson cannot see
	failing error
	asked   map[string]int
	// blockAfter is how many asks about a project answer at once; each later ask blocks for
	// block, or until its context ends.
	blockAfter int
	block      time.Duration
	// mapping is the repository mapping's rows, nil for a row of each repository answers maps to
	// its project, never renamed. Its read fails with mappingFailing when that is set, and with
	// mappingBlocks blocks until its context ends.
	mapping        []projectaccess.RepoProject
	mappingFailing error
	mappingBlocks  bool
}

func (d *dispatchAnswers) access(ctx context.Context, project, person string) (projectaccess.Answer, error) {
	d.mu.Lock()
	if d.asked == nil {
		d.asked = map[string]int{}
	}
	d.asked[project]++
	asks, repositories, failing := d.asked[project], d.answers[project], d.failing
	_, held := d.answers[project]
	private, unseen := d.private[project], d.unseen[project]
	blocks := d.block > 0 && asks > d.blockAfter
	d.mu.Unlock()
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
	answer := projectaccess.Answer{Project: project, Visibility: "public", Visible: true, Repositories: slices.Clone(repositories)}
	if private {
		answer.Visibility = "private"
		answer.Visible = person == entryPerson && !unseen
	}
	return answer, nil
}

func (d *dispatchAnswers) repoProjects(ctx context.Context) ([]projectaccess.RepoProject, error) {
	d.mu.Lock()
	rows, failing, blocks := slices.Clone(d.mapping), cmp.Or(d.failing, d.mappingFailing), d.mappingBlocks
	if rows == nil {
		for project, repositories := range d.answers {
			for _, repository := range repositories {
				rows = append(rows, projectaccess.RepoProject{Repo: repository, Project: project})
			}
		}
	}
	d.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if failing != nil {
		return nil, failing
	}
	return rows, nil
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

// drop takes project out of Dispatch, which then answers ErrProjectNotFound about it.
func (d *dispatchAnswers) drop(project string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.answers, project)
}

// hide takes entryPerson off project's access list, or with hidden false puts them back on it.
func (d *dispatchAnswers) hide(project string, hidden bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.unseen == nil {
		d.unseen = map[string]bool{}
	}
	d.unseen[project] = hidden
}

// heldDeps are Deps whose ServiceToken verifies tokens, whose exchange list is exchange, and
// whose Dispatch is dispatch, with one pass over every project services can name made, as Start's
// first pass is.
func heldDeps(t *testing.T, tokens map[string]platformToken, exchange map[string][]string, dispatch *dispatchAnswers, services []ServiceEntry) Deps {
	t.Helper()
	deps := Deps{ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(exchange), Access: dispatch.access, RepoProjects: dispatch.repoProjects}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held, "no service entry names a project to ask Dispatch about")
	deps.held.refresh(t.Context())
	return deps
}

// withAccessRefresh runs the rest of the test with the service asking Dispatch again every
// refresh.
func withAccessRefresh(t *testing.T, refresh time.Duration) {
	t.Helper()
	was := accessRefresh
	accessRefresh = refresh
	t.Cleanup(func() { accessRefresh = was })
}

// requireRefused requires err to be a refusal under label whose reason begins reason.
func requireRefused(t *testing.T, err error, label, reason string) {
	t.Helper()
	var refused *Refusal
	require.True(t, errors.As(err, &refused), "the connection was not refused: %v", err)
	require.Equal(t, label, refused.Label)
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

	// The same, through the hub, with the asks to Dispatch the service makes itself, and a static
	// publisher of every notification topic, which proves the served subscribe by a delivery.
	keys := testnats.NewCalloutKeys(t)
	static, staticSeed := publisher(t)
	url := testnats.StartWithCallout(t, keys, static)
	startServiceOf(t, url, keys, services, Deps{ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(exchange), Access: dispatch.access, RepoProjects: dispatch.repoProjects})
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
	served, err := nc.SubscribeSync(pubExceptions)
	require.NoError(t, err)
	_, err = nc.SubscribeSync(bus.ExceptionTopicPrefix + contracts.RoleTopicPrefix + "PRIV.>")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	awaitViolation(t, errs, bus.ExceptionTopicPrefix+contracts.RoleTopicPrefix+"PRIV.>")
	_, err = nc.SubscribeSync(nats.InboxPrefix + ">")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	awaitViolation(t, errs, nats.InboxPrefix+">")

	pub, err := testnats.ConnectSeed(t, url, staticSeed)
	require.NoError(t, err)
	defer pub.Close()
	pubException := bus.ExceptionTopicPrefix + contracts.RoleTopicPrefix + "PUB.implementer"
	require.NoError(t, pub.Publish(pubException, []byte("PUB's exception")))
	require.NoError(t, pub.Flush())
	msg, err := served.NextMsg(5 * time.Second)
	require.NoError(t, err, "the PUB daemon received nothing on PUB's role exceptions: its own subscribe was refused or held")
	require.Equal(t, "PUB's exception", string(msg.Data))
}

// No admission waits on Dispatch: the service makes a pass at its start and every 20 s after,
// and a decision reads only the pass it holds. Dispatch here answers its first ask about PUB at
// once and blocks three seconds on every later one. With a second pass blocked, PUB's daemon is
// admitted in under 100 ms from the first pass. The control reads Access inside the decision: the
// admission waits the three seconds, and this test fails naming how long it took.
func TestAnAdmissionNeverWaitsOnDispatch(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}, blockAfter: 1, block: 3 * time.Second}
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	asks, stop := context.WithCancel(t.Context())
	t.Cleanup(stop)
	go deps.held.refresh(asks)
	testwait.Eventually(t, "the first pass held", func() bool { return deps.held.latest() != nil })
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
}

// A held daemon's grant comes from one complete pass, never from two mixed: a mapping from one
// pass beside answers from another could serve a repository neither pass alone would. Pass 1
// reads mapping M1 (PUB1 maps acme/a, PUB2 acme/c) and PUB1's answer acme/a. Dispatch then renames
// acme/a to acme/b, which PUB2 maps too (M2 shares acme/b's lane), and answers PUB1 acme/a and
// acme/b; pass 2's read of the mapping fails. PUB1's held daemon is answered with pass 1's grant
// whole: acme/a's lane, and not acme/b's. The control mixes pass 1's mapping with pass 2's
// answers: PUB1's grant holds acme/b's lane, which M1 gives no other project.
func TestAHeldDaemonsGrantComesFromOnePass(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{"pub1-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{
		answers: map[string][]string{"PUB1": {"acme/a"}, "PUB2": {"acme/c"}},
		mapping: []projectaccess.RepoProject{{Repo: "acme/a", Project: "PUB1"}, {Repo: "acme/c", Project: "PUB2"}},
	}
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB1"}, slowDaemonAccount: {"PUB2"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	deps.held.refresh(t.Context())
	want, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PUB1"}, Repositories: []string{"acme/a"}, Identity: pubDaemonAccount})
	require.NoError(t, err)

	renamed := "acme/b"
	dispatch.mu.Lock()
	dispatch.answers = map[string][]string{"PUB1": {"acme/a", "acme/b"}, "PUB2": {"acme/b", "acme/c"}}
	dispatch.mapping = []projectaccess.RepoProject{{Repo: "acme/a", Project: "PUB1", RenamedTo: &renamed}, {Repo: "acme/b", Project: "PUB2"}, {Repo: "acme/c", Project: "PUB2"}}
	dispatch.mappingFailing = errors.New("projectaccess: dispatch answered /api/v1/settings/repo-projects 503 with the code \"\"")
	dispatch.mu.Unlock()
	deps.held.refresh(t.Context())
	decision, err := Decide(t.Context(), deps, services, serviceRequest("pub1-token"))
	require.NoError(t, err)
	require.False(t, holdsLane(decision.Grant, "acme/b"), "PUB1's grant holds acme/b's lane: pass 1's mapping was mixed with pass 2's answers")
	require.Equal(t, want, decision.Grant, "PUB1's grant is not pass 1's whole")
}

// A complete pass once held is kept while Dispatch cannot be reached, and the next complete pass
// replaces it.
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

// Dispatch refusing the broker's own account token (a 401 or 403, projectaccess.ErrBrokerRefused)
// is no outage to admit through: the token exchange refuses every trade that asks Dispatch then
// (oauth's refusedByDispatch), so a pass holding that refusal is complete, the auth service
// refuses a connection held to what it refused, under its own label and naming why, and logs it
// at error once a pass. Here Dispatch answers that PUB's person sees OPS and PUB, then refuses
// the broker's token while that person is off both access lists: PUB's daemon is refused, and one
// error line names the refusal for the pass's three questions (two projects and the mapping). The
// next pass replaces the refusal: once Dispatch answers again, with the person back, the daemon is
// admitted. The control keeps the last pass on a refusal, as on an outage: the daemon is admitted
// on the pass from before.
func TestDispatchRefusingTheBrokersTokenRefusesAHeldConnection(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{
		answers: map[string][]string{"OPS": {"acme/ops"}, "PUB": {"acme/pub"}},
		private: map[string]bool{"OPS": true, "PUB": true},
	}
	var logged bytes.Buffer
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"OPS", "PUB"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
		Log: slog.New(slog.NewTextHandler(&logged, nil)),
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	deps.held.refresh(t.Context())
	_, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	require.NoError(t, err, "PUB's daemon was refused while its person sees both of its projects")

	dispatch.hide("OPS", true)
	dispatch.hide("PUB", true)
	dispatch.fail(fmt.Errorf("%w: dispatch answered /api/v1/access/projects/PUB 403 with the code %q", projectaccess.ErrBrokerRefused, "ACCESS_ACCOUNT_REQUIRED"))
	deps.held.refresh(t.Context())
	_, err = Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
	require.Equal(t, 1, strings.Count(logged.String(), "level=ERROR"), "want one error line for the pass's three refused questions:\n%s", logged.String())
	require.Contains(t, logged.String(), "ACCESS_ACCOUNT_REQUIRED")
	require.NotContains(t, logged.String(), "level=WARN", "a refusal of the broker's token was logged as an outage:\n%s", logged.String())

	dispatch.fail(nil)
	dispatch.hide("OPS", false)
	dispatch.hide("PUB", false)
	deps.held.refresh(t.Context())
	_, err = Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	require.NoError(t, err, "Dispatch's answer after its refusal did not replace it")
}

// The service asks Dispatch again every accessRefresh on its own (heldAccess.run, which Start
// runs), the one thing that bounds how old a held answer is, so an answer changed at Dispatch
// reaches a decision with no ask the test makes. With the re-ask every 100 ms, PUB's daemon is
// admitted through the hub on Start's first pass; then PUB's person is taken off
// its access list at Dispatch, and within seconds the daemon's connection is refused, naming PUB as
// one its person cannot see. The control asks once at Start and never again: the daemon is
// admitted on that first answer for the whole wait.
func TestTheServiceAsksDispatchAgainOnItsOwn(t *testing.T) {
	withAccessRefresh(t, 100*time.Millisecond)
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}, private: map[string]bool{"PUB": true}}
	log := &lockedLog{}
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	startServiceOf(t, url, keys, services, Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
		Log: slog.New(slog.NewTextHandler(log, nil)),
	})
	connect := func() error {
		nc, err := nats.Connect(url, nats.UserInfo(natscred.ServiceUser, "pub-daemon-token"), nats.NoReconnect())
		if err == nil {
			nc.Close()
		}
		return err
	}
	testwait.Eventually(t, "the hub to admit PUB's daemon on the first answer", func() bool { return connect() == nil })

	dispatch.hide("PUB", true)
	testwait.WithinPolling(t, 5*time.Second, 50*time.Millisecond, "the service to ask Dispatch again and refuse PUB's daemon", func() bool {
		return errors.Is(connect(), nats.ErrAuthorization)
	})
	require.Contains(t, log.String(), "project not visible to the account's person: PUB", "the refusal was not the changed answer's:\n%s", log.String())
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
	requireRefused(t, err, labelNoExchangeEntry, "account has no exchange entry: "+slowDaemonAccount)
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
	requireRefused(t, err, labelProjectNotFound, "project not found: GONE")
}

// The connection is held to what the token exchange holds the account's token to: the person its
// exchange entry acts for must see each project (projectaccess.Visible). A private project that
// person sees is served, with its repository's lane, which an ask naming no person would not
// have found visible; one that person cannot see refuses the connection, naming the project, as
// the exchange refuses the token. The control drops the person's check: the PRIV2 row is served.
func TestAProjectItsPersonCannotSeeRefusesTheConnection(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{
		"seen-token":   {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
		"unseen-token": {account: slowDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
	}
	dispatch := &dispatchAnswers{
		answers: map[string][]string{"PRIV": {"acme/priv"}, "PRIV2": {"acme/priv2"}},
		private: map[string]bool{"PRIV": true, "PRIV2": true},
		unseen:  map[string]bool{"PRIV2": true},
	}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PRIV"}, slowDaemonAccount: {"PRIV2"}}, dispatch, services)

	decision, err := Decide(t.Context(), deps, services, serviceRequest("seen-token"))
	require.NoError(t, err, "a private project the entry's person sees was refused: the auth service asked about no person")
	want, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PRIV"}, Repositories: []string{"acme/priv"}, Identity: pubDaemonAccount})
	require.NoError(t, err)
	require.Equal(t, want, decision.Grant)

	_, err = Decide(t.Context(), deps, services, serviceRequest("unseen-token"))
	requireRefused(t, err, labelProjectNotVisible, "project not visible to the account's person: PRIV2")
}

// An exchange entry that names no project (the shared public-session account's form) gives the
// connection nothing to hold it to, and is refused saying so under a label of its own, not by the
// grant's emptiness.
func TestAnEntryNamingNoProjectIsRefusedByName(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{"empty-token": {account: slowDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}, slowDaemonAccount: nil}, dispatch, services)

	_, err := Decide(t.Context(), deps, services, serviceRequest("empty-token"))
	requireRefused(t, err, labelNoProjects, "account's exchange entry names no project to hold it to: "+slowDaemonAccount)
}

// A grant natsgrant.WithinProjects cannot hold to the entry's projects is refused naming why,
// under a label of its own: here Dispatch maps PUB to a repository that is not owner/name, which
// no GitHub lane can be written for.
func TestAGrantThatCannotBeHeldToItsProjectsIsRefusedByName(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"not-a-repository"}}}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}}, dispatch, services)

	_, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	requireRefused(t, err, labelGrantNotHoldable, "grant cannot be held to its projects: service daemon/"+pubDaemonAccount+`: natsgrant: scope repository "not-a-repository" is not owner/name`)
}

// holdsLane reports whether an entry of grant names repository's GitHub lane
// (natsgrant.GitHubFilter).
func holdsLane(grant natsgrant.Permissions, repository string) bool {
	return slices.ContainsFunc(slices.Concat(grant.Publish, grant.Subscribe), func(entry string) bool {
		return strings.Contains(entry, natsgrant.GitHubFilter(repository))
	})
}

// sharedLanesDropped is broker_nats_callout_shared_lanes_dropped_total as registry serves it, 0
// while it has counted nothing.
func sharedLanesDropped(t *testing.T, registry *metrics.Registry) int {
	t.Helper()
	scrape := httptest.NewRecorder()
	registry.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(scrape.Body.String()) {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "broker_nats_callout_shared_lanes_dropped_total "); found {
			var dropped int
			_, err := fmt.Sscan(value, &dropped)
			require.NoError(t, err, "the counter's line %q", line)
			return dropped
		}
	}
	return 0
}

// Two projects can map repositories onto one GitHub lane: a rename (PUB1's acme/a, renamed to
// acme/b, which PUB2 maps) or two spellings of one lane (acme/a.b and acme/a_b, a dot written
// "_"). A held daemon is not served such a lane when a project its entry does not name maps it:
// the daemon of neither entry is, each keeps its other lanes, the broker logs the lane and its
// projects at error once for the pass, and each repository withheld is counted
// (broker_nats_callout_shared_lanes_dropped_total). A lane shared only among one entry's own
// projects (PUB1 and PUB3, both PUB's) is that entry's, and served. The controls: a grant built
// without the drop serves PUB's daemon the lane PUB2 maps; one that counts the entry's own
// projects as another's withholds the lane PUB1 and PUB3 share.
func TestAHeldDaemonIsNotServedASharedLane(t *testing.T) {
	renamed := "acme/b"
	for _, row := range []struct {
		name     string
		pub      []string // the projects PUB's entry names; PUB2's entry names PUB2
		answers  map[string][]string
		mapping  []projectaccess.RepoProject
		served   []string // the repositories whose lanes PUB's daemon is served
		withheld []string // the repositories whose lanes neither daemon is served
		logged   string   // the projects of the one error line, "" for none
		dropped  int
	}{
		{
			name:    "a rename onto another entry's project's repository",
			pub:     []string{"PUB1"},
			answers: map[string][]string{"PUB1": {"acme/a", "acme/b"}, "PUB2": {"acme/b"}},
			mapping: []projectaccess.RepoProject{{Repo: "acme/a", Project: "PUB1", RenamedTo: &renamed}, {Repo: "acme/b", Project: "PUB2"}},
			served:  []string{"acme/a"}, withheld: []string{"acme/b"}, logged: "[PUB1 PUB2]", dropped: 2,
		},
		{
			name:    "two spellings of one lane in two entries' projects",
			pub:     []string{"PUB1"},
			answers: map[string][]string{"PUB1": {"acme/a", "acme/a.b"}, "PUB2": {"acme/a_b"}},
			served:  []string{"acme/a"}, withheld: []string{"acme/a.b"}, logged: "[PUB1 PUB2]", dropped: 2,
		},
		{
			name:    "two spellings of one lane in two of one entry's projects",
			pub:     []string{"PUB1", "PUB3"},
			answers: map[string][]string{"PUB1": {"acme/a", "acme/a.b"}, "PUB3": {"acme/a_b"}, "PUB2": {"acme/c"}},
			served:  []string{"acme/a", "acme/a.b", "acme/a_b"},
		},
		{
			name:    "a lane one of the entry's projects shares with a project outside it",
			pub:     []string{"PUB1", "PUB3"},
			answers: map[string][]string{"PUB1": {"acme/a"}, "PUB3": {"acme/a.b"}, "PUB2": {"acme/a_b"}},
			served:  []string{"acme/a"}, withheld: []string{"acme/a.b"}, logged: "[PUB2 PUB3]", dropped: 2,
		},
		{
			// GitHub names a repository without case, as Dispatch's own lane rule compares lanes.
			name:    "one lane spelled in two cases in two entries' projects",
			pub:     []string{"PUB1"},
			answers: map[string][]string{"PUB1": {"acme/a", "Acme/Shared"}, "PUB2": {"acme/shared"}},
			served:  []string{"acme/a"}, withheld: []string{"acme/shared", "Acme/Shared"}, logged: "[PUB1 PUB2]", dropped: 2,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			now := time.Now()
			services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
			tokens := map[string]platformToken{
				"pub-token":  {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
				"pub2-token": {account: slowDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
			}
			dispatch := &dispatchAnswers{answers: row.answers, mapping: row.mapping}
			var logged bytes.Buffer
			registry := metrics.New()
			deps := Deps{
				ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: row.pub, slowDaemonAccount: {"PUB2"}}),
				Access: dispatch.access, RepoProjects: dispatch.repoProjects,
				Log: slog.New(slog.NewTextHandler(&logged, nil)), Metrics: NewMetrics(registry),
			}
			deps.held = newHeldAccess(deps, services)
			require.NotNil(t, deps.held)
			deps.held.refresh(t.Context())

			pub, err := Decide(t.Context(), deps, services, serviceRequest("pub-token"))
			require.NoError(t, err)
			pub2, err := Decide(t.Context(), deps, services, serviceRequest("pub2-token"))
			require.NoError(t, err)
			for _, repository := range row.served {
				require.True(t, holdsLane(pub.Grant, repository), "PUB's held daemon is not served %s, which no project outside its entry maps", repository)
			}
			for _, repository := range row.withheld {
				for name, grant := range map[string]natsgrant.Permissions{"PUB": pub.Grant, "PUB2": pub2.Grant} {
					require.False(t, holdsLane(grant, repository), "%s's held daemon is served %s, whose lane a project outside its entry maps", name, repository)
				}
			}
			if row.logged == "" {
				require.NotContains(t, logged.String(), "level=ERROR", "a lane no project outside an entry maps was logged as shared:\n%s", logged.String())
			} else {
				require.Equal(t, 1, strings.Count(logged.String(), "level=ERROR"), "want one error line for the shared lane:\n%s", logged.String())
				require.Contains(t, logged.String(), "lane="+natsgrant.GitHubPrefix(row.withheld[0])+" ")
				require.Contains(t, logged.String(), `projects="`+row.logged+`"`)
			}
			require.Equal(t, row.dropped, sharedLanesDropped(t, registry), "the repositories withheld from the two grants")
		})
	}
}

// A whole mapping move (a repository Dispatch maps to PUB2 in a later pass, where it mapped it
// to PUB1 before, with no rename) is no shared lane: one project maps the lane in each pass, so
// the repository goes with its mapping, PUB1's daemon losing its lane and PUB2's gaining it at
// the complete pass that reads the move, with nothing logged or counted as shared. Who may move a
// mapping is Dispatch's write rule, not this guard's. The control keeps the earlier pass's lanes
// beside the later's: the moved repository reads as shared, and PUB2's daemon is not served it.
func TestAMappingMoveIsNoSharedLane(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{
		"pub1-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
		"pub2-token": {account: slowDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
	}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB1": {"acme/x"}, "PUB2": {"acme/y"}}}
	var logged bytes.Buffer
	registry := metrics.New()
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB1"}, slowDaemonAccount: {"PUB2"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
		Log: slog.New(slog.NewTextHandler(&logged, nil)), Metrics: NewMetrics(registry),
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	deps.held.refresh(t.Context())
	pub1, err := Decide(t.Context(), deps, services, serviceRequest("pub1-token"))
	require.NoError(t, err)
	require.True(t, holdsLane(pub1.Grant, "acme/x"))

	dispatch.set("PUB1")
	dispatch.set("PUB2", "acme/x", "acme/y")
	deps.held.refresh(t.Context())
	pub1, err = Decide(t.Context(), deps, services, serviceRequest("pub1-token"))
	require.NoError(t, err)
	pub2, err := Decide(t.Context(), deps, services, serviceRequest("pub2-token"))
	require.NoError(t, err)
	require.False(t, holdsLane(pub1.Grant, "acme/x"), "PUB1's daemon is still served acme/x, which Dispatch moved to PUB2")
	require.True(t, holdsLane(pub2.Grant, "acme/x"), "PUB2's daemon is not served acme/x, which Dispatch moved to it: the move read as a shared lane")
	require.NotContains(t, logged.String(), "level=ERROR", "a mapping move was logged as a shared lane:\n%s", logged.String())
	require.Zero(t, sharedLanesDropped(t, registry), "a mapping move was counted as a shared lane")
}

// A grant the drop leaves WithinProjects unable to hold is refused under shared_lane, naming the
// withheld lanes, and not as a grant that cannot be held at all. scopeGrant is handed a grant
// whose one subscribe is every repository's GitHub lane: PUB1's one repository, acme/a.b, shares
// its lane with PUB2's acme/a_b, so once it is withheld the subscribe keeps no entry. With PUB2
// mapping another lane the same grant is held, so the drop is what leaves it empty. The daemon's
// own grant, which a decision hands scopeGrant (serviceGrant), keeps its per-project subscribes
// whatever is withheld, so only a grant like this one reaches the refusal. The control drops the
// shared_lane case: the refusal is grant_not_holdable.
func TestADropThatLeavesNoGrantIsRefusedAsASharedLane(t *testing.T) {
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	exchange := map[string][]string{pubDaemonAccount: {"PUB1"}, slowDaemonAccount: {"PUB2"}}
	lanesOnly := natsgrant.Permissions{Publish: []string{contracts.DispatchIssueTopicPrefix + ">"}, Subscribe: []string{contracts.GithubTopicPrefix + ">"}}

	apart := heldDeps(t, nil, exchange, &dispatchAnswers{answers: map[string][]string{"PUB1": {"acme/a.b"}, "PUB2": {"acme/c"}}}, services)
	held, err := scopeGrant(t.Context(), apart, services[0], lanesOnly)
	require.NoError(t, err, "the grant was refused with no lane withheld")
	require.True(t, holdsLane(held, "acme/a.b"))

	shared := heldDeps(t, nil, exchange, &dispatchAnswers{answers: map[string][]string{"PUB1": {"acme/a.b"}, "PUB2": {"acme/a_b"}}}, services)
	_, err = scopeGrant(t.Context(), shared, services[0], lanesOnly)
	requireRefused(t, err, labelSharedLane, "grant cannot be held to its projects once the repositories whose lane a project outside the entry maps are withheld: service daemon/"+pubDaemonAccount)
}

// A held daemon is refused under no_complete_pass, inside the budget, until Dispatch's repository
// mapping has been read once: until then no repository is known to be no other project's, and the
// label says that, not a shared lane, so no operator goes looking for a second mapping. Here
// Dispatch answers about PUB at once and never answers the mapping: PUB's held daemon is refused
// in under 100 ms, and Dispatch's own connection, an all-projects entry, is admitted with its
// grant as before. The control takes an unread mapping as one with no row: the daemon is
// admitted.
func TestAHeldDaemonIsRefusedUntilTheLanesAreKnown(t *testing.T) {
	now := time.Now()
	dispatchEntry := ServiceEntry{Kind: KindDispatch, MachineID: "example-dispatch", Account: "system:serviceaccount:dispatch:dispatch", AllProjects: true}
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, dispatchEntry}
	tokens := map[string]platformToken{
		"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
		"dispatch-token":   {account: dispatchEntry.Account, issued: now, expiry: now.Add(time.Hour)},
	}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}, mappingBlocks: true}
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	asks, stop := context.WithCancel(t.Context())
	t.Cleanup(stop)
	go deps.held.refresh(asks)
	testwait.Eventually(t, "the first pass's ask about PUB", func() bool {
		dispatch.mu.Lock()
		defer dispatch.mu.Unlock()
		return dispatch.asked["PUB"] >= 1
	})

	began := time.Now()
	_, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	took := time.Since(began)
	requireRefused(t, err, "no_complete_pass", "no complete read of Dispatch's project answers and repository mapping is held yet")
	require.Less(t, took, 100*time.Millisecond, "the refusal took %s: it waited on Dispatch", took)

	decision, err := Decide(t.Context(), deps, services, serviceRequest("dispatch-token"))
	require.NoError(t, err, "Dispatch's own connection waited on the mapping")
	require.Equal(t, dispatchCalloutGrant("example-dispatch"), decision.Grant)
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
