// packages/envoy/internal/cipher/proxy/enrollment_test.go
//
// The rows about every live grant of one enrollment: a command holds the placeholders of each
// grant its session has, under one agent-secrets run nested in another or a role's own grant
// beside one agent-secrets asked for, and its proxy password is the placeholder of one of them.
// So a host any live grant of the enrollment binds is read (serveConnect), a request is handled
// for the grant whose placeholder it carries (serveBound), and a call carried for a grant other
// than the connection's own is held to that grant as well (watchCarried).
package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
	"github.com/sjawhar/envoy/internal/cipher/requests"
)

// A second grant of the rig's enrollment, and one grant of another enrollment, each holding one
// stored secret of its own.
const (
	secondName        = "SECOND_TOKEN"
	secondValue       = "the-real-second-token"
	secondGrantID     = "grant-00000002"
	foreignName       = "FOREIGN_TOKEN"
	foreignValue      = "the-real-foreign-token"
	foreignGrantID    = "grant-00000003"
	foreignEnrollment = "enrollment-00000003"
	// chatHost is the host the connection's own grant binds in the rows where it binds no
	// host the call goes to, as a Slack grant of a role binds slack.com and not GitHub.
	chatHost = "chat.example.net"
	chatName = "CHAT_TOKEN"
)

var (
	secondPlaceholder  = requests.PlaceholderPrefix + "secondsecondsecondsecondsecond02"
	foreignPlaceholder = requests.PlaceholderPrefix + "foreignforeignforeignforeignfor3"
	chatPlaceholder    = requests.PlaceholderPrefix + "chatchatchatchatchatchatchatcha4"
)

// addGrant is one more live grant the rig's broker holds: placeholder opens it, it holds name's
// value slot alone, and its values are value.
func (r *rig) addGrant(placeholder, grantID, enrollmentID, name, value string) {
	r.grants.setConnection(placeholder, requests.AirlockConnection{
		GrantID: grantID, EnrollmentID: enrollmentID, RequestID: "request-" + grantID, Issue: testIssue,
		SessionID: testSession, RuntimeID: testRuntime,
		Names: map[string]requests.AirlockGrantName{name: {Slots: map[string]string{requests.SlotValue: placeholder}}},
	})
	r.grants.setValues(grantID, storedValues(name, value))
}

// enrollmentRules binds the rig's own name, the second grant's and the foreign grant's to the
// bound host, each in a header of its own so one request can carry two of them, and chatName to
// chatHost alone.
func enrollmentRules() proxyrules.Rules {
	return proxyrules.Rules{Secrets: []proxyrules.SecretRule{
		{Name: workerName, Hosts: []string{boundHost}, Headers: []string{"authorization"}},
		{Name: secondName, Hosts: []string{boundHost}, Headers: []string{"x-second"}},
		{Name: foreignName, Hosts: []string{boundHost, tunnelHost}, Headers: []string{"x-foreign"}},
		{Name: chatName, Hosts: []string{chatHost}, Headers: []string{"authorization"}},
	}}
}

