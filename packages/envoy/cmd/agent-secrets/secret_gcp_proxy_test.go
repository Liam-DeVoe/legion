package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/airlock/airlocktest"
	"github.com/sjawhar/envoy/internal/broker/airlockproxy"
	"github.com/sjawhar/envoy/internal/broker/requests"
)

const (
	gcpAirlockHost        = "secretmanager.googleapis.com"
	gcpAirlockName        = "not-a-secret"
	gcpAirlockToken       = "not-a-secret-access-token"
	gcpAirlockPlaceholder = requests.PlaceholderPrefix + "not-a-secret-placeholder-for-test"
)

// gcpAirlockGrant supplies one Google login grant; the real proxy does the TLS termination,
// grant authentication and placeholder replacement.
type gcpAirlockGrant struct{}

func (gcpAirlockGrant) AirlockConnection(_ context.Context, placeholder string) (requests.AirlockConnection, error) {
	if placeholder != gcpAirlockPlaceholder {
		return requests.AirlockConnection{}, requests.ErrGrantNotLive
	}
	return requests.AirlockConnection{
		GrantID: "not-a-secret-grant",
		Names: map[string]requests.AirlockGrantName{
			gcpAirlockName: {Kind: "google-login", Slots: map[string]string{requests.SlotValue: gcpAirlockPlaceholder}},
		},
	}, nil
}

func (gcpAirlockGrant) AirlockValues(_ context.Context, grantID string) (map[string]requests.AirlockSecret, string, error) {
	if grantID != "not-a-secret-grant" {
		return nil, "", requests.ErrGrantNotLive
	}
	return map[string]requests.AirlockSecret{
		gcpAirlockName: {Kind: "google-login", Slots: map[string]string{requests.SlotValue: gcpAirlockToken}},
	}, "not-a-secret-policy", nil
}

func (gcpAirlockGrant) PolicyVersion() string { return "not-a-secret-policy" }

func (gcpAirlockGrant) AirlockAccessPlaceholder(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("this grant has no Azure exchange")
}

// TestGCPSecretListThroughHTTP1Airlock catches a return to the gRPC constructor: the actual
// airlock terminates TLS without HTTP/2 ALPN, so only the REST client can complete this form.
// The subprocess keeps the SDK's proxy and system-root caches independent of other tests.
func TestGCPSecretListThroughHTTP1Airlock(t *testing.T) {
	binary := buildAgentSecrets(t)
	service, err := airlocktest.NewService(airlocktest.ServiceOptions{Hosts: []string{gcpAirlockHost}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	service.AnswerWith(func(w http.ResponseWriter, r *http.Request, _ airlocktest.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+gcpAirlockToken {
			http.Error(w, "the grant's access token is required", http.StatusUnauthorized)
			return true
		}
		switch r.URL.Path {
		case "/transport-control":
			io.WriteString(w, `{}`)
		case "/v1/projects/" + testGCPProject + "/secrets":
			fmt.Fprintf(w, `{"secrets":[{"name":%q,"annotations":{"owner":"shared","tier":"agent"}}]}`, testGCPPrefix+"NOT_A_SECRET")
		case "/v1/" + testGCPPrefix + "NOT_A_SECRET/versions/latest":
			fmt.Fprintf(w, `{"name":%q,"state":"ENABLED"}`, testGCPPrefix+"NOT_A_SECRET/versions/1")
		default:
			http.NotFound(w, r)
		}
		return true
	})

	authority, err := airlocktest.NewAuthority("not-a-secret-test-authority")
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, err := authority.WriteFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := airlockproxy.NewAuthority(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := airlockproxy.New(airlockproxy.Options{
		CA: ca, Grants: gcpAirlockGrant{},
		Rules: airlock.Rules{Secrets: []airlock.SecretRule{{Name: gcpAirlockName, Hosts: []string{gcpAirlockHost}}}},
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, service.Addr())
		},
		UpstreamRoots: service.Authority().Pool(),
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := proxy.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Errorf("serve proxy: %v", err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proxy.Shutdown(ctx); err != nil {
			t.Errorf("stop proxy: %v", err)
		}
	})
	proxyURL := &url.URL{Scheme: "http", Host: listener.Addr().String(), User: url.UserPassword("grant", gcpAirlockPlaceholder)}

	// Control: offer HTTP/2, verify the connection falls back to HTTP/1.1, and prove the
	// same grant reaches the service before asking the production CLI to use it.
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL), ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{RootCAs: authority.Pool()},
	}
	t.Cleanup(transport.CloseIdleConnections)
	req, err := http.NewRequest(http.MethodGet, "https://"+gcpAirlockHost+"/transport-control", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+gcpAirlockPlaceholder)
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Proto != "HTTP/1.1" || resp.TLS.NegotiatedProtocol != "http/1.1" {
		t.Fatalf("HTTP/1.1 control: status=%d protocol=%s ALPN=%q", resp.StatusCode, resp.Proto, resp.TLS.NegotiatedProtocol)
	}
	t.Log("control: HTTP/1.1 with ALPN http/1.1 reaches the service through the airlock")

	startSecretBrokerWith(t, gcpTestSettings(), alwaysServed)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "secret", "list")
	cmd.Env = append(os.Environ(),
		"CLOUDSDK_AUTH_ACCESS_TOKEN="+gcpAirlockPlaceholder,
		"HTTPS_PROXY="+proxyURL.String(), "https_proxy="+proxyURL.String(),
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		"SSL_CERT_FILE="+certFile, "SSL_CERT_DIR="+t.TempDir(),
		"GOOGLE_API_USE_MTLS_ENDPOINT=never", "GOOGLE_API_USE_CLIENT_CERTIFICATE=false",
		"GRPC_GO_LOG_SEVERITY_LEVEL=info",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("secret list through the HTTP/1.1 airlock: %v (deadline: %v)\n%s", err, ctx.Err(), out)
	}
	if !strings.Contains(string(out), "NOT_A_SECRET") || !strings.Contains(string(out), "shared") {
		t.Fatalf("secret list did not return the REST secret: %s", out)
	}
	if strings.Contains(string(out), gcpAirlockPlaceholder) || strings.Contains(string(out), gcpAirlockToken) {
		t.Fatalf("secret list printed a credential: %s", out)
	}
	seen := service.Requests()
	if len(seen) != 3 || seen[1].Method != http.MethodGet || seen[1].Path != "/v1/projects/"+testGCPProject+"/secrets" || seen[2].Path != "/v1/"+testGCPPrefix+"NOT_A_SECRET/versions/latest" {
		t.Fatalf("control, ListSecrets and GetSecretVersion requests: %+v", seen)
	}
}
