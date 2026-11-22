package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/ciphertoken"
	"github.com/sjawhar/envoy/internal/ciphertoken/ciphertokentest"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/config"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// edgeProject is the one project this package's edge listeners serve.
const edgeProject = "PRIV"

// startEdgeListenerProcess starts binary as an edge listener of edgeProject, machine machineID,
// against the NATS at uri, with env added to its environment: on a loopback port the test holds
// (startListenerProcess says why), taking broker access tokens from broker and no other
// credential, as an edge listener takes them.
func startEdgeListenerProcess(t *testing.T, binary, uri, machineID string, broker *ciphertokentest.StubBroker, env ...string) *listenerProcess {
	t.Helper()
	held := cmdtest.HoldPort(t)
	account := filepath.Join(t.TempDir(), "account-token")
	if err := os.WriteFile(account, []byte("edge-listener-account-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Env = append([]string{
		"PORT=" + strconv.Itoa(held.Port),
		"ENVOY_LISTEN_HOST=127.0.0.1",
		"ENVOY_MACHINE_ID=" + machineID,
		"NATS_URLS=" + uri,
		config.EdgeProjectsEnvVar + "=" + edgeProject,
		EnvBrokerURL + "=" + broker.Server.URL,
		EnvBrokerAccountTokenFile + "=" + account,
		EnvPublicURL + "=http://127.0.0.1:" + strconv.Itoa(held.Port),
	}, env...)
	held.Pass(cmd, "ENVOY_LISTEN_FD")
	return &listenerProcess{Process: cmdtest.Start(t, "the edge listener "+machineID, cmd), port: held.Port}
}

// edgeServerViolation matches a permissions violation nats-server logs, capturing its kind and
// the subject it names.
var edgeServerViolation = regexp.MustCompile(`(Publish|Subscription) Violation - .*?Subject "([^"]*)"`)

// listenerStateRequests are the requests on the listener state bucket (bus.ListenerStateBucket)
// a nats-server's log holds refused (edgeServerViolation), each as its kind and subject: a
// JetStream API call that names the bucket's stream, or a write of one of its keys.
func listenerStateRequests(log string) []string {
	stream := "KV_" + bus.ListenerStateBucket
	var found []string
	for _, match := range edgeServerViolation.FindAllStringSubmatch(log, -1) {
		if subject := match[2]; strings.HasPrefix(subject, "$KV."+bus.ListenerStateBucket+".") || slices.Contains(strings.Split(subject, "."), stream) {
			found = append(found, match[1]+" "+subject)
		}
	}
	return found
}

// An edge listener reads the stream and never creates it, so its start ends, naming the stream,
// on a server that holds none, and it starts on the same server once a shared listener, which
// owns the stream, has created it.
func TestTheEdgeRefusesToStartWithoutTheStream(t *testing.T) {
	binary := cmdtest.Build(t, "envoy-listener")
	uri := testnats.URL(t)
	broker := ciphertokentest.NewStubBroker(t)

	refused := startEdgeListenerProcess(t, binary, uri, "edge-no-stream", broker)
	refused.WaitExit(t, "its start")
	output := refused.Output.String()
	if code := refused.Cmd.ProcessState.ExitCode(); code != 1 {
		t.Fatalf("the edge listener exited %d on a server with no stream, want 1:\n%s", code, output)
	}
	if want := "the server holds no " + bus.Stream + " stream, and an edge listener may not create it"; !strings.Contains(output, want) {
		t.Fatalf("the edge listener's start did not end naming the missing stream (%q):\n%s", want, output)
	}

	shared := startListenerProcess(t, binary, uri, "shared-makes-the-stream")
	shared.waitHealthy(t)
	shared.Terminate(t)
	shared.WaitExit(t, "SIGTERM")

	started := startEdgeListenerProcess(t, binary, uri, "edge-with-stream", broker)
	started.waitHealthy(t)
	if output := started.Output.String(); strings.Contains(output, "role lane subscribed") || !strings.Contains(output, "role lane not subscribed") {
		t.Fatalf("the edge listener opened a role lane, or did not say it opened none:\n%s", output)
	}
}

