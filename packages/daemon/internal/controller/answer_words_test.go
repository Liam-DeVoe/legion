package controller

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/testreason"
)

// TestAProbeLogsTheListenersWordsAsTheRuleReadsThem: the Envoy listener, or whatever answers in
// its place, answers the controller role lookup with words that quote a credential's URL: a 404
// whose reason does for a holder that is unresponsive (unresponsive), one whose holder state does
// (gone), and a 200 whose holder does (another-holder). The line the probe logs names each as the
// rule reads it, with the credentials the lookup carried redacted (oauth2call.RoleLookup), and none
// of what the listener's stand-in wrote. Captures controller.answer-words/<case>.
func TestAProbeLogsTheListenersWordsAsTheRuleReadsThem(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
		status           int
		probed           Liveness
	}{
		{"unresponsive", `{"reason":"holder_lapsed ` + testreason.Reason + `","holder":"ses_ctl","holder_state":"unresponsive"}`, "issuer.example/help", http.StatusNotFound, Unknown},
		{"gone", `{"reason":"holder_lapsed","holder":"ses_ctl","holder_state":"` + testreason.Reason + `"}`, "issuer.example/help", http.StatusNotFound, Gone},
		{"another-holder", `{"holder":"` + testreason.Reason + `","holder_state":"live","last_seen":1}`, "issuer.example/help", http.StatusOK, Gone},
	} {
		t.Run(c.name, func(t *testing.T) {
			var log bytes.Buffer
			l := &listener{status: c.status, body: c.body}
			if got := newTestProber(l.serve(t), "envoy-bearer", &log).Probe(context.Background(), "ses_ctl"); got != c.probed {
				t.Fatalf("Probe = %v, want %v (log: %s)", got, c.probed, log.String())
			}
			testreason.RequireNone(t, "controller.answer-words/"+c.name, log.String())
			if !strings.Contains(log.String(), c.want) {
				t.Errorf("the probe logged %q, want the listener's words as the rule reads them, %q", log.String(), c.want)
			}
		})
	}
}
