// handlers_private_projects_test.go drives GET /v1/private-projects, the broker's own list of
// private projects, over real HTTP against a real Postgres: the shared session watch reads the
// union of the projects the deployment declares private and the ones Dispatch answers private,
// no other caller reads it, and a list the broker cannot read in full is a 503, never a shorter
// list.
package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/api"
	"github.com/sjawhar/envoy/internal/cipher/ciphertest"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/redact"
)

const (
	// watchAccount is the ServiceAccount BROKER_API_ACCOUNTS names as the caller session-watch,
	// the shared session watch, which holds readsPrivateList and nothing else.
	watchAccount = "system:serviceaccount:example-ns:example-session-watch"
	watchCaller  = "session-watch"
	// privateListPath is the route under test.
	privateListPath = "/v1/private-projects"
	// declaredKey is the project the broker's own settings declare private (Deps.PrivateDeclared)
	// and dispatchKey the one Dispatch answers private, so each half of the union is a key of its
	// own.
	declaredKey = "PRIV"
	dispatchKey = "OTHER"
)

// wirePrivateList is GET /v1/private-projects' answer as the wire carries it.
type wirePrivateList struct {
	PrivateProjects []string `json:"private_projects"`
}

// privateListServer is the test server with the session watch listed as its own caller, reading
// Dispatch's part of the list from dispatch and the declared part from declared, its held answer
// aged by now (nil is the wall clock). It answers the server and the watch's own token.
func privateListServer(t *testing.T, dispatch api.CachedPrivateProjects, declared []string, now func() time.Time) (*testServer, string) {
	t.Helper()
	ts := newTestServerWith(t, func(d *api.Deps) {
		d.APIAccounts[watchAccount] = ciphertest.Caller(t, watchCaller)
		d.PrivateProjects = dispatch
		d.PrivateDeclared = func() []string { return slices.Clone(declared) }
		api.SetPrivateListClock(d, now)
	})
	return ts, ts.accountToken(t, watchAccount, "", time.Hour)
}

// privateList reads the list as the caller whose ServiceAccount token is bearer.
func (ts *testServer) privateList(t *testing.T, bearer string) (int, []byte) {
	t.Helper()
	return ts.req(t, http.MethodGet, privateListPath, map[string]string{"Authorization": "Bearer " + bearer}, nil)
}

// dispatchClient is the broker's real client of a raw stub at url, one that scripts its answer
// and checks no bearer, so the bearer this client presents is any.
func dispatchClient(url string) *projectaccess.Client {
	return projectaccess.New(url, projectaccesstest.StaticAccount("example-dispatch-access"), nil)
}

// wantList fails t unless the answer is 200 carrying exactly want, in that order.
func wantList(t *testing.T, what string, status int, body []byte, want []string) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("%s = %d %s, want 200 %v", what, status, body, want)
	}
	if got := decode[wirePrivateList](t, body).PrivateProjects; got == nil || !slices.Equal(got, want) {
		t.Fatalf("%s answers %v, want %v (body %s)", what, got, want, body)
	}
}

// wantUnknown fails t unless the answer is 503 PRIVATE_LIST_UNKNOWN whose message carries cause,
// the failure it names.
func wantUnknown(t *testing.T, what string, status int, body []byte, cause string) {
	t.Helper()
	answer := decode[wireError](t, body)
	if status != http.StatusServiceUnavailable || answer.Code != "PRIVATE_LIST_UNKNOWN" {
		t.Fatalf("%s = %d %s, want 503 PRIVATE_LIST_UNKNOWN: a list the broker cannot read in full is no list", what, status, body)
	}
	if !strings.Contains(answer.Error, cause) {
		t.Fatalf("%s answers %q, which does not name the failure %q", what, answer.Error, cause)
	}
}

// TestThePrivateListIsTheBrokersOwnUnion: with PRIV declared private and Dispatch answering OTHER
// private (and PUB public), the session watch reads ["OTHER", "PRIV"]: the union of both, sorted,
// each key once, a key in both halves included, and no public project.
func TestThePrivateListIsTheBrokersOwnUnion(t *testing.T) {
	dispatch := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{
		dispatchKey: {Visibility: "private", Owner: testApprover},
		"PUB":       {Visibility: "public", Owner: testApprover},
	}, nil)
	client := projectaccess.New(dispatch.URL, dispatch.Account, nil)
	for _, c := range []struct {
		name     string
		declared []string
	}{
		{"one key declared", []string{declaredKey}},
		{"a declared key Dispatch answers too", []string{declaredKey, dispatchKey, declaredKey}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts, watch := privateListServer(t, client, c.declared, nil)
			status, body := ts.privateList(t, watch)
			wantList(t, "GET "+privateListPath+" as session-watch", status, body, []string{dispatchKey, declaredKey})
		})
	}
}

