package natscallout

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// answered is one service's answer: the entry it was decided for and the grant it holds.
type answered struct {
	entry ServiceEntry
	grant natsgrant.Permissions
}

// measureInbox is the one fixed reply prefix of the measurement job's grant (natsgrant.Measure),
// which its client sets itself and the auth service answers as it is.
const measureInbox = "_INBOX_envoy_measure.>"

// readsAnotherInbox names each way an answer of answers reads a reply that is not its own: it
// subscribes an inbox subject (one whose first token is _INBOX or begins _INBOX_) other than its
// own account's reply prefix (natscred.InboxPrefix) and, for a listener and Dispatch, its session
// reply inbox (natsgrant.SessionReplyInbox), the measurement job's fixed prefix aside; it lacks
// either of those; or a subscribe entry of it can match a subject another answer's reply spaces,
// $JS.API.> or $JS.EVENT.> hold (server.SubjectsCollide), so that >, _INBOX.> and $JS.> each
// fail; or it publishes >.
func readsAnotherInbox(answers []answered) []string {
	var faults []string
	spaces := func(a answered) []string {
		own := []string{natscred.InboxPrefix(a.entry.Account) + ".>"}
		switch a.entry.Kind {
		case KindListener, KindDispatch:
			own = append(own, natsgrant.SessionReplyInbox(a.entry.Account)+".>")
		}
		return own
	}
	for _, a := range answers {
		who := a.entry.name()
		own := spaces(a)
		if a.entry.Kind != KindMeasure {
			for _, space := range own {
				if !slices.Contains(a.grant.Subscribe, space) {
					faults = append(faults, fmt.Sprintf("%s does not subscribe its own %s", who, space))
				}
			}
		}
		for _, entry := range a.grant.Subscribe {
			subject := strings.Fields(entry)[0]
			first, _, _ := strings.Cut(subject, ".")
			inbox := first == "_INBOX" || strings.HasPrefix(first, "_INBOX_")
			mine := slices.Contains(own, subject) || a.entry.Kind == KindMeasure && subject == measureInbox
			if inbox && !mine {
				faults = append(faults, fmt.Sprintf("%s subscribes %s, an inbox not its own", who, subject))
			}
			for _, reserved := range []string{"$JS.API.>", "$JS.EVENT.>"} {
				if server.SubjectsCollide(subject, reserved) {
					faults = append(faults, fmt.Sprintf("%s subscribes %s, which reaches %s", who, subject, reserved))
				}
			}
			for _, other := range answers {
				if other.entry.Account == a.entry.Account {
					continue
				}
				for _, space := range spaces(other) {
					if server.SubjectsCollide(subject, space) {
						faults = append(faults, fmt.Sprintf("%s subscribes %s, which reaches %s's %s", who, subject, other.entry.name(), space))
					}
				}
			}
		}
		if slices.Contains(a.grant.Publish, ">") {
			faults = append(faults, who+" publishes >")
		}
	}
	return faults
}

