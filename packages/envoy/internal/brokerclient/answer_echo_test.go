package brokerclient

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// TestATradeAnswerPrintsNoSubjectTokenItEchoes: something in the broker's place answers the trade
// and echoes the subject token its form carried, in a status's words that are no OAuth refusal (a
// 302 and a 502, each printed as the broker's words beside its status) and in the values of a 200
// that is not this grant's (its token_type and its credential_expires_at). The trade's error, and
// the log line the renewal loop writes of it (retriedTrade), hold none of it: every answer the
// trade reads prints through the record of the request it sent (redact.Carried), whatever its
// status, and a 200's values print nowhere, as in the TypeScript client (the review of #4368 at
// 63bd188095, findings 1 and 2). Captures brokerclient.answer-echo/<case> and
// brokerclient.answer-echo/<case>/log.
func TestATradeAnswerPrintsNoSubjectTokenItEchoes(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   map[string]any
	}{
		{"302-words", 302, map[string]any{"error": "moved; the form was subject_token={subject}"}},
		{"502-words", 502, map[string]any{"error": "upstream refused subject_token={subject}"}},
		{"200-token-type", 200, map[string]any{"access_token": "a", "token_type": "Bearer {subject}", "expires_in": 60}},
		{"200-credential-expires-at", 200, map[string]any{"access_token": "a", "token_type": "DPoP", "expires_in": 60, "credential_expires_at": "{subject}"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			broker := newStubBroker(t, func(_ int, form url.Values) (int, map[string]any) {
				echoed := map[string]any{}
				for k, v := range c.body {
					if s, ok := v.(string); ok {
						v = strings.ReplaceAll(s, "{subject}", form.Get("subject_token"))
					}
					echoed[k] = v
				}
				return c.status, echoed
			})
			client, err := New(Config{BrokerURL: broker.Address(), Key: testKey(t),
				Subject: &scriptedSource{token: redacttest.Token, tokenType: "urn:envoy:params:oauth:token-type:kubernetes-service-account", describe: "probe subject"}})
			if err != nil {
				t.Fatal(err)
			}
			_, tradeErr := client.trade(context.Background())
			if tradeErr == nil {
				t.Fatal("the trade answered no error")
			}
			if len(broker.Requests()) != 1 {
				t.Fatalf("the broker saw %d trades, want the one", len(broker.Requests()))
			}
			redacttest.RequireNone(t, "brokerclient.answer-echo/"+c.name, tradeErr.Error())
			msg, args := retriedTrade(tradeErr)
			var line bytes.Buffer
			slog.New(slog.NewTextHandler(&line, nil)).Warn(msg, args...)
			redacttest.RequireNone(t, "brokerclient.answer-echo/"+c.name+"/log", line.String())
		})
	}
}

// TestARefusalBesideACredentialedAddressPrintsNoneOfItsWords: the configured broker address
// carries a credential in its fragment, which no request sends and so no request record names,
// and whatever answers at that address refuses the trade with an OAuth error whose description
// repeats it. The trade's error prints the refusal's status and none of its words: post wraps the
// refusal in the address's Error (brokeraddr.Address.Error), which beside an address that is not
// clean withholds every word of the answer (R4). Captures brokerclient.refusal-beside-credential.
func TestARefusalBesideACredentialedAddressPrintsNoneOfItsWords(t *testing.T) {
	base := newRawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"the address ` + redacttest.Token + ` names no broker"}`))
	})
	client, err := New(Config{BrokerURL: brokeraddr.New(base + "#" + redacttest.Token), Key: testKey(t),
		Subject: &scriptedSource{token: "probe-subject", tokenType: "urn:envoy:params:oauth:token-type:kubernetes-service-account", describe: "probe subject"}})
	if err != nil {
		t.Fatal(err)
	}
	_, tradeErr := client.trade(context.Background())
	if tradeErr == nil {
		t.Fatal("the trade answered no error")
	}
	var refused *RefusedError
	if !errors.As(tradeErr, &refused) || !refused.HasCode("invalid_grant") {
		t.Errorf("the trade's error is %v, want the refusal with the code the answer gave", tradeErr)
	}
	redacttest.RequireNone(t, "brokerclient.refusal-beside-credential", tradeErr.Error())
	if !strings.Contains(tradeErr.Error(), "400") {
		t.Errorf("the trade's error is %q, want its status", tradeErr)
	}
}