// TestARequestIsHandledForTheLiveGrantOfTheEnrollmentWhosePlaceholderItCarries: a connection is
// opened with the placeholder of one grant, and the command holds another grant of the same
// session beside it - mailbox's write placeholder on the connection of its read grant. A request
// that carries the second grant's placeholder and none of the first's is handled for the second:
// its value is swapped in, and the line names its grant. A request carrying a placeholder of both
// is handled for the connection's own grant alone, one (name, slot) per request as before. The
// controls: a placeholder of a grant of another enrollment, and of a second grant that has ended,
// each reach the service as the command wrote them.
func TestARequestIsHandledForTheLiveGrantOfTheEnrollmentWhosePlaceholderItCarries(t *testing.T) {
	r := newRig(t, Options{Rules: enrollmentRules()})
	r.addGrant(secondPlaceholder, secondGrantID, testEnrollID, secondName, secondValue)
	r.addGrant(foreignPlaceholder, foreignGrantID, foreignEnrollment, foreignName, foreignValue)
	client := r.client(workerPlaceholder)

	r.get(client, "https://"+boundHost+"/v1/second", "X-Second", "Token "+secondPlaceholder)
	seen := r.requests()
	if got, want := seen[0].Header.Get("X-Second"), "Token "+secondValue; got != want {
		t.Fatalf("the service saw X-Second %q; want %q: the request carried the second grant's placeholder on the first grant's connection", got, want)
	}
	line := r.logs.callsAtLeast(t, 1)[0]
	if line.fields["grant"] != secondGrantID || line.fields["secret"] != secondName || line.fields["enrollment"] != testEnrollID {
		t.Fatalf("the call logged %v; want grant %s, secret %s, enrollment %s", line.fields, secondGrantID, secondName, testEnrollID)
	}

	// The connection's own grant on the same connection, and both at once: the own grant's.
	r.get(client, "https://"+boundHost+"/v1/both", "Authorization", "Bearer "+workerPlaceholder, "X-Second", "Token "+secondPlaceholder)
	seen = r.requests()
	if got := seen[1].Header.Get("Authorization"); got != "Bearer "+workerValue {
		t.Errorf("the service saw Authorization %q; want the connection's own grant's value", got)
	}
	if got := seen[1].Header.Get("X-Second"); got != "Token "+secondPlaceholder {
		t.Errorf("the service saw X-Second %q; want the placeholder as written: one request is handled for one name", got)
	}
	if grant := r.logs.callsAtLeast(t, 2)[1].fields["grant"]; grant != testGrantID {
		t.Errorf("the call carrying both logged grant %v; want the connection's own %s", grant, testGrantID)
	}

	// Another enrollment's live grant: its placeholder is no placeholder of this session.
	r.get(client, "https://"+boundHost+"/v1/foreign", "X-Foreign", "Token "+foreignPlaceholder)
	seen = r.requests()
	if got := seen[2].Header.Get("X-Foreign"); got != "Token "+foreignPlaceholder {
		t.Fatalf("the service saw X-Foreign %q; want the placeholder of another enrollment's grant as written", got)
	}
	if grant := r.logs.callsAtLeast(t, 3)[2].fields["grant"]; grant != testGrantID {
		t.Errorf("the call carrying another enrollment's placeholder logged grant %v; want the connection's own %s", grant, testGrantID)
	}

	// The second grant once it has ended.
	r.grants.refuseConnection(secondPlaceholder, requests.ErrGrantNotLive)
	r.get(client, "https://"+boundHost+"/v1/ended", "X-Second", "Token "+secondPlaceholder)
	seen = r.requests()
	if got := seen[3].Header.Get("X-Second"); got != "Token "+secondPlaceholder {
		t.Fatalf("the service saw X-Second %q; want the ended grant's placeholder as written", got)
	}
}