// Every answer of the auth service reads only its own replies, across identities. Each service
// kind and a daemon subscribe their account's own reply prefix in place of _INBOX.>, the listener
// and Dispatch also their session reply inbox, and no other inbox subject; no subscribe entry of
// any answer can match another identity's reply spaces, $JS.API.> or $JS.EVENT.> (whose
// $JS.EVENT.ADVISORY.API carries every API request's and answer's body); none publishes >. Live,
// through the auth service: a second listener's subscription to the first's prefix, and to
// _INBOX.>, is refused by name, the first's CONSUMER.INFO of its own durable still answers on its
// prefixed response inbox, and the second sees no reply of the first's to forge. The controls are
// today's _INBOX.> grant, which the check names and under which the second listener receives the
// first's CONSUMER.INFO answer, and a $JS.> subscribe planted on the daemon's answer, which no
// literal comparison would catch and the collision check names.
func TestNoCalloutGrantReadsAnotherInbox(t *testing.T) {
	now := time.Now()
	services := exampleServices[:4] // one all-projects entry of each kind
	tokens := map[string]platformToken{}
	for _, entry := range services {
		tokens[entry.name()] = platformToken{account: entry.Account, google: entry.Google, issued: now, expiry: now.Add(time.Hour)}
	}
	deps := Deps{ServiceToken: serviceTokens(tokens)}
	var answers []answered
	for _, entry := range services {
		decision, err := Decide(t.Context(), deps, services, serviceRequest(entry.name()))
		require.NoError(t, err, entry.name())
		answers = append(answers, answered{entry: entry, grant: decision.Grant})
	}

	t.Run("every answer", func(t *testing.T) {
		require.Empty(t, readsAnotherInbox(answers))
	})
	t.Run("control: today's _INBOX.> grants", func(t *testing.T) {
		var today []answered
		for _, entry := range services {
			today = append(today, answered{entry: entry, grant: serviceGrant(entry)})
		}
		faults := strings.Join(readsAnotherInbox(today), "\n")
		require.Contains(t, faults, "listener/example-hub subscribes _INBOX.>, an inbox not its own")
		require.Contains(t, faults, "dispatch/example-dispatch subscribes _INBOX.>, which reaches")
		require.Contains(t, faults, "daemon/"+daemonAccount+" subscribes _INBOX.>")
	})
	t.Run("control: a $JS.> subscribe planted on the daemon's answer", func(t *testing.T) {
		planted := slices.Clone(answers)
		for i := range planted {
			if planted[i].entry.Kind == KindDaemon {
				planted[i].grant.Subscribe = append(slices.Clone(planted[i].grant.Subscribe), "$JS.>")
			}
		}
		faults := strings.Join(readsAnotherInbox(planted), "\n")
		require.Contains(t, faults, "daemon/"+daemonAccount+" subscribes $JS.>, which reaches $JS.API.>")
		require.Contains(t, faults, "daemon/"+daemonAccount+" subscribes $JS.>, which reaches $JS.EVENT.>")
	})

	t.Run("live", func(t *testing.T) {
		leak := listenerLeak(t, Decide, true)
		require.True(t, leak.answered, "the first listener's CONSUMER.INFO of its own durable got no answer on its own prefix")
		require.Empty(t, leak.seen, "the second listener received another's replies")
		require.Subset(t, leak.refused, []string{natscred.InboxPrefix(leakFirst) + ".>", nats.InboxPrefix + ">"})
	})
	t.Run("live control: today's _INBOX.> listener grant", func(t *testing.T) {
		today := func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
			decision, err := Decide(ctx, deps, services, req)
			for _, entry := range services {
				if err == nil && entry.name() == decision.Name {
					decision.Grant = serviceGrant(entry)
				}
			}
			return decision, err
		}
		leak := listenerLeak(t, today, false)
		require.True(t, leak.answered)
		require.NotEmpty(t, leak.seen, "under today's grant the second listener should have read the first's CONSUMER.INFO answer")
	})
}

// The two listeners of listenerLeak, each an all-projects listener entry of its own account.
const (
	leakFirst  = "system:serviceaccount:example-ns:listener-a"
	leakSecond = "system:serviceaccount:example-ns:listener-b"
)

// leaked is what listenerLeak saw: whether the first listener's CONSUMER.INFO answered, the
// subjects of every message the second received, and the subscribes the server refused it.
type leaked struct {
	answered bool
	seen     []string
	refused  []string
}

