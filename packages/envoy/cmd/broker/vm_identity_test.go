// vm_identity_test.go pins how the broker boots with Google identity tokens: the Google issuer's
// key set is read, and named in one boot line, only when BROKER_GOOGLE_OIDC_AUDIENCE is set; the
// machine services reach the policy loader; and an audience with no machine service refuses the
// boot. It also pins the boot's read of one bare cluster issuer, which the Google rows share.
package main

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/broker/config"
	"github.com/sjawhar/envoy/internal/broker/enroll"
	"github.com/sjawhar/envoy/internal/broker/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// exampleVMAccount is the one VM service account these rows bind, a placeholder: no real account,
// project or instance name appears in a test.
const exampleVMAccount = "example-fleet@example-project.iam.gserviceaccount.com"

// vmConfig loads the broker's configuration with cluster as its one cluster issuer, a bare URL,
// and, when audience is set, one VM service: example-fleet, run by exampleVMAccount in
// example-project as example-vm-*.
func vmConfig(t *testing.T, cluster, audience string) config.Config {
	t.Helper()
	env := map[string]string{
		"BROKER_DATABASE_URL":        "postgres://unused",
		"BROKER_PUBLIC_URL":          "https://secrets.internal.example",
		"BROKER_MACHINE_LOGIN_URL":   "https://dispatch.example/credentials/machine",
		"BROKER_SECRETS_PREFIX":      testSecretsPrefix,
		"BROKER_SECRETS_KMS_KEY_ARN": testSecretsKMSKeyARN,
		"BROKER_K8S_OIDC_ISSUER":     cluster,
		"BROKER_K8S_OIDC_AUDIENCE":   "example-broker-pod",
		"BROKER_SERVICES":            "example-service=system:serviceaccount:example:example-sa",
		// The four the approval page's sign-in needs, which every broker refuses to start
		// without: this configuration is a whole one, as a deployment's is.
		"BROKER_SIGNIN_ISSUER":             testSignInIssuer,
		"BROKER_SIGNIN_CLIENT_ID":          brokertest.SignInClientID,
		"BROKER_SIGNIN_CLIENT_SECRET_FILE": signInClientSecretFile(t),
		"BROKER_SIGNIN_DOMAIN":             brokertest.SignInDomain,
	}
	if audience != "" {
		env["BROKER_GOOGLE_OIDC_AUDIENCE"] = audience
		env["BROKER_VM_SERVICES"] = "example-fleet=" + exampleVMAccount
		env["BROKER_VM_PROJECTS"] = exampleVMAccount + "=example-project"
		env["BROKER_VM_INSTANCES"] = exampleVMAccount + "=example-vm-*"
	}
	cfg, err := config.Load(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// discovery is a keysFunc that records each issuer whose keys are asked for and answers a key
// source of its own for each, reading nothing.
type discovery struct {
	asked []string
	keys  map[string]*oidc.Keys
}

func (d *discovery) keysFor(_ context.Context, issuer string) (*oidc.Keys, error) {
	d.asked = append(d.asked, issuer)
	if d.keys == nil {
		d.keys = map[string]*oidc.Keys{}
	}
	d.keys[issuer] = &oidc.Keys{}
	return d.keys[issuer], nil
}

// localCluster is a cluster issuer this process serves, for a row that runs the boot's read of the
// clusters' issuers, which reads each one over HTTP: a placeholder host would leave that read
// waiting on a name no resolver holds.
func localCluster(t *testing.T) *oidctest.Issuer {
	t.Helper()
	cluster := oidctest.New(t)
	cluster.PublishKey(t, "cluster-key")
	return cluster
}

// captureLog sends slog's default logger to a buffer until t ends.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logged
}

// TestWithNoGoogleAudienceTheBrokerDiscoversOneIssuer: with one bare cluster issuer URL and no
// Google audience, the broker reads that one issuer at boot and logs one line naming its URL and
// that it was read; builds the pod verifier over it, which verifies that issuer's tokens; reads no
// key set through keysFor, since there is neither an exchange audience nor a Google one; and logs
// nothing about Google.
func TestWithNoGoogleAudienceTheBrokerDiscoversOneIssuer(t *testing.T) {
	logged := captureLog(t)
	cluster := oidctest.New(t)
	clusterKey := cluster.PublishKey(t, "cluster-key")
	d := &discovery{}
	found, err := discoverVerifiers(context.Background(), vmConfig(t, cluster.URL(), ""), d.keysFor)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.asked) != 0 {
		t.Fatalf("read the key sets of %v at boot, want none: the clusters' issuers are the pod verifier's own to read, and there is no exchange or Google issuer", d.asked)
	}
	wantLine := `msg="broker: kubernetes issuer" url=` + cluster.URL() + ` read=true`
	if lines := strings.Count(logged.String(), `msg="broker: kubernetes issuer"`); lines != 1 || !strings.Contains(logged.String(), wantLine) {
		t.Fatalf("log = %q, want one line holding %q: the one issuer, read at boot", logged, wantLine)
	}
	pods, ok := found.Pod.(enroll.K8sPodVerifier)
	if !ok {
		t.Fatalf("verifiers = pod %#v; want an enroll.K8sPodVerifier", found.Pod)
	}
	if _, ok := pods.Issuers.(loggedIssuers); !ok {
		t.Fatalf("the pod verifier checks tokens with %T, want loggedIssuers over the clusters' oidc.IssuerSet", pods.Issuers)
	}
	token := cluster.Mint(t, clusterKey, cluster.Claims("system:serviceaccount:example:example-sa", "example-broker-pod"))
	if claims, err := pods.VerifyAccount(context.Background(), token); err != nil || claims.Issuer != (oidc.NamedIssuer{URL: cluster.URL()}) {
		t.Fatalf("the pod verifier on a token of the cluster's issuer = %+v, %v; want it verified, naming that issuer with no name", claims, err)
	}
	if found.ExchangeK8s != nil || found.GoogleKeys != nil {
		t.Fatalf("the exchange was handed cluster issuers (%+v) or a Google key set (%p) with neither audience set; it must then register neither subject token type", found.ExchangeK8s, found.GoogleKeys)
	}
	if strings.Contains(strings.ToLower(logged.String()), "google") {
		t.Fatalf("log with no Google audience names Google:\n%s", logged)
	}
}