// TestOnlyTheSessionWatchReadsThePrivateList: dispatch, listener and access-review, each listed
// for its own powers, are refused 403 ACCOUNT_LACKS_POWER naming the caller and the route, and an
// account the broker does not list is refused 403 ACCOUNT_NOT_LISTED; session-watch alone reads
// the list.
func TestOnlyTheSessionWatchReadsThePrivateList(t *testing.T) {
	dispatch := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{
		dispatchKey: {Visibility: "private", Owner: testApprover},
	}, nil)
	ts := newTestServerWith(t, func(d *api.Deps) {
		d.APIAccounts[watchAccount] = ciphertest.Caller(t, watchCaller)
		d.APIAccounts[listenerAccount] = ciphertest.Caller(t, listenerCaller)
		d.APIAccounts[reviewAccount] = ciphertest.Caller(t, reviewCaller)
		d.PrivateProjects = projectaccess.New(dispatch.URL, dispatch.Account, nil)
	})
	route := http.MethodGet + " " + privateListPath
	for _, c := range []struct {
		caller, account, code string
	}{
		{dispatchCaller, dispatchAccount, "ACCOUNT_LACKS_POWER"},
		{listenerCaller, listenerAccount, "ACCOUNT_LACKS_POWER"},
		{reviewCaller, reviewAccount, "ACCOUNT_LACKS_POWER"},
		{"an unlisted account", unlistedAccount, "ACCOUNT_NOT_LISTED"},
	} {
		t.Run(c.caller, func(t *testing.T) {
			status, body := ts.privateList(t, ts.accountToken(t, c.account, "", time.Hour))
			answer := decode[wireError](t, body)
			if status != http.StatusForbidden || answer.Code != c.code {
				t.Fatalf("%s as %s = %d %s, want 403 %s: only the session watch reads the private list", route, c.caller, status, body, c.code)
			}
			if c.code == "ACCOUNT_LACKS_POWER" && (!strings.Contains(answer.Error, c.caller) || !strings.Contains(answer.Error, route)) {
				t.Errorf("the refusal reads %q, which does not name the caller %q and the route %q", answer.Error, c.caller, route)
			}
		})
	}
	status, body := ts.privateList(t, ts.accountToken(t, watchAccount, "", time.Hour))
	wantList(t, route+" as session-watch", status, body, []string{dispatchKey})
}

// scriptedDispatch is Dispatch's part of the list as a test scripts it: the projects it answers
// private, or the failure it answers instead while down.
type scriptedDispatch struct {
	mu       sync.Mutex
	projects []projectaccess.PrivateProject
	down     error
}

func (d *scriptedDispatch) PrivateProjects(context.Context) ([]projectaccess.PrivateProject, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down != nil {
		return nil, d.down
	}
	return slices.Clone(d.projects), nil
}

func (d *scriptedDispatch) setProjects(projects []projectaccess.PrivateProject) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.projects = slices.Clone(projects)
}

func (d *scriptedDispatch) setDown(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = err
}