// TestAConnectToAHostOnlyAnotherGrantOfTheEnrollmentBindsIsRead: a role's own grant binds GitHub,
// and inside agent-secrets for another secret the command's proxy password is that secret's
// placeholder, whose grant binds no GitHub host. The host is still read, since a live grant of the
// session binds it, and the role's placeholder is swapped. The service's own certificate is in the
// command's trust here, so a tunnel would reach it too and show the placeholder unswapped. The
// controls: a host only another enrollment's grant binds, and a host no grant binds, are tunnels.
func TestAConnectToAHostOnlyAnotherGrantOfTheEnrollmentBindsIsRead(t *testing.T) {
	r := newRig(t, Options{Rules: enrollmentRules()})
	r.grants.connections = map[string]requests.AirlockConnection{}
	r.addGrant(chatPlaceholder, testGrantID, testEnrollID, chatName, "the-real-chat-token")
	r.addGrant(secondPlaceholder, secondGrantID, testEnrollID, secondName, secondValue)
	r.addGrant(foreignPlaceholder, foreignGrantID, foreignEnrollment, foreignName, foreignValue)
	r.trust.AddCert(r.service.Authority().Certificate())

	answer, body := r.get(r.client(chatPlaceholder), "https://"+boundHost+"/v1/repos", "X-Second", "Token "+secondPlaceholder)
	if answer.StatusCode != http.StatusOK {
		t.Fatalf("the call answered %d %q; want 200", answer.StatusCode, body)
	}
	seen := r.requests()
	if len(seen) != 1 {
		t.Fatalf("the service saw %d requests; want one", len(seen))
	}
	if got, want := seen[0].Header.Get("X-Second"), "Token "+secondValue; got != want {
		t.Fatalf("the service saw X-Second %q; want %q: a live grant of the session binds the host, so the CONNECT is read and the placeholder swapped", got, want)
	}
	if tunnels := r.logs.tunnels(); len(tunnels) != 0 {
		t.Fatalf("the proxy logged %d tunnel(s); want none to a host a grant of the session binds", len(tunnels))
	}
	if grant := r.logs.callsAtLeast(t, 1)[0].fields["grant"]; grant != secondGrantID {
		t.Errorf("the call logged grant %v; want %s, whose placeholder it carried", grant, secondGrantID)
	}

	for _, host := range []string{tunnelHost, echoHost} {
		// tunnelHost is bound by the foreign grant alone, echoHost by no grant.
		answer, conn, _ := r.connectRaw(net.JoinHostPort(host, "443"), basic(chatPlaceholder))
		if answer.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT to %s answered %d; want 200 Connection established", host, answer.StatusCode)
		}
		if host == tunnelHost {
			client := tls.Client(conn, &tls.Config{ServerName: tunnelHost, RootCAs: r.trust})
			if err := client.Handshake(); err != nil {
				t.Fatalf("the handshake through the CONNECT to %s: %v", host, err)
			}
			if err := client.ConnectionState().PeerCertificates[0].CheckSignatureFrom(r.authority.Certificate()); err == nil {
				t.Fatalf("%s was answered a certificate of the proxy's own: a grant of another enrollment opened it", host)
			}
		}
		conn.Close()
	}
	if tunnels := r.logs.tunnelsAtLeast(t, 2); len(tunnels) != 2 {
		t.Fatalf("the proxy logged %d tunnels; want one for each host no grant of the session binds", len(tunnels))
	}
}

// TestPlainHTTPToAHostOnlyAnotherGrantOfTheEnrollmentBindsIsRefused: what is bound is what any
// live grant of the session binds, for plain HTTP as for a CONNECT, so a request in the clear to
// such a host is refused as one to a host the connection's own grant binds is.
func TestPlainHTTPToAHostOnlyAnotherGrantOfTheEnrollmentBindsIsRefused(t *testing.T) {
	r := newRig(t, Options{Rules: enrollmentRules()})
	r.grants.connections = map[string]requests.AirlockConnection{}
	r.addGrant(chatPlaceholder, testGrantID, testEnrollID, chatName, "the-real-chat-token")
	r.addGrant(secondPlaceholder, secondGrantID, testEnrollID, secondName, secondValue)
	answer, body := r.get(r.client(chatPlaceholder), "http://"+boundHost+"/v1/things", "X-Second", "Token "+secondPlaceholder)
	if answer.StatusCode != http.StatusForbidden || !strings.Contains(body, "plain http") {
		t.Fatalf("plain http to a host the session's second grant binds answered %d %q; want 403 refusing plain http", answer.StatusCode, body)
	}
	if r.dials.Load() != 0 {
		t.Fatalf("the proxy dialled %d time(s) for a refused plain-http request; want none", r.dials.Load())
	}
}

