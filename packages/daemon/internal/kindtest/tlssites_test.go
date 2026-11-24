package kindtest_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/kindtest"
)

// webSearchRequest is the one request the pinned Oh My Pi's keyless web_search sends Parallel's
// MCP endpoint, as testdata/parallel-mcp-request.json holds it. That file is written from the
// provider's code at the .omp-pin tag (oh-my-pi's packages/coding-agent/src/web/search/providers/
// parallel.ts, lines 83-98, and src/mcp/json-rpc.ts, lines 174-189): a JSON-RPC 2.0 tools/call of
// web_search whose id is a random base-36 string and whose arguments carry the capability check's
// query. Its id and session_id are stand-ins. A run's own request replaces it: the kind job keeps
// each request the stand-in received, with its body, as a line of tls-sites-requests.txt in its
// kind-<leg>-logs artifact (`grep '^POST search.parallel.ai/mcp ' tls-sites-requests.txt`), and the
// body after the status is the file, with the run's id written here.
const webSearchRequest = "testdata/parallel-mcp-request.json"

// A pod's web_search reaches the rig's stand-in for Parallel's MCP endpoint over TLS, under its
// own name and behind a certificate of the sites authority the pod trusts, and the stand-in
// answers the provider's one tools/call with what the provider reads as one result: a JSON-RPC 2.0
// result whose id is the request's own, the same JSON string, and whose structuredContent holds a
// result with a url. The stand-in keeps the request with its body. A call of another tool is
// answered with a JSON-RPC error, which the provider fails on, naming what it was asked.
func TestTheWebSearchStandInAnswersTheProvidersCallWithOneResult(t *testing.T) {
	sites := kindtest.StartTLSSites(t, "127.0.0.1:0")
	client := sitesClient(t, sites)
	request, err := os.ReadFile(webSearchRequest)
	if err != nil {
		t.Fatal(err)
	}
	request = bytes.TrimSpace(request)

	t.Run("search", func(t *testing.T) {
		answer := postWebSearch(t, client, request)
		var sent struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(request, &sent); err != nil {
			t.Fatal(err)
		}
		var got struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   json.RawMessage `json:"error"`
			Result  struct {
				IsError           bool `json:"isError"`
				StructuredContent struct {
					Results []struct {
						URL string `json:"url"`
					} `json:"results"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(answer, &got); err != nil {
			t.Fatalf("the stand-in's answer is no JSON: %v: %s", err, answer)
		}
		if got.JSONRPC != "2.0" || !bytes.Equal(got.ID, sent.ID) || got.Error != nil || got.Result.IsError {
			t.Fatalf("the stand-in answered %s; want a JSON-RPC 2.0 result with the request's id %s and no error", answer, sent.ID)
		}
		if results := got.Result.StructuredContent.Results; len(results) != 1 || results[0].URL == "" {
			t.Fatalf("the stand-in answered %s; want one result with a url in its structuredContent", answer)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, request); err != nil {
			t.Fatal(err)
		}
		if want := "POST " + kindtest.WebSearchHost + "/mcp -> 200 " + compact.String(); !slices.Contains(sites.Requests(), want) {
			t.Fatalf("the stand-in keeps the requests %q; want %q among them", sites.Requests(), want)
		}
	})

	t.Run("another tool", func(t *testing.T) {
		var call map[string]any
		if err := json.Unmarshal(request, &call); err != nil {
			t.Fatal(err)
		}
		call["params"].(map[string]any)["name"] = "fetch"
		other, err := json.Marshal(call)
		if err != nil {
			t.Fatal(err)
		}
		answer := postWebSearch(t, client, other)
		var got struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(answer, &got); err != nil {
			t.Fatalf("the stand-in's answer is no JSON: %v: %s", err, answer)
		}
		if got.Result != nil || !strings.Contains(got.Error.Message, `"tools/call" of "fetch"`) {
			t.Fatalf("the stand-in answered a call of another tool with %s; want a JSON-RPC error naming it", answer)
		}
	})
}

// postWebSearch POSTs body to the web-search stand-in's /mcp as the provider does, and returns the
// answer, failing t unless it is a 200 of JSON.
func postWebSearch(t *testing.T, client *http.Client, body []byte) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "https://"+kindtest.WebSearchHost+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST https://%s/mcp: %v", kindtest.WebSearchHost, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("POST https://%s/mcp answered %d %s: %s", kindtest.WebSearchHost, response.StatusCode, response.Header.Get("Content-Type"), answer)
	}
	return answer
}

// sitesClient is an HTTP client that reaches every name at the stand-ins' address and trusts the
// sites authority alone, as a pod of the rig does through CoreDNS and its image's trust store.
func sitesClient(t *testing.T, sites *kindtest.TLSSites) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(sites.AuthorityPEM()) {
		t.Fatal("the sites authority is no PEM certificate")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, sites.Addr())
		},
	}}
}