// TestAGoogleAudienceDiscoversGooglesIssuer: with BROKER_GOOGLE_OIDC_AUDIENCE set the broker also
// reads the Google issuer's key set and hands it to the token exchange, which is what lets the
// exchange register the Google subject token type and resolve a machine's token against Google's
// own keys, and logs one line naming the issuer, the audience, the machine services and how many
// machine entries the exchange holds.
func TestAGoogleAudienceDiscoversGooglesIssuer(t *testing.T) {
	logged := captureLog(t)
	d := &discovery{}
	found, err := discoverVerifiers(context.Background(), vmConfig(t, localCluster(t).URL(), "example-vm-audience"), d.keysFor)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{config.DefaultGoogleOIDCIssuer}; !slices.Equal(d.asked, want) {
		t.Fatalf("read the key sets of %v, want %v", d.asked, want)
	}
	if found.GoogleKeys == nil || found.GoogleKeys != d.keys[config.DefaultGoogleOIDCIssuer] {
		t.Fatalf("the exchange's Google key source = %p, want Google's own, %p", found.GoogleKeys, d.keys[config.DefaultGoogleOIDCIssuer])
	}
	lines := strings.Count(logged.String(), "google machine identity")
	wantLine := `msg="google machine identity" issuer=https://accounts.google.com audience=example-vm-audience services=example-fleet fleets=1`
	if lines != 1 || !strings.Contains(logged.String(), wantLine) {
		t.Fatalf("log = %q, want one line holding %q", logged, wantLine)
	}
}

// TestAStandInIssuerIsDiscoveredInGooglesPlace: BROKER_GOOGLE_OIDC_ISSUER names the issuer whose
// keys the broker reads and whose tokens the exchange takes, so a dev stack's stand-in
// (cmd/dev-oidc-issuer) serves in Google's place with nothing else changed.
func TestAStandInIssuerIsDiscoveredInGooglesPlace(t *testing.T) {
	logged := captureLog(t)
	d := &discovery{}
	cfg := vmConfig(t, localCluster(t).URL(), "example-vm-audience")
	cfg.GoogleOIDCIssuer = "http://127.0.0.1:18080"
	found, err := discoverVerifiers(context.Background(), cfg, d.keysFor)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{cfg.GoogleOIDCIssuer}; !slices.Equal(d.asked, want) {
		t.Fatalf("read the key sets of %v, want %v", d.asked, want)
	}
	if found.GoogleKeys == nil || found.GoogleKeys != d.keys[cfg.GoogleOIDCIssuer] {
		t.Fatalf("the exchange's Google key source = %p, want the stand-in issuer's own, %p", found.GoogleKeys, d.keys[cfg.GoogleOIDCIssuer])
	}
	if !strings.Contains(logged.String(), "issuer=http://127.0.0.1:18080") {
		t.Fatalf("log = %q, want the stand-in issuer named", logged)
	}
}

