// private_list_test.go drives GET /v1/private-projects through the REAL compiled broker, pointed
// at a Dispatch stand-in with BROKER_PROJECT_ACCESS_URL: the list the shared session watch reads
// is the one Dispatch answers through the broker's own client, read with the broker's own token,
// which is how cmd/cipherd wires the route.
package main

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// TestTheBinaryAnswersTheWatchDispatchsPrivateProjects: with Dispatch holding PRIV private and
// PUB public, the session-watch caller reads ["PRIV"] from the broker's binary, and the stand-in
// saw the broker ask with the one bearer it takes.
func TestTheBinaryAnswersTheWatchDispatchsPrivateProjects(t *testing.T) {
	databaseURL := storetest.URL(t)
	cluster := oidctest.New(t)
	clusterKey := cluster.PublishKey(t, "signing-key")
	fakeSecretsFile := writeTempFile(t, "fake-secrets.json", []byte(`{"secrets": []}`))
	dispatch := projectAccessStandIn(t)
	tokenFile := writeTempFile(t, "dispatch-access-token", []byte(dispatch.Token+"\n"))
	broker := startBroker(t, brokerOptions{Env: []string{
		"BROKER_DATABASE_URL=" + databaseURL,
		"BROKER_LISTEN_ADDR=127.0.0.1:0",
		"BROKER_PUBLIC_URL=http://127.0.0.1:0",
		"BROKER_FAKE_SECRETS_FILE=" + fakeSecretsFile,
		"BROKER_K8S_OIDC_ISSUER=" + cluster.URL(),
		"BROKER_K8S_OIDC_AUDIENCE=" + enrolmentAudience,
		"BROKER_API_ACCOUNTS=session-watch=" + watchAccount,
		"BROKER_PROJECT_ACCESS_URL=" + dispatch.URL,
		"BROKER_PROJECT_ACCESS_TOKEN_FILE=" + tokenFile,
	}})
	live := newPlainBroker(broker.Addr)
	before := dispatch.PrivateProjectsCalls()
	status, body := live.callAs(t, http.MethodGet, privateListRoute, accountToken(t, cluster, clusterKey, watchAccount), "", "")
	if status != http.StatusOK {
		t.Fatalf("GET %s as session-watch = %d %v, want 200 and Dispatch's private projects", privateListRoute, status, body)
	}
	if want := []any{privateProject}; !reflect.DeepEqual(body["private_projects"], want) {
		t.Fatalf("GET %s as session-watch answers %v, want %v: the private projects Dispatch holds, and no public one", privateListRoute, body, want)
	}
	if dispatch.PrivateProjectsCalls() <= before {
		t.Fatalf("the Dispatch stand-in answered %d private-list calls before the read and %d after, want the read to have asked it", before, dispatch.PrivateProjectsCalls())
	}
}
