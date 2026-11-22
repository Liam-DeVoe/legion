package oauth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/oauth"
	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// TestATokenWhoseKeySetCannotBeReadIsAnswered503WithItsWait: a subject or actor token whose key
// the broker cannot read, because its issuer's key set cannot be read now, is answered 503
// OIDC_ISSUER_UNAVAILABLE with Retry-After: 1 and no access token, whichever issuer signed it: a
// cluster's, GitHub's, or Google's, for a machine's own trade and for a person's sign-in, whose
// actor is the machine's token. Before GitHub's and Google's key sources were read as the
// cluster's is, the library refused those three as tokens it could not parse, 400
// invalid_request, which the broker's token clients take as final.
func TestATokenWhoseKeySetCannotBeReadIsAnswered503WithItsWait(t *testing.T) {
	cluster := oidctest.New(t)
	clusterKey := cluster.PublishKey(t, "cluster-key")
	f := newSignInFixtureWith(t, func(c *oauth.Config) {
		c.Exchange.Kubernetes = []oauth.IssuerConfig{{Name: clusterName, Issuer: cluster.URL(), Audience: podAudience, Keys: oidc.NewLazyVerifier(cluster.URL(), podAudience)}}
	})
	// Every key set fails before any token of it is checked, so no key is held from a good read.
	for _, issuer := range []*oidctest.Issuer{cluster, f.github, f.google} {
		issuer.FailKeys(http.StatusServiceUnavailable)
	}
	podClaims := cluster.Claims(account, podAudience)
	podClaims["kubernetes.io"] = map[string]any{"pod": map[string]any{"uid": "pod-uid-unread"}}
	for _, row := range []struct {
		name string
		form url.Values
	}{
		{"a pod's token of a cluster", exchangeForm(cluster.Mint(t, clusterKey, podClaims), oauthwire.SubjectTokenTypeKubernetes, "implementer-g1")},
		{"a workflow run's token of GitHub", exchangeForm(f.workflowToken(t, "4242", nil), oauthwire.SubjectTokenTypeGitHub, "ci-release")},
		{"a machine's token of Google", exchangeForm(f.machineToken(t, signInMachine, machineAudience, true), oauthwire.SubjectTokenTypeGoogle, "fleet-box-1")},
		{"a person's sign-in, whose actor is a machine's token of Google", signInForm(
			f.personToken(t, person, deviceAudience, true, signInDomain),
			f.machineToken(t, signInMachine, machineAudience, true),
			"ada-box-1")},
	} {
		t.Run(row.name, func(t *testing.T) {
			status, header, body := f.post(t, "/token", row.form, newKey(t))
			if status != http.StatusServiceUnavailable || body["code"] != oidc.IssuerUnavailableCode || body["access_token"] != nil {
				t.Fatalf("answered %d %v, want 503 %s and no access token", status, body, oidc.IssuerUnavailableCode)
			}
			if got := header.Get("Retry-After"); got != "1" {
				t.Fatalf("the 503 carries Retry-After %q, want \"1\", the Envoy listener's wait for the same fault", got)
			}
		})
	}
}

// TestATokenSignedByAKeyItsIssuerNeverPublishedIsStillRefused: the control of the test above. A
// GitHub or Google token signed by a key its issuer never published, while that issuer's key set
// is read, is a token the broker judged: the library refuses it 400 invalid_request as a token it
// could not parse, with no access token and no Retry-After, since no wait changes the answer.
func TestATokenSignedByAKeyItsIssuerNeverPublishedIsStillRefused(t *testing.T) {
	f := newSignInFixture(t)
	stray := oidctest.New(t).PublishKey(t, "stray-key")
	now := time.Now()
	for _, row := range []struct {
		name string
		form url.Values
	}{
		{"a workflow run's token of GitHub", exchangeForm(f.github.Mint(t, stray, map[string]any{
			"iss": f.github.URL(), "sub": "repo:" + repository + ":ref:" + ref, "aud": workflowAudience,
			"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(15 * time.Minute).Unix(), "jti": "run-4343",
		}), oauthwire.SubjectTokenTypeGitHub, "ci-release")},
		{"a machine's token of Google", exchangeForm(f.google.Mint(t, stray, map[string]any{
			"iss": f.google.URL(), "sub": "110" + signInMachine, "aud": machineAudience,
			"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(30 * time.Minute).Unix(),
			"email": signInMachine, "email_verified": true,
		}), oauthwire.SubjectTokenTypeGoogle, "fleet-box-1")},
	} {
		t.Run(row.name, func(t *testing.T) {
			status, header, body := f.post(t, "/token", row.form, newKey(t))
			description, _ := body["error_description"].(string)
			if status != http.StatusBadRequest || body["error"] != "invalid_request" || body["access_token"] != nil || !strings.Contains(description, "Unable to parse the JSON web token") {
				t.Fatalf("answered %d %v, want 400 invalid_request refusing a token it could not parse", status, body)
			}
			if got := header.Get("Retry-After"); got != "" {
				t.Fatalf("the refusal carries Retry-After %q, want none", got)
			}
		})
	}
}