// TestARequestOfTheConnectionsOwnGrantReadsNoOtherGrant: a request carrying a placeholder of the
// connection's own grant is handled as before and costs no read of the enrollment's other grants;
// only a request that carries none of them reads those, and a read of them that did not happen is
// refused 503 in the broker's own words rather than forwarded.
func TestARequestOfTheConnectionsOwnGrantReadsNoOtherGrant(t *testing.T) {
	r := newRig(t, Options{Rules: enrollmentRules()})
	client := r.client(workerPlaceholder)
	r.get(client, "https://"+boundHost+"/v1/things", "Authorization", "Bearer "+workerPlaceholder)
	if reads := r.grants.enrollmentReads(); reads != 0 {
		t.Fatalf("a CONNECT and a request of the connection's own grant read the enrollment's grants %d time(s); want none", reads)
	}
	r.grants.failEnrollmentReads(databaseDown)
	answer, body := r.get(client, "https://"+boundHost+"/v1/things", "X-Second", "Token "+secondPlaceholder)
	if answer.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a request whose grant could not be read answered %d %q; want 503", answer.StatusCode, body)
	}
	for _, word := range []string{"user=broker", "10.20.0.7", "db.internal"} {
		if strings.Contains(body, word) {
			t.Fatalf("the answer %q carries the store's own words", body)
		}
	}
	if seen := r.requests(); len(seen) != 1 {
		t.Fatalf("the service saw %d requests; want the first alone", len(seen))
	}
}

// TestACallCarriedForAnotherGrantOfTheEnrollmentEndsWithThatGrant: a streamed call handled for
// the second grant on the first grant's connection is held to the second grant, whose value it
// carries: once that grant ends, the watcher cuts the call. The named revert:
// proxy-carry-reads-connection-grant-only, the watcher reading the connection's own grant alone,
// which leaves the stream running on the ended grant's value.
func TestACallCarriedForAnotherGrantOfTheEnrollmentEndsWithThatGrant(t *testing.T) {
	r := newRig(t, Options{Rules: enrollmentRules(), Watch: watchFast})
	r.addGrant(secondPlaceholder, secondGrantID, testEnrollID, secondName, secondValue)
	ended := streamStandIn(r)
	client := r.client(workerPlaceholder)
	client.Timeout = 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*cutWait)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+boundHost+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Second", "Token "+secondPlaceholder)
	answer, err := client.Do(request)
	if err != nil {
		t.Fatalf("the streamed call through the proxy: %v", err)
	}
	defer answer.Body.Close()
	reader := bufio.NewReader(answer.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "a line of the stream\n" {
		t.Fatalf("the stream's first line is %q (%v), want the service's", line, err)
	}
	if got := r.requests()[0].Header.Get("X-Second"); got != "Token "+secondValue {
		t.Fatalf("the service received X-Second %q, want the second grant's value", got)
	}
	// While the second grant lives the call goes on, for many watch intervals: the watcher reads
	// the grant the call is handled for, and the connection's own grant holds no such name.
	for deadline := time.Now().Add(10 * watchFast); time.Now().Before(deadline); {
		if line, err := reader.ReadString('\n'); err != nil || line != "a line of the stream\n" {
			t.Fatalf("the stream read %q (%v) while its grant lives; want it carried on", line, err)
		}
	}
	if cuts := r.logs.cuts(); len(cuts) != 0 {
		t.Fatalf("the proxy cut the call while its grant lives: %v", cuts[0].fields)
	}

	r.grants.refuseConnection(secondPlaceholder, requests.ErrGrantNotLive)
	cut := time.Now()
	_, err = io.Copy(io.Discard, reader)
	if took := time.Since(cut); err == nil || errors.Is(err, context.DeadlineExceeded) || took > cutWait {
		t.Fatalf("the stream went on %v after its grant ended and then ended with %v; want it cut within %v, the watch interval being %v", took, err, cutWait, watchFast)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the service's answer ended with %v, want it cancelled", err)
		}
	case <-time.After(cutWait):
		t.Fatal("the service went on streaming after the proxy cut the command's side")
	}
	cuts := r.logs.cuts()
	if len(cuts) != 1 {
		t.Fatalf("the proxy logged %d lines for the cut, want 1", len(cuts))
	}
	if got := cuts[0].fields["grant"]; got != secondGrantID {
		t.Errorf("the cut logged grant %v, want %s", got, secondGrantID)
	}
}