// listenerLeak starts a server whose auth service decides with decide, connects two listeners by
// their platform tokens, the first with its durable on the notification stream, and has the
// second subscribe the first's reply prefix and _INBOX.> while the first asks CONSUMER.INFO of
// its durable. prefixed says whether the clients set their account's reply prefix
// (natscred.InboxPrefix), as internal/bus's service connections do.
func listenerLeak(t *testing.T, decide decideFunc, prefixed bool) leaked {
	t.Helper()
	keys := testnats.NewCalloutKeys(t)
	adminSeed, adminKey := testnats.User(t)
	url := testnats.StartWithCallout(t, keys, testnats.StaticUser{NKey: adminKey, Publish: []string{">"}, Subscribe: []string{">"}})
	services := []ServiceEntry{
		{Kind: KindListener, MachineID: "hub-a", Account: leakFirst, AllProjects: true},
		{Kind: KindListener, MachineID: "hub-b", Account: leakSecond, AllProjects: true},
	}
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
		return path
	}
	now := time.Now()
	raw := map[string]string{leakFirst: unsignedServiceToken(leakFirst, time.Hour), leakSecond: unsignedServiceToken(leakSecond, time.Hour)}
	service, err := start(t.Context(), Settings{
		URLs: []string{url}, SeedFile: write("service.seed", keys.ServiceSeed),
		IssuerSeedFile: write("issuer.seed", keys.IssuerSeed), XKeySeedFile: write("xkey.seed", keys.XKeySeed), Services: services,
	}, Deps{ServiceToken: serviceTokens(map[string]platformToken{
		raw[leakFirst]:  {account: leakFirst, issued: now, expiry: now.Add(time.Hour)},
		raw[leakSecond]: {account: leakSecond, issued: now, expiry: now.Add(time.Hour)},
	}), Log: slog.New(slog.DiscardHandler)}, decide)
	require.NoError(t, err)
	t.Cleanup(service.Close)
	ready, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, service.Ready(ready))

	admin, err := testnats.ConnectSeed(t, url, adminSeed)
	require.NoError(t, err)
	defer admin.Close()
	js, err := admin.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: bus.Stream, Subjects: []string{"notifications.>"}})
	require.NoError(t, err)
	durable := bus.ListenerDurable("hub-a")
	_, err = js.AddConsumer(bus.Stream, &nats.ConsumerConfig{Durable: durable, AckPolicy: nats.AckExplicitPolicy})
	require.NoError(t, err)

	connect := func(account string, options ...nats.Option) *nats.Conn {
		credential, _ := natscred.Service(natscred.ServiceFile(write(account+".token", raw[account])))
		options = append(options, credential, nats.NoReconnect(), nats.Timeout(10*time.Second))
		if prefixed {
			options = append(options, nats.CustomInboxPrefix(natscred.InboxPrefix(account)))
		}
		nc, err := nats.Connect(url, options...)
		require.NoError(t, err, "connect as %s", account)
		t.Cleanup(nc.Close)
		return nc
	}
	var mu sync.Mutex
	var result leaked
	errs := make(chan error, 16)
	second := connect(leakSecond, nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		mu.Lock()
		defer mu.Unlock()
		for _, subject := range []string{natscred.InboxPrefix(leakFirst) + ".>", nats.InboxPrefix + ">"} {
			if strings.Contains(err.Error(), `Subscription to "`+subject+`"`) {
				result.refused = append(result.refused, subject)
			}
		}
		select {
		case errs <- err:
		default:
		}
	}))
	for _, subject := range []string{natscred.InboxPrefix(leakFirst) + ".>", nats.InboxPrefix + ">"} {
		_, err := second.Subscribe(subject, func(m *nats.Msg) {
			mu.Lock()
			defer mu.Unlock()
			result.seen = append(result.seen, m.Subject)
		})
		require.NoError(t, err)
	}
	require.NoError(t, second.Flush())
	if prefixed {
		awaitViolation(t, errs, natscred.InboxPrefix(leakFirst)+".>")
		awaitViolation(t, errs, nats.InboxPrefix+">")
	}

	first := connect(leakFirst)
	firstJS, err := first.JetStream()
	require.NoError(t, err)
	info, err := firstJS.ConsumerInfo(bus.Stream, durable)
	result.answered = err == nil && info.Name == durable
	require.NoError(t, second.Flush())
	time.Sleep(200 * time.Millisecond) // a reply the second could read has had its round trip
	mu.Lock()
	defer mu.Unlock()
	return leaked{answered: result.answered, seen: slices.Clone(result.seen), refused: slices.Clone(result.refused)}
}
