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
//
// While the key sets cannot be read, a token the library refuses whatever key signed it is refused
// the same way and not answered 503: an expired token of GitHub, of Google or of a cluster, and a
// token of GitHub or of Google whose iss names another issuer. A live token of the same key set is
// answered 503 meanwhile, so the key set does fail.
func TestATokenSignedByAKeyItsIssuerNeverPublishedIsStillRefused(t *testing.T) {
	type refusalRow struct {
		name string
		form url.Values
	}
	requireRefused := func(t *testing.T, f *signInFixture, row refusalRow) {
		t.Helper()
		status, header, body := f.post(t, "/token", row.form, newKey(t))
		description, _ := body["error_description"].(string)
		if status != http.StatusBadRequest || body["error"] != "invalid_request" || body["access_token"] != nil || !strings.Contains(description, "Unable to parse the JSON web token") {
			t.Fatalf("answered %d %v, want 400 invalid_request refusing a token it could not parse", status, body)
		}
		if got := header.Get("Retry-After"); got != "" {
			t.Fatalf("the refusal carries Retry-After %q, want none", got)
		}
	}
	now := time.Now()

	t.Run("while the key sets are read", func(t *testing.T) {
		f := newSignInFixture(t)
		stray := oidctest.New(t).PublishKey(t, "stray-key")
		for _, row := range []refusalRow{
			{"a workflow run's token of GitHub signed by a key GitHub never published", exchangeForm(f.github.Mint(t, stray, map[string]any{
				"iss": f.github.URL(), "sub": "repo:" + repository + ":ref:" + ref, "aud": workflowAudience,
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(15 * time.Minute).Unix(), "jti": "run-4343",
			}), oauthwire.SubjectTokenTypeGitHub, "ci-release")},
			{"a machine's token of Google signed by a key Google never published", exchangeForm(f.google.Mint(t, stray, map[string]any{
				"iss": f.google.URL(), "sub": "110" + signInMachine, "aud": machineAudience,
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(30 * time.Minute).Unix(),
				"email": signInMachine, "email_verified": true,
			}), oauthwire.SubjectTokenTypeGoogle, "fleet-box-1")},
		} {
			t.Run(row.name, func(t *testing.T) { requireRefused(t, f, row) })
		}
	})

	t.Run("while the key sets cannot be read", func(t *testing.T) {
		f := newSignInFixture(t)
		// Every key set fails before any token of it is checked, so no key is held from a good read.
		for _, issuer := range []*oidctest.Issuer{f.cluster, f.github, f.google} {
			issuer.FailKeys(http.StatusServiceUnavailable)
		}
		expired := func(claims map[string]any) {
			claims["iat"], claims["exp"] = now.Add(-20*time.Minute).Unix(), now.Add(-5*time.Minute).Unix()
		}
		machine := func(change func(map[string]any)) string {
			claims := map[string]any{
				"iss": f.google.URL(), "sub": "110" + signInMachine, "aud": machineAudience,
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(30 * time.Minute).Unix(),
				"email": signInMachine, "email_verified": true,
			}
			change(claims)
			return f.google.Mint(t, f.googleKey, claims)
		}
		elsewhere := func(claims map[string]any) { claims["iss"] = "https://issuer.elsewhere.example" }

		t.Run("a live workflow run's token of GitHub is answered 503", func(t *testing.T) {
			status, header, body := f.post(t, "/token", exchangeForm(f.workflowToken(t, "4400", nil), oauthwire.SubjectTokenTypeGitHub, "ci-release"), newKey(t))
			if status != http.StatusServiceUnavailable || body["code"] != oidc.IssuerUnavailableCode || header.Get("Retry-After") != "1" {
				t.Fatalf("answered %d %v (Retry-After %q), want 503 %s with Retry-After: 1: the key set must fail for the rows after this to mean anything", status, body, header.Get("Retry-After"), oidc.IssuerUnavailableCode)
			}
		})
		for _, row := range []refusalRow{
			{"an expired workflow run's token of GitHub", exchangeForm(f.workflowToken(t, "4401", expired), oauthwire.SubjectTokenTypeGitHub, "ci-release")},
			{"a workflow run's token whose iss is not GitHub's", exchangeForm(f.workflowToken(t, "4402", elsewhere), oauthwire.SubjectTokenTypeGitHub, "ci-release")},
			{"an expired machine's token of Google", exchangeForm(machine(expired), oauthwire.SubjectTokenTypeGoogle, "fleet-box-1")},
			{"a machine's token whose iss is not Google's", exchangeForm(machine(elsewhere), oauthwire.SubjectTokenTypeGoogle, "fleet-box-1")},
			{"an expired pod's token of a cluster", exchangeForm(f.podToken(t, "pod-uid-expired", expired), oauthwire.SubjectTokenTypeKubernetes, "implementer-g1")},
		} {
			t.Run(row.name+" is refused, not answered 503", func(t *testing.T) { requireRefused(t, f, row) })
		}
	})
}
