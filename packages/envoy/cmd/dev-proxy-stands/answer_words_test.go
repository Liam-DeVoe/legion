package main

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// TestAnUpgradeAnswerPrintsTheServicesWordsAsTheRuleReadsThem: the service behind the proxy, or
// whatever answers in its place, answers the WebSocket upgrade with an Upgrade header, and its frame
// back, that quote a credential's URL and echo the bearer the request carried. The upgrade's
// result names the header and the frame as the rule reads them, with the request's bearer
// redacted (redact.Header, and redact.Field over redact.Tokens for the frame), and none of what the
// service's stand-in wrote; the frame's own words still print. Captures
// dev-proxy-stands.answer-words/upgrade.
func TestAnUpgradeAnswerPrintsTheServicesWordsAsTheRuleReadsThem(t *testing.T) {
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		conn, out, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack the connection: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(out, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket %s %s\r\nConnection: Upgrade\r\n\r\n", bearer, redacttest.Words)
		_ = out.Flush()
		line, _ := bufio.NewReader(out).ReadString('\n')
		fmt.Fprintf(out, "the service read %s; bearer %s; %s\n", strings.TrimSpace(line), bearer, redacttest.Words)
		_ = out.Flush()
	}))
	defer service.Close()
	proxyAddr, _ := connectProxy(t, service.Listener.Addr().String())
	dir := t.TempDir()
	ca := writeFile(t, dir, "ca.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: service.Certificate().Raw})))
	proxyFile := writeFile(t, dir, "proxy", fixtureProxy(proxyAddr)+"\n")
	headerFile := writeFile(t, dir, "header", redacttest.Token+"\n")
	var stdout, stderr strings.Builder
	if code := run(context.Background(), []string{"load", "upgrade", "--proxy-file", proxyFile, "--header-file", headerFile,
		"--host", "example.com", "--ca", ca}, &stdout, &stderr); code != 0 {
		t.Fatalf("dev-airlock-stands load upgrade exited %d; stderr: %s", code, stderr.String())
	}
	redacttest.RequireNone(t, "dev-proxy-stands.answer-words/upgrade", stdout.String())
	var result struct {
		Status  int
		Upgrade string
		Frames  string
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("load upgrade answered %q: %v", stdout.String(), err)
	}
	if result.Status != http.StatusSwitchingProtocols || !strings.HasPrefix(result.Upgrade, "websocket") || !strings.Contains(result.Upgrade, redacttest.Shown) ||
		!strings.Contains(result.Frames, "the service read a frame from the command") || !strings.Contains(result.Frames, redacttest.Shown) {
		t.Errorf("load upgrade answered %+v; want 101, the Upgrade header and the frame each as the rule reads them", result)
	}
}