// An edge listener opens no delivery lease and sends nothing on the listener state bucket, as the
// server records it. The edge runs under its rendered grant (natsgrant.Edge), which holds no call
// of envoy_listener, so the server refuses, and logs, every request the listener makes there: the
// lease's open, a delivery ledger or last-seen read or write. The admin made the bucket before the
// edge started, as a shared listener's start does, so it is there to be used. The edge starts,
// registers a session under a broker token of its project (a listener with the lease records the
// session's last-seen time in the bucket here) and relays a send to it, and the server logs no
// refusal on the bucket but one the edge's own user is refused, made last, which shows the log is
// read. A request is the one route to the bucket, so a listener that sent none holds no lease.
func TestAnEdgeListenerOpensNoLeaseAndSendsNothingOnTheListenerStateBucket(t *testing.T) {
	const (
		machineID = "example-edge-nolease"
		sessionID = "ses_edge_nolease"
		token     = "access-token-of-the-edge-session"
		message   = "example message to the edge"
	)
	grant, err := natsgrant.Edge(machineID, []string{edgeProject})
	if err != nil {
		t.Fatalf("the edge's grant: %v", err)
	}
	seed, public := testnats.User(t)
	granted := testnats.StartGranted(t, testnats.GrantedUser{Public: public, Grant: testnats.Grant{
		Publish:   testnats.GrantRules{Allow: grant.Publish},
		Subscribe: testnats.GrantRules{Allow: grant.Subscribe},
	}})
	admin, err := bus.ConnectOwningStream([]string{granted.AdminURL}, bus.WithReplicas(1), bus.WithName("edge-lease-test admin"))
	if err != nil {
		t.Fatalf("the admin creates the stream: %v", err)
	}
	t.Cleanup(admin.Close)
	for _, bucket := range append(bus.ListenerBuckets(), bus.ListenerStateBucket) {
		if _, err := admin.JS().CreateKeyValue(&natsgo.KeyValueConfig{Bucket: bucket, Storage: natsgo.FileStorage}); err != nil {
			t.Fatalf("the admin makes the %s bucket: %v", bucket, err)
		}
	}
	broker := ciphertokentest.NewStubBroker(t)
	caller := ciphertokentest.NewCaller(t)
	broker.Says(token, ciphertoken.Introspection{
		Active:       true,
		Session:      sessionID,
		Person:       "outside-person@example.test",
		ProjectScope: ciphertoken.ProjectScopeNamed,
		Projects:     []string{edgeProject},
		ClientID:     ciphertoken.ExchangeClientID,
		Thumbprint:   caller.JKT(),
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	// The edge's public origin is the one signedListenerCall signs for: of a variable named twice,
	// the process reads the last.
	edge := startEdgeListenerProcess(t, cmdtest.Build(t, "envoy-listener"), granted.URL(), machineID, broker,
		bus.NkeySeedEnvVar+"="+seed, EnvPublicURL+"="+listenerBrokerOrigin)
	edge.waitHealthy(t)

	standIn := startSessionStandIn(t, granted.AdminURL, sessionID, answerDone)
	if status, _, answer := signedListenerCall(t, edge.port, caller, token, "/v1/interests/subscribe",
		`{"session_id":"`+sessionID+`","topics":[],"self_subscribed":true,"delivery":{"consumer":true,"epoch":"edge-epoch"}}`); status != http.StatusOK || !strings.Contains(answer, `"mode":"consumer"`) {
		t.Fatalf("the edge's registration of %s answered %d %s, want 200 consumer delivery:\n%s", sessionID, status, answer, edge.Output.String())
	}
	if status, _, answer := signedListenerCall(t, edge.port, caller, token, "/v1/messages/send",
		`{"target_session":"`+sessionID+`","source_session":"`+sessionID+`","message":"`+message+`"}`); status < 200 || status > 299 {
		t.Fatalf("the edge's send to %s answered %d %s:\n%s", sessionID, status, answer, edge.Output.String())
	}
	if frame := standIn.collect(t, 1, 15*time.Second)[0]; frame.item.PayloadSummary != message {
		t.Fatalf("the frame relayed to %s carries %q, want the send", sessionID, frame.item.PayloadSummary)
	}

	// The refusal provoked last, by the edge's own user, on a key no listener writes: a reading
	// that finds it and no other request on the bucket saw the listener send none.
	provoked := "$KV." + bus.ListenerStateBucket + ".edge-test-probe"
	probe, err := testnats.ConnectSeed(t, granted.URL(), seed)
	if err != nil {
		t.Fatalf("connect as the edge's user: %v", err)
	}
	t.Cleanup(probe.Close)
	if err := probe.Publish(provoked, nil); err != nil {
		t.Fatal(err)
	}
	_ = probe.Flush()
	waitFor(t, 10*time.Second, "the server to log its refusal of "+provoked+", so its log is read", func() bool {
		return slices.Contains(listenerStateRequests(granted.Log()), "Publish "+provoked)
	})
	for _, request := range listenerStateRequests(granted.Log()) {
		if request != "Publish "+provoked {
			t.Fatalf("the server refused the edge listener %s on the %s bucket: an edge listener opens no lease and sends nothing there:\n%s",
				request, bus.ListenerStateBucket, edge.Output.String())
		}
	}
}
