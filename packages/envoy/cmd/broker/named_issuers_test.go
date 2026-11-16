// named_issuers_test.go drives the REAL compiled broker trusting two named Kubernetes issuers
// (BROKER_K8S_OIDC_ISSUER=a=<URL>,b=<URL>), each a local OpenID issuer standing in for one cluster,
// on a real Postgres and over HTTP alone: each token is checked by the issuer it names, an issuer
// the broker cannot read stops neither the boot nor the other issuer and its tokens are answered
// 503 until it can be read, the token exchange takes the issuers BROKER_EXCHANGE_K8S_ISSUER names,
// and a listed account is taken only from the issuers its entry names. Every request and
// answer is logged, with no token, as the live run's transcript.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
	"github.com/sjawhar/envoy/internal/testwait"
)

const (
	// namedService is the service the two clusters' daemons log in as; namedDaemonA and
	// namedDaemonB are their accounts, one in each cluster; namedWorker is the account their pods
	// run as, which BROKER_SERVICES binds to that service; namedCaller is the listed caller of the
	// routes Dispatch's server calls; namedSession is the session name a pod writes under.
	namedService = "legion-daemon"
	namedDaemonA = "system:serviceaccount:legion-system:legion-daemon-a"
	namedDaemonB = "system:serviceaccount:legion-system:legion-daemon-b"
	namedWorker  = "system:serviceaccount:legion:legion-worker"
	namedCaller  = "system:serviceaccount:example-ns:example-dispatch"
	namedSession = "example-session"
	// issuerUnavailable is the code of the answer to a token whose issuer the broker cannot read.
	issuerUnavailable = "OIDC_ISSUER_UNAVAILABLE"
)

