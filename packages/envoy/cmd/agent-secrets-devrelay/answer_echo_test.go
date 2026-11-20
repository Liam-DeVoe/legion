package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// TestAListedCallersRefusalPrintsNoBearerItsAnswerEchoes: an answer in the broker's place refuses
// a listed caller's call with an envelope whose code and words echo the bearer the call carried.
// The *apiError prints neither under Error, %v or %+v: apiClient.do reads the envelope through the
// record of the request it sent (redact.Carried), while the status still prints (the review of
// #4368 at 63bd188095, class A). Captures agent-secrets-devrelay.answer-echo/<case>.
func TestAListedCallersRefusalPrintsNoBearerItsAnswerEchoes(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"words", `{"code":"UNAUTHORIZED","error":"the bearer {auth} is not on the list"}`},
		{"code", `{"code":"{auth}","error":"not on the list"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, strings.ReplaceAll(c.body, "{auth}", r.Header.Get("Authorization")))
			}))
			defer server.Close()
			client := &apiClient{base: brokeraddr.New(server.URL), http: &http.Client{}, token: redacttest.Token}
			_, err := client.do(context.Background(), http.MethodPost, "/v1/machine-logins/lookup", map[string]any{"code": "ABCD-EFGH"})
			if err == nil {
				t.Fatal("the call answered no error")
			}
			capture := "agent-secrets-devrelay.answer-echo/" + c.name
			redacttest.RequireNone(t, capture, fmt.Sprintf("%s | %v | %+v", err.Error(), err, err))
			if !strings.Contains(err.Error(), "(HTTP 401)") {
				t.Errorf("the refusal is %q, want its status", err)
			}
		})
	}
}
