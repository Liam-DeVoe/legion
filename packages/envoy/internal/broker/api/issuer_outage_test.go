// issuer_outage_test.go drives every broker route that verifies a projected ServiceAccount token
// while the cluster's key set cannot be read, on the same real broker api_test.go mounts.
package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
)

// A token the broker cannot judge because its issuer's key set cannot be read reaches no verdict,
// and every route that verifies a ServiceAccount token answers it 503 ISSUER_UNAVAILABLE, never
// the 401 or 403 a refused token gets: a caller retries a 503 and stops on a refusal, so a live
// token refused for an outage ended an enrolment, a login, a registration or a call that the next
// attempt would have landed (review B4). Each token here is signed by a key published after the
// broker read the set, so it sends the verifier to read the set again, which answers 500.
func TestATokenWhoseIssuerCannotBeReadIsUnavailableNotRefused(t *testing.T) {
	ts := newTestServer(t)
	// A token login before the outage: the set is read, and the launcher credential below enrols.
	credentialID, launcherKey := ts.mintServiceLauncherCredential(t)
	registrar := ts.accountToken(t, daemonAccount, "", time.Hour)
	// A pod enrolled before the outage, whose renewal carries its current token (renewEnrollment).
	renewingUID, renewingKey := uuid.NewString(), newSigningKey(t)
	status, body := ts.launcher(t, launcherKey, credentialID, http.MethodPost, "/v1/enrollments", map[string]any{
		"kind": "pod", "runtime_id": renewingUID, "thumbprint": thumbprintOf(t, renewingKey), "pod_token": ts.podToken(t, workerAccount, renewingUID),
	})
	if status != http.StatusCreated {
		t.Fatalf("the pod's enrolment before the outage = %d %s, want 201", status, body)
	}
	renewing := decode[wireEnrolled](t, body).EnrollmentID
	rotated := ts.podIssuer.PublishKey(t, "rotated-key")
	mint := func(account, podUID string) string {
		t.Helper()
		claims := ts.podIssuer.Claims(account, podAudience)
		now := time.Now()
		claims["iat"] = now.Add(-5 * time.Second).Unix()
		claims["nbf"] = claims["iat"]
		claims["exp"] = now.Add(time.Hour).Unix()
		if podUID != "" {
			claims["kubernetes.io"] = map[string]any{"pod": map[string]any{"uid": podUID}}
		}
		return ts.podIssuer.Mint(t, rotated, claims)
	}
	ts.podIssuer.FailKeys(http.StatusInternalServerError)

	for _, route := range []struct {
		name string
		send func() (int, []byte)
	}{
		{"an account caller's token, GET /v1/pending (accountAuth)", func() (int, []byte) {
			return ts.req(t, http.MethodGet, "/v1/pending?approver="+testApprover, map[string]string{"Authorization": "Bearer " + mint(dispatchAccount, "")}, nil)
		}},
		{"a service's token, POST /v1/oauth/exchange/pod-sessions (serviceAuth)", func() (int, []byte) {
			return ts.registerPodSession(t, mint(daemonAccount, ""), mint(brokertest.ExchangeAccount, "pod-uid-outage"), "outage-session")
		}},
		{"the pod's token a registrar sends, POST /v1/oauth/exchange/pod-sessions", func() (int, []byte) {
			return ts.registerPodSession(t, registrar, mint(brokertest.ExchangeAccount, "pod-uid-outage"), "outage-session")
		}},
		{"a service's token login, POST /v1/launcher-credentials/token", func() (int, []byte) {
			return ts.tokenLogin(t, mint(daemonAccount, uuid.NewString()), ts.daemonRequest(t, newSigningKey(t)))
		}},
		{"a pod's enrolment token, POST /v1/enrollments", func() (int, []byte) {
			podUID := uuid.NewString()
			return ts.launcher(t, launcherKey, credentialID, http.MethodPost, "/v1/enrollments", map[string]any{
				"kind": "pod", "runtime_id": podUID, "thumbprint": thumbprintOf(t, newSigningKey(t)),
				"pod_token": mint(workerAccount, podUID),
			})
		}},
		{"a pod's renewal token, POST /v1/enrollments/{id}/renew (renewEnrollment)", func() (int, []byte) {
			return ts.session(t, renewingKey, renewing, http.MethodPost, "/v1/enrollments/"+renewing+"/renew", map[string]any{
				"pod_token": mint(workerAccount, renewingUID),
			})
		}},
	} {
		t.Run(route.name, func(t *testing.T) {
			status, body := route.send()
			if refusal := decode[wireError](t, body); status != http.StatusServiceUnavailable || refusal.Code != "ISSUER_UNAVAILABLE" {
				t.Fatalf("%s while the key set answers 500 = %d %s, want 503 ISSUER_UNAVAILABLE: the token was never judged", route.name, status, body)
			}
		})
	}
}