// namedIssuersEnv is the environment of a broker on databaseURL that trusts issuers, the value of
// BROKER_K8S_OIDC_ISSUER, for enrolmentAudience, binds namedWorker's pods to namedService, and
// stops at once when it is asked to; extra adds the settings a row is about.
func namedIssuersEnv(t *testing.T, databaseURL, issuers string, extra ...string) []string {
	t.Helper()
	fakeSecretsFile := filepath.Join(t.TempDir(), "fake-secrets.json")
	if err := os.WriteFile(fakeSecretsFile, []byte(`{"secrets": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return append([]string{
		"BROKER_DATABASE_URL=" + databaseURL,
		"BROKER_LISTEN_ADDR=127.0.0.1:0",
		"BROKER_PUBLIC_URL=http://127.0.0.1:0",
		"BROKER_FAKE_SECRETS_FILE=" + fakeSecretsFile,
		"BROKER_K8S_OIDC_ISSUER=" + issuers,
		"BROKER_K8S_OIDC_AUDIENCE=" + enrolmentAudience,
		"BROKER_SERVICES=" + namedService + "=" + namedWorker,
		"BROKER_DRAIN_SECONDS=0",
	}, extra...)
}

// bothIssuers is the setting naming a and b, in that order, the broker's two clusters.
func bothIssuers(a, b *oidctest.Issuer) string {
	return "a=" + a.URL() + ",b=" + b.URL()
}

// clusterToken is account's projected token whose iss is cluster's, signed with key, for
// enrolmentAudience, issued a few seconds ago and living an hour, and bound to the pod podUID (""
// for a token bound to no pod, which a daemon's login and a listed caller present). A key that is
// not cluster's makes a token naming one issuer and carrying another's signature.
func clusterToken(t *testing.T, cluster *oidctest.Issuer, key *oidctest.Key, account, podUID string) string {
	t.Helper()
	claims := cluster.Claims(account, enrolmentAudience)
	claims["iat"] = time.Now().Add(-5 * time.Second).Unix()
	if podUID != "" {
		claims["kubernetes.io"] = map[string]any{"pod": map[string]any{"uid": podUID}}
	}
	return cluster.Mint(t, key, claims)
}

// namedClient drives one running broker as the clusters' daemons, pods and listed callers do.
// Every call answers the broker's status and the code of its refusal, "" for an answer that is
// none, and logs the route and the answer, never a token.
type namedClient struct {
	t    *testing.T
	base string
}

// send sends body as JSON to path with headers and answers the status and the body.
func (c namedClient) send(method, path string, headers map[string]string, body any) (int, []byte) {
	c.t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, payload)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Logf("> %s %s\n< %d %s", method, path, response.StatusCode, bytes.TrimSpace(answer))
	return response.StatusCode, answer
}

// codeOf is the code of a refusal's JSON body, "" for a body that names none.
func codeOf(answer []byte) string {
	var refusal struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(answer, &refusal)
	return refusal.Code
}

// tokenLogin logs namedService in with token, its key key, and answers the credential it issued.
func (c namedClient) tokenLogin(token string, key *ecdsa.PrivateKey) (int, string, launcher) {
	c.t.Helper()
	request, err := record.Sign(key, c.base, []record.AuthorizationDetail{
		{Type: record.KindLauncherCredential, Identifier: "example-host-" + namedService, Service: namedService},
	}, "", "", nil, time.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	status, answer := c.send(http.MethodPost, "/v1/launcher-credentials/token", nil, map[string]any{"token": token, "request": request})
	var login struct {
		CredentialID string `json:"credential_id"`
	}
	_ = json.Unmarshal(answer, &login)
	return status, codeOf(answer), launcher{key: key, credentialID: login.CredentialID}
}

// enroll has by's credential enroll the pod podUID, which proves itself with token, under a key of
// its own, and answers the enrollment.
func (c namedClient) enroll(by launcher, podUID, token string) (int, string, pod) {
	c.t.Helper()
	key := proofKey(c.t)
	thumbprint, err := proof.Thumbprint(&key.PublicKey)
	if err != nil {
		c.t.Fatal(err)
	}
	signed, err := proof.SignLauncher(by.key, by.credentialID, http.MethodPost, c.base+"/v1/enrollments", time.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	status, answer := c.send(http.MethodPost, "/v1/enrollments", map[string]string{"Proof": signed}, map[string]any{
		"kind": "pod", "runtime_id": podUID, "thumbprint": thumbprint, "pod_token": token,
	})
	var enrolled struct {
		EnrollmentID string `json:"enrollment_id"`
	}
	_ = json.Unmarshal(answer, &enrolled)
	return status, codeOf(answer), pod{runtimeID: podUID, key: key, enrollmentID: enrolled.EnrollmentID}
}

// renew renews p's enrollment with token.
func (c namedClient) renew(p pod, token string) (int, string) {
	c.t.Helper()
	path := "/v1/enrollments/" + p.enrollmentID + "/renew"
	signed, err := proof.Sign(p.key, p.enrollmentID, http.MethodPost, c.base+path, time.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	status, answer := c.send(http.MethodPost, path, map[string]string{"Proof": signed}, map[string]any{"pod_token": token})
	return status, codeOf(answer)
}

// register registers the pod podToken names for namedSession, as a registrar presenting its own
// login token, bearer, does.
func (c namedClient) register(bearer, podToken string) (int, string) {
	c.t.Helper()
	status, answer := c.send(http.MethodPost, "/v1/oauth/exchange/pod-sessions", map[string]string{"Authorization": "Bearer " + bearer},
		map[string]any{"pod_token": podToken, "session": namedSession})
	return status, codeOf(answer)
}

// pending is GET /v1/pending, a route of the listed caller, presenting bearer.
func (c namedClient) pending(bearer string) (int, string) {
	c.t.Helper()
	status, answer := c.send(http.MethodGet, "/v1/pending?approver="+url.QueryEscape("ada@example.com"), map[string]string{"Authorization": "Bearer " + bearer}, nil)
	return status, codeOf(answer)
}

// authenticates is a launcher route by's credential reaches only while it authenticates: ending
// an enrollment no one made, which answers 204 to a credential the broker takes and 401
// LAUNCHER_INVALID to one it does not.
func (c namedClient) authenticates(by launcher) (int, string) {
	c.t.Helper()
	path := "/v1/enrollments/" + uuid.NewString()
	signed, err := proof.SignLauncher(by.key, by.credentialID, http.MethodDelete, c.base+path, time.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	status, answer := c.send(http.MethodDelete, path, map[string]string{"Proof": signed}, nil)
	return status, codeOf(answer)
}

// namedRoute is one call a row makes and the answer it wants.
type namedRoute struct {
	name   string
	call   func() (int, string)
	status int
	code   string
}

// expect runs each route as a subtest of its own, failing it unless the broker gave the status and
// code it wants.
func expect(t *testing.T, prefix string, routes []namedRoute) {
	t.Helper()
	for _, route := range routes {
		t.Run(prefix+route.name, func(t *testing.T) {
			if status, code := route.call(); status != route.status || code != route.code {
				t.Fatalf("answered %d %q, want %d %q", status, code, route.status, route.code)
			}
		})
	}
}

// TestTheBrokerTrustsEachNamedIssuer: with two named issuers, A and B, both up, a token of A and a
// token of B each pass enrollment, renewal, the token login, the pod-session registration and a
// route of the listed caller. On each of those routes a token of a third issuer, C, gets the
// status and code a token of another issuer gets on a broker of one issuer, and so does a token
// that names A and carries a signature of B's key, under a key id A publishes.
func TestTheBrokerTrustsEachNamedIssuer(t *testing.T) {
	databaseURL := storetest.URL(t)
	a, b, c := oidctest.New(t), oidctest.New(t), oidctest.New(t)
	// One key id across the three, so the token that names A and carries B's signature names a key
	// A publishes, and only the key itself is wrong.
	aKey, bKey, cKey := a.PublishKey(t, "cluster-key"), b.PublishKey(t, "cluster-key"), c.PublishKey(t, "cluster-key")
	broker := startBroker(t, brokerOptions{Env: namedIssuersEnv(t, databaseURL, bothIssuers(a, b),
		"BROKER_SERVICE_LOGINS="+namedService+"="+namedDaemonA+"=a,b "+namedService+"="+namedDaemonB+"=a,b",
		"BROKER_API_ACCOUNTS=dispatch="+namedCaller+"=a,b",
		// The registration route writes for an account the exchange lists with no fixed session.
		"BROKER_EXCHANGE_K8S_AUDIENCE="+exchangeAudience,
		"BROKER_EXCHANGE_K8S_ISSUER=a",
		"BROKER_EXCHANGE_ACCOUNTS="+namedWorker+"=ACME=ada@example.com",
	)})
	client := namedClient{t: t, base: "http://" + broker.Addr}

	// A's login and pod, which the refusals below present their tokens to.
	var loginOfA launcher
	var podOfA pod
	for _, cluster := range []struct {
		name   string
		issuer *oidctest.Issuer
		key    *oidctest.Key
		daemon string
	}{
		{"A", a, aKey, namedDaemonA},
		{"B", b, bKey, namedDaemonB},
	} {
		t.Run("a token of "+cluster.name, func(t *testing.T) {
			podUID := "pod-of-" + strings.ToLower(cluster.name)
			var login launcher
			var enrolled pod
			t.Run("passes the token login", func(t *testing.T) {
				status, code, issued := client.tokenLogin(clusterToken(t, cluster.issuer, cluster.key, cluster.daemon, ""), proofKey(t))
				if status != http.StatusOK || issued.credentialID == "" {
					t.Fatalf("answered %d %q, want 200 with a credential", status, code)
				}
				login = issued
			})
			t.Run("passes enrollment", func(t *testing.T) {
				if login.credentialID == "" {
					t.Fatal("the token login above issued no credential to enroll with")
				}
				status, code, p := client.enroll(login, podUID, clusterToken(t, cluster.issuer, cluster.key, namedWorker, podUID))
				if status != http.StatusCreated || p.enrollmentID == "" {
					t.Fatalf("answered %d %q, want 201 with an enrollment", status, code)
				}
				enrolled = p
			})
			t.Run("passes renewal", func(t *testing.T) {
				if enrolled.enrollmentID == "" {
					t.Fatal("the enrollment above made no enrollment to renew")
				}
				if status, code := client.renew(enrolled, clusterToken(t, cluster.issuer, cluster.key, namedWorker, podUID)); status != http.StatusOK {
					t.Fatalf("answered %d %q, want 200", status, code)
				}
			})
			t.Run("passes pod-session registration", func(t *testing.T) {
				if status, code := client.register(clusterToken(t, cluster.issuer, cluster.key, cluster.daemon, ""), clusterToken(t, cluster.issuer, cluster.key, namedWorker, podUID)); status != http.StatusOK {
					t.Fatalf("answered %d %q, want 200", status, code)
				}
			})
			t.Run("passes an API account route", func(t *testing.T) {
				if status, code := client.pending(clusterToken(t, cluster.issuer, cluster.key, namedCaller, "")); status != http.StatusOK {
					t.Fatalf("answered %d %q, want 200", status, code)
				}
			})
			if cluster.name == "A" {
				loginOfA, podOfA = login, enrolled
			}
		})
	}

	for _, refused := range []struct {
		name  string
		token func(account, podUID string) string
	}{
		{"a token of a third issuer, C,", func(account, podUID string) string { return clusterToken(t, c, cKey, account, podUID) }},
		{"a token naming A and signed with B's key", func(account, podUID string) string { return clusterToken(t, a, bKey, account, podUID) }},
	} {
		t.Run(refused.name, func(t *testing.T) {
			if loginOfA.credentialID == "" || podOfA.enrollmentID == "" {
				t.Fatal("A's login or A's pod is missing, so there is nothing to present the refused tokens to")
			}
			expect(t, "is refused ", []namedRoute{
				{"at enrollment", func() (int, string) {
					status, code, _ := client.enroll(loginOfA, "pod-refused", refused.token(namedWorker, "pod-refused"))
					return status, code
				}, http.StatusForbidden, "POD_IDENTITY_MISMATCH"},
				{"at renewal", func() (int, string) {
					return client.renew(podOfA, refused.token(namedWorker, podOfA.runtimeID))
				}, http.StatusForbidden, "POD_IDENTITY_MISMATCH"},
				{"at the token login", func() (int, string) {
					status, code, _ := client.tokenLogin(refused.token(namedDaemonA, ""), proofKey(t))
					return status, code
				}, http.StatusUnauthorized, "TOKEN_INVALID"},
				{"at pod-session registration", func() (int, string) {
					return client.register(clusterToken(t, a, aKey, namedDaemonA, ""), refused.token(namedWorker, "pod-refused"))
				}, http.StatusUnauthorized, "POD_TOKEN_INVALID"},
				{"as the registrar's own token", func() (int, string) {
					return client.register(refused.token(namedDaemonA, ""), clusterToken(t, a, aKey, namedWorker, "pod-refused"))
				}, http.StatusUnauthorized, "ACCOUNT_TOKEN_INVALID"},
				{"on an API account route", func() (int, string) {
					return client.pending(refused.token(namedCaller, ""))
				}, http.StatusUnauthorized, "ACCOUNT_TOKEN_INVALID"},
			})
		})
	}
}

// TestTheBrokerBootsWithAnIssuerItCannotRead: B's discovery document fails at boot. The broker
// starts, logs one line for A, read, and one for B, not read, each with its name and its URL,
// answers /healthz 200, and serves A's token login. A token of B gets 503 OIDC_ISSUER_UNAVAILABLE
// on enrollment, renewal, the token login, the pod-session registration, a route of the listed
// caller and a service-login route, and twelve token logins of B from one address, past the
// address's burst of ten, each get 503 and none 429. Once B answers, B's token login answers 200
// within 60 seconds, and once the background read has read B too, one line names B read again and
// none says B cannot be read, the boot's line having said so. The control boots with B up and sees
// B's first token pass, so the 503 is the unread issuer's.
func TestTheBrokerBootsWithAnIssuerItCannotRead(t *testing.T) {
	binary := buildBroker(t)
	a, b := oidctest.New(t), oidctest.New(t)
	aKey, bKey := a.PublishKey(t, "a-key"), b.PublishKey(t, "b-key")
	settings := []string{
		"BROKER_SERVICE_LOGINS=" + namedService + "=" + namedDaemonA + "=a,b " + namedService + "=" + namedDaemonB + "=a,b",
		"BROKER_API_ACCOUNTS=dispatch=" + namedCaller + "=a,b",
	}

	t.Run("the control boots with B up and its first token of B passes the token login", func(t *testing.T) {
		control := startBroker(t, brokerOptions{Binary: binary, Env: namedIssuersEnv(t, storetest.URL(t), bothIssuers(a, b), settings...)})
		client := namedClient{t: t, base: "http://" + control.Addr}
		if status, code, _ := client.tokenLogin(clusterToken(t, b, bKey, namedDaemonB, ""), proofKey(t)); status != http.StatusOK {
			t.Fatalf("answered %d %q, want 200", status, code)
		}
	})

	b.FailDiscovery(http.StatusServiceUnavailable)
	broker := startBroker(t, brokerOptions{Binary: binary, Env: namedIssuersEnv(t, storetest.URL(t), bothIssuers(a, b), settings...)})
	client := namedClient{t: t, base: "http://" + broker.Addr}

	t.Run("the broker starts and logs a line for each issuer with its name, its URL and whether it read it", func(t *testing.T) {
		for _, want := range []map[string]any{
			{"severity": "INFO", "name": "a", "url": a.URL(), "read": true},
			{"severity": "INFO", "name": "b", "url": b.URL(), "read": false},
		} {
			if got := len(recordsWith(broker.Boot, issuerBootMessage, want)); got != 1 {
				t.Errorf("the boot log holds %d lines %q carrying %v, want one:\n%s", got, issuerBootMessage, want, broker.Boot)
			}
		}
	})
	t.Run("/healthz answers 200", func(t *testing.T) { requireHealthy(t, broker.Addr) })

	var loginOfA launcher
	var podOfA pod
	t.Run("a token of A passes the token login", func(t *testing.T) {
		status, code, issued := client.tokenLogin(clusterToken(t, a, aKey, namedDaemonA, ""), proofKey(t))
		if status != http.StatusOK || issued.credentialID == "" {
			t.Fatalf("answered %d %q, want 200 with a credential", status, code)
		}
		loginOfA = issued
		status, code, podOfA = client.enroll(loginOfA, "pod-of-a", clusterToken(t, a, aKey, namedWorker, "pod-of-a"))
		if status != http.StatusCreated {
			t.Fatalf("the enrollment of A's pod, which the renewal below renews, answered %d %q, want 201", status, code)
		}
	})
	if loginOfA.credentialID == "" || podOfA.enrollmentID == "" {
		t.Fatal("A's login or A's pod is missing, so there is nothing to present B's tokens to")
	}

	expect(t, "a token of B gets 503 OIDC_ISSUER_UNAVAILABLE ", []namedRoute{
		{"on enrollment", func() (int, string) {
			status, code, _ := client.enroll(loginOfA, "pod-of-b", clusterToken(t, b, bKey, namedWorker, "pod-of-b"))
			return status, code
		}, http.StatusServiceUnavailable, issuerUnavailable},
		{"on renewal", func() (int, string) {
			return client.renew(podOfA, clusterToken(t, b, bKey, namedWorker, podOfA.runtimeID))
		}, http.StatusServiceUnavailable, issuerUnavailable},
		{"on the token login", func() (int, string) {
			status, code, _ := client.tokenLogin(clusterToken(t, b, bKey, namedDaemonB, ""), proofKey(t))
			return status, code
		}, http.StatusServiceUnavailable, issuerUnavailable},
		{"on pod-session registration", func() (int, string) {
			return client.register(clusterToken(t, a, aKey, namedDaemonA, ""), clusterToken(t, b, bKey, namedWorker, "pod-of-b"))
		}, http.StatusServiceUnavailable, issuerUnavailable},
		{"on an API account route", func() (int, string) {
			return client.pending(clusterToken(t, b, bKey, namedCaller, ""))
		}, http.StatusServiceUnavailable, issuerUnavailable},
		{"on a service-login route", func() (int, string) {
			return client.register(clusterToken(t, b, bKey, namedDaemonB, ""), clusterToken(t, a, aKey, namedWorker, podOfA.runtimeID))
		}, http.StatusServiceUnavailable, issuerUnavailable},
	})

	t.Run("twelve token logins of B from one address each get 503 and none 429", func(t *testing.T) {
		for i := range 12 {
			if status, code, _ := client.tokenLogin(clusterToken(t, b, bKey, namedDaemonB, ""), proofKey(t)); status != http.StatusServiceUnavailable || code != issuerUnavailable {
				t.Fatalf("login %d answered %d %q, want 503 %s: a login answered 503 spends none of its address's burst of 10", i+1, status, code, issuerUnavailable)
			}
		}
	})

	b.FailDiscovery(0)
	answered := time.Now()
	t.Run("once B answers, its token login answers 200 within 60 seconds and one line names B read", func(t *testing.T) {
		var last string
		if !testwait.Holds(time.Minute, time.Second, func() bool {
			status, code, issued := client.tokenLogin(clusterToken(t, b, bKey, namedDaemonB, ""), proofKey(t))
			last = fmt.Sprintf("%d %q", status, code)
			return status == http.StatusOK && issued.credentialID != ""
		}) {
			t.Fatalf("B's token login was not answered 200 within a minute of B answering; it last answered %s", last)
		}
		t.Logf("B's token login answered 200 %s after B answered", time.Since(answered).Round(time.Millisecond))
		// The background read of B writes this line of package oidc's once it has read B, just
		// before it notes B read; the token login above has noted B read already.
		read := map[string]any{"issuer": b.URL()}
		if !testwait.Holds(time.Until(answered.Add(time.Minute)), 250*time.Millisecond, func() bool {
			return len(recordsWith(broker.Log(), "oidc: discovered the issuer", read)) != 0
		}) {
			t.Fatalf("no line %q carrying %v within a minute of B answering:\n%s", "oidc: discovered the issuer", read, broker.Log())
		}
		want := map[string]any{"severity": "INFO", "name": "b", "url": b.URL()}
		if !testwait.Holds(time.Until(answered.Add(time.Minute)), 250*time.Millisecond, func() bool {
			return len(recordsWith(broker.Log(), issuerReadableMessage, want)) != 0
		}) {
			t.Fatalf("no line %q carrying %v within a minute of B answering:\n%s", issuerReadableMessage, want, broker.Log())
		}
		if got := recordsWith(broker.Log(), issuerReadableMessage, map[string]any{"name": "b"}); len(got) != 1 {
			t.Fatalf("B's recovery wrote %d lines %q, want one:\n%s", len(got), issuerReadableMessage, broker.Log())
		}
		if got := recordsWith(broker.Log(), issuerUnreadableMessage, map[string]any{"name": "b"}); len(got) != 0 {
			t.Fatalf("the tokens of B answered 503 after the boot's line said B was not read wrote %d lines %q, want none:\n%s", len(got), issuerUnreadableMessage, broker.Log())
		}
	})
}

// TestAKeySetTheBrokerCannotReadIsAnswered503: the broker reads B's discovery document at boot, and
// B's key set fails before any token is checked. A token of B gets 503 OIDC_ISSUER_UNAVAILABLE at
// enrollment and at the token login, and fifty tokens of B sent within 5 seconds cost B's issuer
// at most two key-set requests. Once the key set answers, a token of B passes both routes within
// 15 seconds.
func TestAKeySetTheBrokerCannotReadIsAnswered503(t *testing.T) {
	a, b := oidctest.New(t), oidctest.New(t)
	aKey, bKey := a.PublishKey(t, "a-key"), b.PublishKey(t, "b-key")
	b.FailKeys(http.StatusServiceUnavailable)
	broker := startBroker(t, brokerOptions{Env: namedIssuersEnv(t, storetest.URL(t), bothIssuers(a, b),
		"BROKER_SERVICE_LOGINS="+namedService+"="+namedDaemonA+"=a,b "+namedService+"="+namedDaemonB+"=a,b",
	)})
	client := namedClient{t: t, base: "http://" + broker.Addr}
	if want := (map[string]any{"name": "b", "url": b.URL(), "read": true}); len(recordsWith(broker.Boot, issuerBootMessage, want)) == 0 {
		t.Fatalf("the boot log holds no line %q carrying %v: B's discovery document must be read at boot, so that only its key set fails:\n%s", issuerBootMessage, want, broker.Boot)
	}
	status, code, loginOfA := client.tokenLogin(clusterToken(t, a, aKey, namedDaemonA, ""), proofKey(t))
	if status != http.StatusOK || loginOfA.credentialID == "" {
		t.Fatalf("A's token login answered %d %q, want 200 with a credential, which B's pods enroll under", status, code)
	}
	enrollB := func() (int, string) {
		uid := "pod-of-b-" + uuid.NewString()
		status, code, _ := client.enroll(loginOfA, uid, clusterToken(t, b, bKey, namedWorker, uid))
		return status, code
	}
	loginB := func() (int, string) {
		status, code, _ := client.tokenLogin(clusterToken(t, b, bKey, namedDaemonB, ""), proofKey(t))
		return status, code
	}

	fetchesBefore := b.JWKSFetches()
	expect(t, "a token of B gets 503 OIDC_ISSUER_UNAVAILABLE ", []namedRoute{
		{"at enrollment", enrollB, http.StatusServiceUnavailable, issuerUnavailable},
		{"at the token login", loginB, http.StatusServiceUnavailable, issuerUnavailable},
	})
	t.Run("fifty tokens of B sent within 5 seconds cost B's issuer at most two key-set requests", func(t *testing.T) {
		started := time.Now()
		for i := range 50 {
			call := enrollB
			if i%2 == 1 {
				call = loginB
			}
			if status, code := call(); status != http.StatusServiceUnavailable || code != issuerUnavailable {
				t.Fatalf("token %d of B answered %d %q, want 503 %s", i+1, status, code, issuerUnavailable)
			}
		}
		if elapsed := time.Since(started); elapsed >= 5*time.Second {
			t.Fatalf("the fifty tokens took %s, so their key-set requests say nothing of 5 seconds", elapsed)
		}
		if fetches := b.JWKSFetches() - fetchesBefore; fetches > 2 {
			t.Fatalf("B's key set was requested %d times for the tokens of B, want at most 2: a key-set read that failed is not made again for 5 seconds", fetches)
		}
	})

	b.FailKeys(0)
	t.Run("once B's key set answers, a token of B passes both routes within 15 seconds", func(t *testing.T) {
		deadline := time.Now().Add(15 * time.Second)
		for _, route := range []struct {
			name string
			call func() (int, string)
			want int
		}{
			{"the token login", loginB, http.StatusOK},
			{"enrollment", enrollB, http.StatusCreated},
		} {
			var last string
			if !testwait.Holds(time.Until(deadline), 500*time.Millisecond, func() bool {
				status, code := route.call()
				last = fmt.Sprintf("%d %q", status, code)
				return status == route.want
			}) {
				t.Fatalf("a token of B at %s was not answered %d within 15 seconds of B's key set answering; it last answered %s", route.name, route.want, last)
			}
		}
	})
}

// TestTheExchangeTakesTheIssuersItNames: with BROKER_EXCHANGE_K8S_AUDIENCE set and three issuers
// named, A, B and C, the broker refuses to boot, naming BROKER_EXCHANGE_K8S_ISSUER, when that
// setting is missing, names an issuer the list does not hold, or is set without the exchange's
// audience, and when an entry of BROKER_EXCHANGE_ACCOUNTS names an issuer the setting does not
// name or names one twice. With the setting naming A and B, the exchange takes a token of A and a
// token of B for an account whose entry names both, and refuses with 400 invalid_request and no
// access token a token of C, a token of B for an account whose entry names A alone, and a token of
// B for an account whose entry names no issuer, which takes A, the first, alone. With B's
// discovery document failing, the broker boots, the exchange takes a token of A, and a token of B
// gets 503 OIDC_ISSUER_UNAVAILABLE. With one issuer and no BROKER_EXCHANGE_K8S_ISSUER, the
// exchange takes that issuer's token for an entry that names no issuer, whether the issuer is
// named or is one bare URL.
func TestTheExchangeTakesTheIssuersItNames(t *testing.T) {
	const (
		// namingA's entry names A alone, and namingNone's names no issuer; namedWorker's names A
		// and B. Each fixes a session of its own, so its pods exchange with no registration.
		namingA        = "system:serviceaccount:legion:legion-worker-a"
		namingNone     = "system:serviceaccount:legion:legion-worker-none"
		sessionOfA     = "example-session-a"
		sessionOfNone  = "example-session-none"
		exchangeIssuer = "BROKER_EXCHANGE_K8S_ISSUER"
	)
	binary := buildBroker(t)
	a, b, c := oidctest.New(t), oidctest.New(t), oidctest.New(t)
	aKey, bKey, cKey := a.PublishKey(t, "a-key"), b.PublishKey(t, "b-key"), c.PublishKey(t, "c-key")
	threeIssuers := bothIssuers(a, b) + ",c=" + c.URL()
	audience := "BROKER_EXCHANGE_K8S_AUDIENCE=" + exchangeAudience
	accounts := "BROKER_EXCHANGE_ACCOUNTS=" + namedWorker + "=ACME=ada@example.com=" + namedSession + "=a,b " +
		namingA + "=ACME=ada@example.com=" + sessionOfA + "=a " +
		namingNone + "=ACME=ada@example.com=" + sessionOfNone

	for _, refusal := range []struct {
		name string
		env  []string
		says []string
	}{
		{"BROKER_EXCHANGE_K8S_ISSUER is missing", []string{audience}, []string{exchangeIssuer}},
		// The broker's log is JSON, so a quoted name in a refusal reads \"name\" in it.
		{"BROKER_EXCHANGE_K8S_ISSUER names an issuer the list does not hold", []string{audience, exchangeIssuer + "=a,d"}, []string{exchangeIssuer, `\"d\"`}},
		{"BROKER_EXCHANGE_K8S_ISSUER is set without the exchange audience", []string{exchangeIssuer + "=a,b"}, []string{exchangeIssuer, "BROKER_EXCHANGE_K8S_AUDIENCE"}},
		{"an entry of BROKER_EXCHANGE_ACCOUNTS names an issuer the setting does not name",
			[]string{audience, exchangeIssuer + "=a,b", "BROKER_EXCHANGE_ACCOUNTS=" + namedWorker + "=ACME=ada@example.com==c"},
			[]string{exchangeIssuer, "BROKER_EXCHANGE_ACCOUNTS", `\"c\"`}},
		{"an entry of BROKER_EXCHANGE_ACCOUNTS names one issuer twice",
			[]string{audience, exchangeIssuer + "=a,b", "BROKER_EXCHANGE_ACCOUNTS=" + namedWorker + "=ACME=ada@example.com==a,a"},
			[]string{exchangeIssuer, "BROKER_EXCHANGE_ACCOUNTS", `\"a\"`}},
	} {
		t.Run("the broker refuses to boot when "+refusal.name, func(t *testing.T) {
			out := runRefusedBroker(t, binary, append([]string{
				"BROKER_DATABASE_URL=postgres://nonexistent-host-this-test-must-never-reach/db",
				"BROKER_PUBLIC_URL=https://secrets.internal.example",
				"BROKER_K8S_OIDC_ISSUER=" + threeIssuers,
				"BROKER_K8S_OIDC_AUDIENCE=" + enrolmentAudience,
			}, refusal.env...)...)
			for _, want := range refusal.says {
				if !strings.Contains(out, want) {
					t.Fatalf("the broker refused, but its output does not carry %s:\n%s", want, out)
				}
			}
		})
	}

	// exchange trades the token of account's pod, minted by issuer with key for the exchange's
	// audience, under the session account's entry fixes.
	exchange := func(t *testing.T, addr string, issuer *oidctest.Issuer, key *oidctest.Key, account, session string) (int, map[string]any) {
		t.Helper()
		token := mintPodToken(t, issuer, key, account, exchangeAudience, "pod-"+uuid.NewString())
		return newLiveBroker(addr, proofKey(t)).exchange(t, exchangeForm(token, session))
	}
	taken := func(t *testing.T, status int, body map[string]any) {
		t.Helper()
		if token, _ := body["access_token"].(string); status != http.StatusOK || token == "" {
			t.Fatalf("answered %d %v, want 200 with an access token", status, body)
		}
	}
	refused := func(t *testing.T, status int, body map[string]any) {
		t.Helper()
		if status != http.StatusBadRequest || body["error"] != "invalid_request" || body["access_token"] != nil {
			t.Fatalf("answered %d %v, want 400 invalid_request and no access token", status, body)
		}
	}

	t.Run("with the setting naming A and B", func(t *testing.T) {
		broker := startBroker(t, brokerOptions{Binary: binary, Env: namedIssuersEnv(t, storetest.URL(t), threeIssuers,
			audience, exchangeIssuer+"=a,b", accounts)})
		broker.Await(t, "token exchange")
		for _, row := range []struct {
			name    string
			issuer  *oidctest.Issuer
			key     *oidctest.Key
			account string
			session string
			taken   bool
		}{
			{"the exchange takes a token of A for an account whose entry names A and B", a, aKey, namedWorker, namedSession, true},
			{"the exchange takes a token of B for an account whose entry names A and B", b, bKey, namedWorker, namedSession, true},
			{"the exchange refuses a token of C, an issuer the setting does not name", c, cKey, namedWorker, namedSession, false},
			{"the exchange refuses a token of B for an account whose entry names A alone", b, bKey, namingA, sessionOfA, false},
			{"the exchange takes a token of A for an account whose entry names no issuer", a, aKey, namingNone, sessionOfNone, true},
			{"the exchange refuses a token of B for an account whose entry names no issuer", b, bKey, namingNone, sessionOfNone, false},
		} {
			t.Run(row.name, func(t *testing.T) {
				status, body := exchange(t, broker.Addr, row.issuer, row.key, row.account, row.session)
				if row.taken {
					taken(t, status, body)
				} else {
					refused(t, status, body)
				}
			})
		}
	})

	t.Run("with B's discovery document failing", func(t *testing.T) {
		b.FailDiscovery(http.StatusServiceUnavailable)
		t.Cleanup(func() { b.FailDiscovery(0) })
		broker := startBroker(t, brokerOptions{Binary: binary, Env: namedIssuersEnv(t, storetest.URL(t), threeIssuers,
			audience, exchangeIssuer+"=a,b", accounts)})
		broker.Await(t, "token exchange")
		t.Run("the broker boots and answers /healthz 200", func(t *testing.T) { requireHealthy(t, broker.Addr) })
		t.Run("the exchange takes a token of A", func(t *testing.T) {
			status, body := exchange(t, broker.Addr, a, aKey, namedWorker, namedSession)
			taken(t, status, body)
		})
		t.Run("a token of B gets 503 OIDC_ISSUER_UNAVAILABLE", func(t *testing.T) {
			status, body := exchange(t, broker.Addr, b, bKey, namedWorker, namedSession)
			if status != http.StatusServiceUnavailable || body["code"] != issuerUnavailable || body["access_token"] != nil {
				t.Fatalf("answered %d %v, want 503 %s and no access token", status, body, issuerUnavailable)
			}
		})
	})

	for _, one := range []struct{ name, issuers string }{
		{"named", "a=" + a.URL()},
		{"written as one bare URL", a.URL()},
	} {
		t.Run("with one issuer "+one.name+" and no BROKER_EXCHANGE_K8S_ISSUER, the exchange takes its token for an entry that names no issuer", func(t *testing.T) {
			broker := startBroker(t, brokerOptions{Binary: binary, Env: namedIssuersEnv(t, storetest.URL(t), one.issuers,
				audience, "BROKER_EXCHANGE_ACCOUNTS="+namingNone+"=ACME=ada@example.com="+sessionOfNone)})
			broker.Await(t, "token exchange")
			status, body := exchange(t, broker.Addr, a, aKey, namingNone, sessionOfNone)
			taken(t, status, body)
		})
	}
}

// TestAnAccountIsAcceptedFromTheIssuersItsEntryNames: a BROKER_API_ACCOUNTS entry that names no
// issuer accepts its subject's token of A, the first issuer, on GET /v1/pending, and answers its
// subject's token of B 403 ACCOUNT_NOT_LISTED; an entry that names A and B accepts both. A
// BROKER_SERVICE_LOGINS entry gives the same answers at the token login. A subject no entry lists
// gets 403 ACCOUNT_NOT_LISTED from either issuer. After a restart with an entry that no longer
// names B, the credential B's token login made stops authenticating, while the credential A's
// login made under the entry that names no issuer still does.
func TestAnAccountIsAcceptedFromTheIssuersItsEntryNames(t *testing.T) {
	const (
		callerNamingNone = "system:serviceaccount:example-ns:example-dispatch-one"
		callerNamingBoth = "system:serviceaccount:example-ns:example-dispatch-both"
		loginNamingNone  = "system:serviceaccount:legion-system:legion-daemon-one"
		loginNamingBoth  = "system:serviceaccount:legion-system:legion-daemon-both"
		stranger         = "system:serviceaccount:example-ns:example-stranger"
	)
	databaseURL := storetest.URL(t)
	binary := buildBroker(t)
	a, b := oidctest.New(t), oidctest.New(t)
	aKey, bKey := a.PublishKey(t, "a-key"), b.PublishKey(t, "b-key")
	// env is the broker's environment with the second login's entry naming its issuers as names
	// says.
	env := func(names string) []string {
		return namedIssuersEnv(t, databaseURL, bothIssuers(a, b),
			"BROKER_API_ACCOUNTS=dispatch="+callerNamingNone+" dispatch="+callerNamingBoth+"=a,b",
			"BROKER_SERVICE_LOGINS="+namedService+"="+loginNamingNone+" "+namedService+"="+loginNamingBoth+names)
	}
	broker := startBroker(t, brokerOptions{Binary: binary, Env: env("=a,b")})
	client := namedClient{t: t, base: "http://" + broker.Addr}
	tokenOf := map[string]func(t *testing.T, account string) string{
		"A": func(t *testing.T, account string) string { return clusterToken(t, a, aKey, account, "") },
		"B": func(t *testing.T, account string) string { return clusterToken(t, b, bKey, account, "") },
	}

	for _, c := range []struct {
		name, account, issuer string
		listed                bool
	}{
		{"the entry naming no issuer accepts its subject's token of A", callerNamingNone, "A", true},
		{"the entry naming no issuer answers its subject's token of B 403 ACCOUNT_NOT_LISTED", callerNamingNone, "B", false},
		{"the entry naming A and B accepts its subject's token of A", callerNamingBoth, "A", true},
		{"the entry naming A and B accepts its subject's token of B", callerNamingBoth, "B", true},
		{"a subject no entry lists gets 403 ACCOUNT_NOT_LISTED from A", stranger, "A", false},
		{"a subject no entry lists gets 403 ACCOUNT_NOT_LISTED from B", stranger, "B", false},
	} {
		t.Run("GET /v1/pending: "+c.name, func(t *testing.T) {
			status, code := client.pending(tokenOf[c.issuer](t, c.account))
			if c.listed && status != http.StatusOK || !c.listed && (status != http.StatusForbidden || code != "ACCOUNT_NOT_LISTED") {
				t.Fatalf("answered %d %q, want %s", status, code, map[bool]string{true: "200", false: "403 ACCOUNT_NOT_LISTED"}[c.listed])
			}
		})
	}

	// The credentials the logins below issue that the restart is read against: B's, made by the
	// entry naming both, and A's, made by the entry naming none.
	var fromB, fromA launcher
	for _, c := range []struct {
		name, account, issuer string
		listed                bool
		keep                  *launcher
	}{
		{"the entry naming no issuer accepts its subject's token of A", loginNamingNone, "A", true, &fromA},
		{"the entry naming no issuer answers its subject's token of B 403 ACCOUNT_NOT_LISTED", loginNamingNone, "B", false, nil},
		{"the entry naming A and B accepts its subject's token of A", loginNamingBoth, "A", true, nil},
		{"the entry naming A and B accepts its subject's token of B", loginNamingBoth, "B", true, &fromB},
		{"a subject no entry lists gets 403 ACCOUNT_NOT_LISTED from A", stranger, "A", false, nil},
		{"a subject no entry lists gets 403 ACCOUNT_NOT_LISTED from B", stranger, "B", false, nil},
	} {
		t.Run("the token login: "+c.name, func(t *testing.T) {
			status, code, issued := client.tokenLogin(tokenOf[c.issuer](t, c.account), proofKey(t))
			if c.listed && (status != http.StatusOK || issued.credentialID == "") || !c.listed && (status != http.StatusForbidden || code != "ACCOUNT_NOT_LISTED") {
				t.Fatalf("answered %d %q, want %s", status, code, map[bool]string{true: "200 with a credential", false: "403 ACCOUNT_NOT_LISTED"}[c.listed])
			}
			if c.keep != nil {
				*c.keep = issued
			}
		})
	}
	if fromA.credentialID == "" || fromB.credentialID == "" {
		t.Fatal("A's or B's login issued no credential, so there is nothing to read against the restart")
	}
	t.Run("the credential B's token login made authenticates while its entry names B", func(t *testing.T) {
		if status, code := client.authenticates(fromB); status != http.StatusNoContent {
			t.Fatalf("answered %d %q, want 204", status, code)
		}
	})

	broker.Terminate(t)
	if err := broker.AwaitExit(t, stopTimeout); err != nil {
		t.Fatalf("the first broker's exit = %v, want a clean one:\n%s", err, broker.Log())
	}
	restarted := startBroker(t, brokerOptions{Binary: binary, Env: env("=a")})
	client = namedClient{t: t, base: "http://" + restarted.Addr}
	expect(t, "after a restart with an entry that no longer names B, ", []namedRoute{
		{"the credential B's token login made stops authenticating", func() (int, string) { return client.authenticates(fromB) }, http.StatusUnauthorized, "LAUNCHER_INVALID"},
		{"the credential A's token login made under the entry naming no issuer still authenticates", func() (int, string) { return client.authenticates(fromA) }, http.StatusNoContent, ""},
	})
}