// TestTheEnrollmentServiceCarriesTheMachineServices pins the two places the broker hands
// BROKER_VM_SERVICES on: AuthenticateLauncher, without which a machine's own launcher credential
// stops authenticating at its next call, and the request decisions, without which a machine's
// session is never its fleet's.
func TestTheEnrollmentServiceCarriesTheMachineServices(t *testing.T) {
	cfg := vmConfig(t, "https://cluster.example", "example-vm-audience")
	enr := &enroll.Service{VMServicesByAccount: cfg.VMServicesByAccount}
	if want := map[string]string{exampleVMAccount: "example-fleet"}; !maps.Equal(enr.VMServicesByAccount, want) {
		t.Fatalf("the enrollment service's VMServicesByAccount = %v, want %v", enr.VMServicesByAccount, want)
	}
	m := newRequestMachine(cfg, nil, nil, nil, nil, airlock.Rules{}, "", nil)
	if want := map[string]string{"system:serviceaccount:example:example-sa": "example-service"}; !maps.Equal(m.ServicesByAccount, want) {
		t.Fatalf("request Machine's ServicesByAccount = %v, want %v", m.ServicesByAccount, want)
	}
	if want := map[string]string{exampleVMAccount: "example-fleet"}; !maps.Equal(m.VMServicesByAccount, want) {
		t.Fatalf("request Machine's VMServicesByAccount = %v, want %v: without it no machine's session is ever its fleet's", m.VMServicesByAccount, want)
	}
}

// TestThePolicyServesTheVMServicesSecrets: the policy loader's services are BROKER_SERVICES' and
// BROKER_VM_SERVICES', sorted and each once, so a secret a machine service owns is that
// service's and not refused as owner-tag-malformed.
func TestThePolicyServesTheVMServicesSecrets(t *testing.T) {
	if got, want := serviceNames(vmConfig(t, "https://cluster.example", "example-vm-audience")), []string{"example-fleet", "example-service"}; !slices.Equal(got, want) {
		t.Fatalf("serviceNames = %v, want %v", got, want)
	}
	if got, want := serviceNames(vmConfig(t, "https://cluster.example", "")), []string{"example-service"}; !slices.Equal(got, want) {
		t.Fatalf("serviceNames with no VM service = %v, want %v", got, want)
	}
}

// TestAGoogleAudienceWithNoVMServicesIsRefusedAtBoot drives the real binary: an audience with no
// VM service to bind refuses the boot, naming both settings, before the broker reaches its
// database or binds.
func TestAGoogleAudienceWithNoVMServicesIsRefusedAtBoot(t *testing.T) {
	out := runRefusedBroker(t, buildBroker(t),
		"BROKER_DATABASE_URL=postgres://nonexistent-host-this-test-must-never-reach/db",
		"BROKER_PUBLIC_URL=https://secrets.internal.example",
		"BROKER_GOOGLE_OIDC_AUDIENCE=example-vm-audience",
	)
	if !strings.Contains(out, "BROKER_GOOGLE_OIDC_AUDIENCE needs BROKER_VM_SERVICES") {
		t.Fatalf("broker refused, but not naming the audience's missing VM services: %s", out)
	}
}

// TestWithNoGoogleAudienceTheBootLogHoldsNoGoogleLine drives the real binary with the cluster's
// issuer and no Google audience: it boots, and its boot log names neither Google's issuer nor a VM
// identity line.
func TestWithNoGoogleAudienceTheBootLogHoldsNoGoogleLine(t *testing.T) {
	databaseURL := storetest.URL(t)
	issuer := oidctest.New(t)
	issuer.PublishKey(t, "signing-key")
	fakeSecretsFile := filepath.Join(t.TempDir(), "fake-secrets.json")
	if err := os.WriteFile(fakeSecretsFile, []byte(`{"secrets": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := startBroker(t, brokerOptions{Env: []string{
		"BROKER_DATABASE_URL=" + databaseURL,
		"BROKER_LISTEN_ADDR=127.0.0.1:0",
		"BROKER_PUBLIC_URL=http://127.0.0.1:0",
		"BROKER_FAKE_SECRETS_FILE=" + fakeSecretsFile,
		"BROKER_K8S_OIDC_ISSUER=" + issuer.URL(),
		"BROKER_K8S_OIDC_AUDIENCE=example-broker-pod",
	}})
	for _, never := range []string{config.DefaultGoogleOIDCIssuer, "google machine identity"} {
		if strings.Contains(broker.Boot, never) {
			t.Fatalf("boot log with no Google audience holds %q:\n%s", never, broker.Boot)
		}
	}
}