// testClock is a clock a test moves by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestAnUnknownListIsA503NeverAShortList: while Dispatch cannot answer and the broker holds no
// answer of it, the list is 503 PRIVATE_LIST_UNKNOWN naming the failure, never the declared keys
// alone. Once Dispatch has answered, a failure 4 minutes after that read is answered with the
// union of the held answer, and one 6 minutes after it is 503 again, naming the failure. The 5
// minutes count from the earliest moment Dispatch can have given the list, CacheTTL before the
// read, since the client may have answered it from its cache: 4m40s after the read is already
// past them.
func TestAnUnknownListIsA503NeverAShortList(t *testing.T) {
	t.Run("no answer ever read", func(t *testing.T) {
		down := downDispatch(t)
		ts, watch := privateListServer(t, projectaccess.New(down.URL, down.Account, nil), []string{declaredKey}, nil)
		status, body := ts.privateList(t, watch)
		wantUnknown(t, "the list while Dispatch is down and never answered", status, body, redact.ClassRequestFailed)
	})

	t.Run("no Dispatch configured", func(t *testing.T) {
		ts, watch := privateListServer(t, nil, []string{declaredKey}, nil)
		status, body := ts.privateList(t, watch)
		wantUnknown(t, "the list with only declared projects and no Dispatch", status, body, "BROKER_PROJECT_ACCESS_URL")
	})

	t.Run("the last answer held for 5 minutes", func(t *testing.T) {
		clock := &testClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
		dispatch := &scriptedDispatch{projects: []projectaccess.PrivateProject{{Key: dispatchKey, SlackChannelsPresent: true}}}
		ts, watch := privateListServer(t, dispatch, []string{declaredKey}, clock.Now)
		status, body := ts.privateList(t, watch)
		wantList(t, "the list while Dispatch answers", status, body, []string{dispatchKey, declaredKey})

		dispatch.setDown(errors.New("dispatch is down in this test"))
		clock.advance(4 * time.Minute)
		status, body = ts.privateList(t, watch)
		wantList(t, "the list 4 minutes after Dispatch last answered", status, body, []string{dispatchKey, declaredKey})

		clock.advance(30*time.Second - time.Nanosecond)
		status, body = ts.privateList(t, watch)
		wantList(t, "the list just before its 5-minute hold ends", status, body, []string{dispatchKey, declaredKey})

		clock.advance(time.Nanosecond)
		status, body = ts.privateList(t, watch)
		wantUnknown(t, "the list exactly at its 5-minute hold", status, body, redact.ClassRequestFailed)

		clock.advance(10 * time.Second)
		status, body = ts.privateList(t, watch)
		wantUnknown(t, "the list 4m40s after the read, which may be 5m10s after Dispatch gave it", status, body, redact.ClassRequestFailed)

		clock.advance(80 * time.Second)
		status, body = ts.privateList(t, watch)
		wantUnknown(t, "the list 6 minutes after Dispatch last answered", status, body, redact.ClassRequestFailed)
	})

	t.Run("a newer answer replaces the held one, and Dispatch's return ends the outage", func(t *testing.T) {
		clock := &testClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
		dispatch := &scriptedDispatch{projects: []projectaccess.PrivateProject{{Key: dispatchKey, SlackChannelsPresent: true}}}
		ts, watch := privateListServer(t, dispatch, []string{declaredKey}, clock.Now)
		status, body := ts.privateList(t, watch)
		wantList(t, "the first list", status, body, []string{dispatchKey, declaredKey})

		// Dispatch answers a second list, naming a project made private since the first.
		clock.advance(time.Minute)
		dispatch.setProjects([]projectaccess.PrivateProject{{Key: "NEW", SlackChannelsPresent: true}, {Key: dispatchKey, SlackChannelsPresent: true}})
		status, body = ts.privateList(t, watch)
		wantList(t, "the second list", status, body, []string{"NEW", dispatchKey, declaredKey})

		// Down 4m20s after the second read: the held list is the second one, so NEW stays in it.
		dispatch.setDown(errors.New("dispatch is down in this test"))
		clock.advance(4*time.Minute + 20*time.Second)
		status, body = ts.privateList(t, watch)
		wantList(t, "the list held from the second read", status, body, []string{"NEW", dispatchKey, declaredKey})
		clock.advance(20 * time.Second)
		status, body = ts.privateList(t, watch)
		wantUnknown(t, "the list past the second read's hold", status, body, redact.ClassRequestFailed)

		// Dispatch answers again: the list is its new answer, held again from this read.
		dispatch.setDown(nil)
		dispatch.setProjects([]projectaccess.PrivateProject{{Key: dispatchKey, SlackChannelsPresent: true}})
		status, body = ts.privateList(t, watch)
		wantList(t, "the list once Dispatch answers again", status, body, []string{dispatchKey, declaredKey})
		dispatch.setDown(errors.New("dispatch is down in this test"))
		clock.advance(4 * time.Minute)
		status, body = ts.privateList(t, watch)
		wantList(t, "the list held from the read after the outage", status, body, []string{dispatchKey, declaredKey})
	})
}

