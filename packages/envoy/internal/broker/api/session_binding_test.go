// session_binding_test.go drives the binding of a session name to the daemon that registered it,
// at POST /v1/oauth/exchange/pod-sessions and at the token exchange on the real broker api_test.go
// mounts: a second daemon listed for the same pod account is refused a name the first holds, and a
// pod it registered cannot trade under that name.
package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/logtest"
)

// exchangeAudience is the audience the test server's token exchange (brokertest.NewOAuthProvider)
// takes a pod's token at: its own, neither podAudience nor registrationAudience.
const exchangeAudience = "legion-broker-exchange"

// heldMessage is the route's WARN line for a registration refused because another registrar holds
// the session name.
const heldMessage = "broker: a pod's session registration was refused: another registrar holds the session name"

// admits fails the test unless the registration of the pod podToken names for session, written by
// the registrar whose login token registrar is, is answered 200 naming that pod and that session;
// what names the registration.
func (ts *testServer) admits(t *testing.T, what, registrar, podToken, pod, session string) {
	t.Helper()
	status, body := ts.registerPodSession(t, registrar, podToken, session)
	if got := decode[wirePodSession](t, body); status != http.StatusOK || got.PodUID != pod || got.Session != session {
		t.Fatalf("%s = %d %s, want 200 naming pod %s and session %s", what, status, body, pod, session)
	}
}

// TestASessionNameIsRefusedToASecondDaemon: a session name belongs to the daemon that registered
// it. Daemon A (daemonAccount) registers its pod P_A for S. Daemon B (otherDaemonAccount), listed
// for the same pod account, registering its own pod P_B for S, and A's own pod P_A for S, is
// refused each time 409 SESSION_HELD_BY_ANOTHER_REGISTRAR naming S and A, with a WARN line naming
// B, A, the pod and S, and nothing it asked for is written. B is listed, so it registers P_B under
// a name of its own; and once A's rows of S have lapsed, B registers P_B for S too: what refuses it
// is A's live registration of the name, not B's login or its entry in the list.
func TestASessionNameIsRefusedToASecondDaemon(t *testing.T) {
	capture := &logtest.Recorder{}
	logtest.SetDefault(t, capture)
	ts := newTestServer(t)
	const session, ownSession, podA, podB = "implementer-g1", "reviewer-g1", "pod-uid-of-a", "pod-uid-of-b"
	daemonA, daemonB := ts.accountToken(t, daemonAccount, "", time.Hour), ts.accountToken(t, otherDaemonAccount, "", time.Hour)
	podToken := func(pod string) string { return ts.accountToken(t, brokertest.ExchangeRolesAccount, pod, time.Hour) }
	registrations := func() []string {
		return ts.column(t, `select pod_uid || ' of ' || registrar from oauth_pod_sessions where session = $1 order by pod_uid`, session)
	}

	ts.admits(t, "A's registration of "+podA+" for "+session, daemonA, podToken(podA), podA, session)
	var want []map[string]any
	for _, pod := range []string{podB, podA} {
		status, body := ts.registerPodSession(t, daemonB, podToken(pod), session)
		refusal := decode[wireError](t, body)
		if status != http.StatusConflict || refusal.Code != "SESSION_HELD_BY_ANOTHER_REGISTRAR" || !strings.Contains(refusal.Error, session) || !strings.Contains(refusal.Error, daemonAccount) {
			t.Fatalf("%s, listed for %s, registering %s for %s, which %s holds = %d %s; want 409 SESSION_HELD_BY_ANOTHER_REGISTRAR naming %s and %s",
				otherDaemonAccount, brokertest.ExchangeRolesAccount, pod, session, daemonAccount, status, strings.TrimSpace(string(body)), session, daemonAccount)
		}
		want = append(want, map[string]any{
			"level": slog.LevelWarn, "msg": heldMessage, "service": "legion-daemon", "registrar": otherDaemonAccount, "holder": daemonAccount,
			"account": brokertest.ExchangeRolesAccount, "pod": pod, "session": session,
		})
	}
	if got, wantRows := registrations(), []string{podA + " of " + daemonAccount}; !slices.Equal(got, wantRows) {
		t.Fatalf("oauth_pod_sessions holds %v for %s after %s was refused it, want %v alone", got, session, otherDaemonAccount, wantRows)
	}
	var logged []map[string]any
	for _, record := range capture.Records() {
		if record.Message != heldMessage {
			continue
		}
		fields := map[string]any{"level": record.Level, "msg": record.Message}
		record.Attrs(func(a slog.Attr) bool { fields[a.Key] = a.Value.Any(); return true })
		logged = append(logged, fields)
	}
	if !reflect.DeepEqual(logged, want) {
		t.Fatalf("the two refused registrations logged %v, want exactly %v", logged, want)
	}

	ts.admits(t, "B's registration of "+podB+" for a name of its own, "+ownSession, daemonB, podToken(podB), podB, ownSession)
	if _, err := ts.Store.Pool.Exec(context.Background(), `update oauth_pod_sessions set expires_at = now() - interval '1 minute'
		where session = $1 and registrar = $2`, session, daemonAccount); err != nil {
		t.Fatalf("lapse %s's registrations of %s: %v", daemonAccount, session, err)
	}
	ts.admits(t, "B's registration of "+podB+" for "+session+" once A's registrations of it lapsed", daemonB, podToken(podB), podB, session)
}

