package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/going/agentwire"
)

// TestTheServerReadsGitHubAndTheWeb: the agent's reads outside Dispatch, which the server makes
// for it. A GitHub read of a repository no project maps is refused. A web read reads an address in
// the person's words or in a tool result of the run, and refuses one in neither; it stops at 1 MiB
// and at 10 s, and says it was cut; and a redirect to a private address is refused, judged on the
// connection itself. The web server of this test listens on loopback, which the reads admit beside
// the public addresses the server's own check admits; the check itself is held to addresses.
func TestTheServerReadsGitHubAndTheWeb(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			chunk := []byte(strings.Repeat("m", 64<<10))
			for range 48 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		case "/slow":
			_, _ = w.Write([]byte(strings.Repeat("s", 1024)))
			w.(http.Flusher).Flush()
			select {
			case <-time.After(15 * time.Second):
			case <-r.Context().Done():
			}
		case "/away":
			http.Redirect(w, r, "http://10.0.0.1/", http.StatusFound)
		default:
			_, _ = w.Write([]byte("a page of the web"))
		}
	}))
	t.Cleanup(web.Close)
	loopback := netip.MustParseAddrPort(web.Listener.Addr().String()).Addr()
	rig := newGoingAgentRig(t, func(agent *AgentListener) {
		agent.web.admit = func(address netip.Addr) bool { return address == loopback || goingPublicAddress(address) }
	})
	words := "read " + web.URL + "/large, then " + web.URL + "/slow, then " + web.URL + "/away"
	webRead := func(run, token string, call int, address string) (agentwire.CallAnswer, error) {
		return rig.call(agentwire.CallRequest{Run: run, LeaseToken: token, Step: 1, Call: call, Kind: agentwire.KindWeb, URL: address})
	}

	t.Run("a GitHub read of a repository no project maps is refused", func(t *testing.T) {
		run, token := rig.working(t, "alice", "read the pull request of acme/unmapped")
		_, err := rig.call(agentwire.CallRequest{Run: run, LeaseToken: token, Step: 1, Call: 1, Kind: agentwire.KindGitHub,
			Repository: "acme/unmapped", Path: "pulls/1"})
		requireAgentCode(t, err, agentwire.CodeRepositoryUnmapped, "a GitHub read of a repository no project maps")
	})

	t.Run("a web read of 3 MB answers 1 MB marked cut", func(t *testing.T) {
		run, token := rig.working(t, "alice", words)
		answer, err := webRead(run, token, 1, web.URL+"/large")
		if err != nil || answer.Status != http.StatusOK || len(answer.Body) != agentwire.CallBodyLimit || !answer.Cut {
			t.Fatalf("a read of 3 MiB answered %d with %d bytes, cut %v, %v; want 1 MiB marked cut", answer.Status, len(answer.Body), answer.Cut, err)
		}
	})

	t.Run("a read that takes 15 s ends at 10 s", func(t *testing.T) {
		run, token := rig.working(t, "alice", words)
		started := time.Now()
		answer, err := webRead(run, token, 1, web.URL+"/slow")
		took := time.Since(started)
		if err != nil || answer.Status != http.StatusOK || len(answer.Body) != 1024 || !answer.Cut || took < goingWebReadTime || took > goingWebReadTime+3*time.Second {
			t.Fatalf("a read of 15 s answered %d with %d bytes, cut %v, after %s, %v; want what it read by 10 s, marked cut", answer.Status, len(answer.Body), answer.Cut, took, err)
		}
	})

	t.Run("a redirect to 10.0.0.1 is refused", func(t *testing.T) {
		run, token := rig.working(t, "alice", words)
		started := time.Now()
		_, err := webRead(run, token, 1, web.URL+"/away")
		requireAgentCode(t, err, agentwire.CodeAddressRefused, "a read redirected to 10.0.0.1")
		if took := time.Since(started); took >= goingWebReadTime {
			t.Fatalf("the redirected read took %s, want it refused before any connection to 10.0.0.1", took)
		}
	})

	t.Run("an address in no word and no tool result is refused", func(t *testing.T) {
		run, token := rig.working(t, "alice", words)
		unseen := web.URL + "/unseen"
		_, err := webRead(run, token, 1, unseen)
		requireAgentCode(t, err, agentwire.CodeAddressUnseen, "a read of an address in no word and no tool result")
		// A tool result of the run names it: it is read.
		body, err := json.Marshal(map[string]any{"tool": "dispatch_read", "result": "the issue links " + unseen})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rig.client.Step(t.Context(), agentwire.StepRequest{Run: run, LeaseToken: token, Step: 1, Kind: agentwire.StepTool,
			Body: body, Note: "read an issue"}); err != nil {
			t.Fatalf("save the tool step: %v", err)
		}
		answer, err := webRead(run, token, 2, unseen)
		if err != nil || answer.Status != http.StatusOK || string(answer.Body) != "a page of the web" {
			t.Fatalf("a read of an address a tool result named answered %d %q, %v; want the page", answer.Status, answer.Body, err)
		}
	})

	t.Run("the public-address check", func(t *testing.T) {
		for address, public := range map[string]bool{
			"127.0.0.1":            false,
			"::1":                  false,
			"10.0.0.1":             false,
			"172.16.0.1":           false,
			"192.168.1.1":          false,
			"169.254.169.254":      false,
			"fe80::1":              false,
			"fd00:ec2::254":        false,
			"100.64.0.1":           false,
			"0.0.0.0":              false,
			"::ffff:10.0.0.1":      false,
			"64:ff9b::a00:1":       false,
			"224.0.0.1":            false,
			"8.8.8.8":              true,
			"2606:4700:4700::1111": true,
		} {
			if got := goingPublicAddress(netip.MustParseAddr(address)); got != public {
				t.Errorf("goingPublicAddress(%s) = %v, want %v", address, got, public)
			}
		}
	})
}