// TestAMalformedK3AnswerIsK3Down: a Dispatch answering the private-projects route 404, whatever
// its code, or 200 with no projects key, a null projects or a projects that is no list, is a
// Dispatch that cannot answer, never one answering no private project: each, on a broker holding
// no earlier answer, is 503 PRIVATE_LIST_UNKNOWN, never the declared ["PRIV"] alone.
func TestAMalformedK3AnswerIsK3Down(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"a 404 naming no project", http.StatusNotFound, `{"code":"NOT_FOUND","error":"no such route"}`},
		{"a 404 naming the project code", http.StatusNotFound, `{"code":"PROJECT_NOT_FOUND","error":"no such project"}`},
		{"{}", http.StatusOK, `{}`},
		{`{"projects": null}`, http.StatusOK, `{"projects": null}`},
		{`{"projects": {}}`, http.StatusOK, `{"projects": {}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(stub.Close)
			ts, watch := privateListServer(t, dispatchClient(stub.URL), []string{declaredKey}, nil)
			status, body := ts.privateList(t, watch)
			wantUnknown(t, "the list while Dispatch answers "+c.name, status, body, redact.ClassRequestFailed)
		})
	}
}

// TestThePrivateListNeverRepeatsTheBrokersToken refuses a dependency's answer without echoing
// its text: an upstream refusal code or a malformed projects value may repeat the bearer it saw.
func TestThePrivateListNeverRepeatsTheBrokersToken(t *testing.T) {
	const token = "example-private-list-broker-token"
	logged := policytest.CaptureLog(t)
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"a refusal code", http.StatusServiceUnavailable, fmt.Sprintf(`{"code":%q}`, token)},
		{"a refused token", http.StatusForbidden, fmt.Sprintf(`{"code":%q}`, token)},
		{"a malformed list", http.StatusOK, fmt.Sprintf(`{"projects":%q}`, token)},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(stub.Close)
			client := projectaccess.New(stub.URL, projectaccesstest.StaticAccount(token), nil)
			ts, watch := privateListServer(t, client, []string{declaredKey}, nil)
			status, body := ts.privateList(t, watch)
			if status != http.StatusServiceUnavailable || decode[wireError](t, body).Code != "PRIVATE_LIST_UNKNOWN" {
				t.Fatalf("the list with %s = %d %s, want 503 PRIVATE_LIST_UNKNOWN", c.name, status, body)
			}
			if strings.Contains(string(body), token) {
				t.Fatal("the private-list refusal repeats the broker's token")
			}
		})
	}
	t.Run("the held-answer warning", func(t *testing.T) {
		dispatch := &scriptedDispatch{projects: []projectaccess.PrivateProject{{Key: dispatchKey, SlackChannelsPresent: true}}}
		ts, watch := privateListServer(t, dispatch, []string{declaredKey}, nil)
		status, body := ts.privateList(t, watch)
		wantList(t, "the list before a dependency fails", status, body, []string{dispatchKey, declaredKey})
		dispatch.setDown(fmt.Errorf("a failure that echoes the broker's token: %s", token))
		status, body = ts.privateList(t, watch)
		wantList(t, "the list held after a dependency fails", status, body, []string{dispatchKey, declaredKey})
	})
	text := logged.String()
	if !strings.Contains(text, "the list of private projects is unknown") || !strings.Contains(text, "answering the last list it gave") {
		t.Fatal("the log capture missed the private-list error or held-answer warning")
	}
	if strings.Contains(text, token) {
		t.Fatal("a private-list error or warning log repeats the broker's token")
	}
}

// TestThePrivateListAnswersAnEmptyArrayWhenNoProjectIsPrivate: with no project private anywhere,
// the answer carries private_projects as an empty array, never JSON null and never no key, which
// a reader could take for a list it could not read.
func TestThePrivateListAnswersAnEmptyArrayWhenNoProjectIsPrivate(t *testing.T) {
	dispatch := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{}, nil)
	ts, watch := privateListServer(t, projectaccess.New(dispatch.URL, dispatch.Account, nil), nil, nil)
	status, body := ts.privateList(t, watch)
	if status != http.StatusOK || !strings.Contains(string(body), `"private_projects":[]`) {
		t.Fatalf("the list with no private project = %d %s, want 200 carrying \"private_projects\":[]", status, body)
	}
}