// TestAPodAnotherDaemonRegisteredCannotTradeUnderAHeldName: daemon A registers its pod P_A for S
// and P_A trades under S at POST /v1/oauth/token. Daemon B, listed for the same pod account,
// registers its pod P_B for a name of its own, S_B, and is refused S. P_B's trade under S is then
// refused invalid_grant with the description of a pod no registration names
// (pod_session_not_registered:), while its trade under S_B is answered. Both pods run as one
// account, which is one principal at the exchange, so the claim on the name, which binds a name
// to a principal, cannot be what refuses P_B: the registration A's binding of S keeps from B is.
func TestAPodAnotherDaemonRegisteredCannotTradeUnderAHeldName(t *testing.T) {
	ts := newTestServer(t)
	const session, ownSession, podA, podB = "implementer-g1", "reviewer-g1", "pod-uid-of-a", "pod-uid-of-b"
	daemonA, daemonB := ts.accountToken(t, daemonAccount, "", time.Hour), ts.accountToken(t, otherDaemonAccount, "", time.Hour)
	podToken := func(pod string) string { return ts.accountToken(t, brokertest.ExchangeRolesAccount, pod, time.Hour) }
	tradeToken := func(pod string) string {
		return ts.audienceToken(t, brokertest.ExchangeRolesAccount, pod, exchangeAudience, time.Hour)
	}

	ts.admits(t, "A's registration of "+podA+" for "+session, daemonA, podToken(podA), podA, session)
	if answer := ts.exchangePodToken(t, tradeToken(podA), session); answer.status != http.StatusOK || !answer.issued {
		t.Fatalf("%s's trade under %s, which %s registered it for = %+v, want 200 with an access token", podA, session, daemonAccount, answer)
	}
	ts.admits(t, "B's registration of "+podB+" for its own name, "+ownSession, daemonB, podToken(podB), podB, ownSession)
	// Each check below reports and goes on, so a registration of S wrongly admitted also shows
	// whether P_B can then trade under S.
	status, body := ts.registerPodSession(t, daemonB, podToken(podB), session)
	if code := decode[wireError](t, body).Code; status != http.StatusConflict || code != "SESSION_HELD_BY_ANOTHER_REGISTRAR" {
		t.Errorf("%s registering %s for %s, which %s holds = %d %s, want 409 SESSION_HELD_BY_ANOTHER_REGISTRAR", otherDaemonAccount, podB, session, daemonAccount, status, strings.TrimSpace(string(body)))
	}

	answer := ts.exchangePodToken(t, tradeToken(podB), session)
	if answer.status != http.StatusBadRequest || answer.code != "invalid_grant" || answer.issued || !strings.HasPrefix(answer.description, oauthwire.PodSessionNotRegistered+":") {
		t.Errorf("%s's trade under %s, which %s holds and did not register it for = %+v; want 400 invalid_grant, its description beginning %s:, and no token",
			podB, session, daemonAccount, answer, oauthwire.PodSessionNotRegistered)
	}
	if answer := ts.exchangePodToken(t, tradeToken(podB), ownSession); answer.status != http.StatusOK || !answer.issued {
		t.Errorf("%s's trade under %s, which %s registered it for = %+v, want 200 with an access token: the refusal above is the name, not the pod", podB, ownSession, otherDaemonAccount, answer)
	}
}
