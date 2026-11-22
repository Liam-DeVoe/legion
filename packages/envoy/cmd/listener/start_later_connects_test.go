package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/config"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testnats"
)

// laterConnectRefusedFor is how long TestTheListenerStartWaitsOutACredentialRefusalAtItsLaterConnects
// has the server refuse the nkey while a later connect of the start runs: past the ten tries a
// second apart that a connect without the wait makes, about ten seconds, so such a connect has
// ended with the refusal by then.
const laterConnectRefusedFor = 15 * time.Second

// TestTheListenerStartWaitsOutACredentialRefusalAtItsLaterConnects: the listener's start does not
// end once its main client has connected (connectListenerBus). It goes on to open the role lane
// (startRoleLane) and the session-consumer connection (startSessionDelivery), each on a connection
// of its own, and each ends the start when it fails. Behind a hub whose servers reload a new nkey
// one at a time, the main client can get in through the one server that has reloaded while a later
// connect meets one that has not. So each later connect waits out a refusal of the credential as
// the main client does, under the start's own bound (natsStart.option), and /healthz answers 503
// naming the refusal while it does. The server here admits the nkey for the main client, then
// refuses it for laterConnectRefusedFor while a later connect runs, then admits it again: the
// connect has not returned by the time the server admits the nkey again, and then it connects, and
// /healthz answers as on main. A later connect without the wait ends with the refusal after its ten
// tries, before the server admits the nkey again.
func TestTheListenerStartWaitsOutACredentialRefusalAtItsLaterConnects(t *testing.T) {
	const watchdog = 60 * time.Second
	logger := logging.NewWithWriter("test", io.Discard)
	for _, row := range []struct {
		name string
		// connect is the later connect under test, as main makes it, answering what closes it.
		connect func(cfg config.Service, client *bus.Client, start *natsStart) (func(), error)
	}{
		{
			name: "the role lane",
			connect: func(cfg config.Service, _ *bus.Client, start *natsStart) (func(), error) {
				lane, err := startRoleLane(cfg, nil, func(*nats.Msg) {}, nil, metrics.New(), logger, start)
				if err != nil {
					return nil, err
				}
				return func() { lane.stopWatch(); lane.client.Close() }, nil
			},
		},
		{
			name: "the session-consumer connection",
			connect: func(cfg config.Service, client *bus.Client, start *natsStart) (func(), error) {
				delivery, err := startSessionDelivery(cfg, bus.SessionConsumerInactiveThreshold, client, logger, nil, start)
				if err != nil {
					return nil, err
				}
				return delivery.close, nil
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			seed, public := testnats.User(t)
			_, fallback := testnats.User(t)
			server, uri := testnats.StartConfig(t, nkeyAuthorization(fallback, nkeyUser(public)), []string{"-js"}, true)
			t.Setenv(bus.NkeySeedEnvVar, seed)
			cfg := config.Service{MachineID: "start-later-connects", NATSURLs: []string{uri}, NATSReplicas: 1}
			start := newNATSStart(listenerCredentialWait)
			base := serveStartRoutes(t, start)
			client, err := connectListenerBus(cfg, nil, start)
			if err != nil {
				t.Fatalf("the start's main client did not connect while the server admitted its nkey: %v", err)
			}
			t.Cleanup(client.Close)

			if err := server.Reload(t, nkeyAuthorization(fallback)); err != nil {
				t.Fatalf("reload the server without the listener's nkey: %v", err)
			}
			type outcome struct {
				closer func()
				err    error
				at     time.Time
			}
			began := time.Now()
			returned := make(chan outcome, 1)
			go func() {
				closer, err := row.connect(cfg, client, start)
				returned <- outcome{closer, err, time.Now()}
			}()
			select {
			case got := <-returned:
				if got.closer != nil {
					got.closer()
				}
				t.Fatalf("%s's connect returned %s after it began, while the server refused the nkey and before it admitted it again: %v", row.name, got.at.Sub(began), got.err)
			case <-time.After(laterConnectRefusedFor):
			}
			if status, fields := probe(t, base, "/healthz"); status != http.StatusServiceUnavailable || !strings.Contains(fmt.Sprint(fields["error"]), authorizationViolation) {
				t.Fatalf("/healthz answered %d %v while %s's connect waited out a refusal, want 503 naming %q", status, fields, row.name, authorizationViolation)
			}
			if err := server.Reload(t, nkeyAuthorization(fallback, nkeyUser(public))); err != nil {
				t.Fatalf("reload the server with the listener's nkey: %v", err)
			}
			var got outcome
			select {
			case got = <-returned:
			case <-time.After(watchdog):
				t.Fatalf("watchdog: %s's connect had not returned %s after the server admitted the nkey again", row.name, watchdog)
			}
			if got.err != nil {
				t.Fatalf("%s's connect returned %v %s after it began, want it to connect once the server admits the nkey again", row.name, got.err, got.at.Sub(began))
			}
			t.Cleanup(got.closer)
			if status, fields := probe(t, base, "/healthz"); status != http.StatusOK || fields["status"] != "starting" {
				t.Fatalf("/healthz after %s's connect = %d %v, want main's 200 starting", row.name, status, fields)
			}
		})
	}
}

// The line a start on NATS ends with names the credential only when the start met a refusal of
// it: an operator who reads "the NATS credential: nkey user ..." looks for that user in the hub's
// authorization, which is the wrong place when the start ended for another reason, a stream the
// cluster could not reconcile, say. The JSON lines the later connects end the start with add the
// credential on the same terms (natsStart.refusedCredential).
func TestTheStartsLastLineNamesTheCredentialOnlyAfterARefusal(t *testing.T) {
	seed, public := testnats.User(t)
	environment := func(name string) (string, bool) {
		if name == bus.NkeySeedEnvVar {
			return seed, true
		}
		return "", false
	}
	start := newNATSStart(listenerCredentialWait)
	failed := errors.New("ensure stream ENVOY_NOTIFICATIONS: ENVOY_NATS_REPLICAS=3: replicas > 1 not supported in non-clustered mode")
	line := startFailureLine(failed, start, environment)
	if want := "connect to NATS and reconcile the " + bus.Stream + " stream: " + failed.Error(); !strings.HasPrefix(line, want) {
		t.Fatalf("the start's last line is %q, want it to begin %q", line, want)
	}
	if strings.Contains(line, public) || strings.Contains(line, "credential") {
		t.Fatalf("the start's last line %q names the credential, though nothing refused it", line)
	}
	if attrs := start.refusedCredential(environment); len(attrs) != 0 {
		t.Fatalf("a start that met no refusal adds %v to the line that ends it, want nothing", attrs)
	}

	start.refused(errors.New("nats: Authorization Violation"))
	line = startFailureLine(errors.New("bus: the NATS server still refused the credential when the start's 3m0s wait ran out: nats: Authorization Violation"), start, environment)
	if !strings.Contains(line, "the NATS credential: nkey user "+public) {
		t.Fatalf("the start's last line %q does not name the credential the server refused, nkey user %s", line, public)
	}
	if attrs := start.refusedCredential(environment); len(attrs) != 1 || attrs[0].Key != "nats_credential" || attrs[0].Value.String() != "nkey user "+public {
		t.Fatalf("a start that met a refusal adds %v to the line that ends it, want nats_credential=nkey user %s", attrs, public)
	}
}
