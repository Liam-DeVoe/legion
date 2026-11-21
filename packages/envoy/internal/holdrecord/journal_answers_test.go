package holdrecord_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// canary is a record's value as an answer might carry it, and overflow a number no field of the
// client holds: neither may reach an error.
const (
	canary   = "RECORD_VALUE_CANARY"
	overflow = "1844674407370955161600123456789"
)

// answer answers a request itself with status and body, as a broker or a proxy before it might.
func answer(status int, body string) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, _ *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return true
	}
}

func recordsRead(req *http.Request) bool {
	return req.Method == http.MethodGet && req.URL.Path == "/v1/nats-hold/records"
}

func rowRead(req *http.Request) bool {
	return req.Method == http.MethodGet && req.URL.Path == "/v1/nats-hold"
}

// answerRows are TestTheHoldJournal's rows of the answers the client cannot use: no error quotes
// an answer's body or a record's value. A refusal names the request's method and route, the
// answer's status and a code the client knows; an answer that does not decode names the field and
// the error's class. An error is printed and logged, and a body the client cannot read may hold
// anything, a record's value among it. Each fails while an error copies the body, the code it
// does not know, json's decoder error or the row's signed_at into its text.
func answerRows(t *testing.T) {
	ctx := context.Background()

	t.Run("a refusal names its method, route, status and a known code, and quotes nothing of its body", func(t *testing.T) {
		r := newRig(t)
		c := r.client(t)
		for _, x := range []struct {
			name   string
			status int
			body   string
			code   string // the RouteError's code, or "" for an unrecognized body
		}{
			{"a body holding a record value", http.StatusInternalServerError, `{"records":[{"value":"` + canary + `"}]}`, ""},
			{"a body that is not JSON", http.StatusBadGateway, canary, ""},
			{"a hold refusal coded with a code the client does not know", http.StatusConflict, `{"error":"` + canary + `","message":"x"}`, ""},
			{"a broker error coded with a code the client does not know", http.StatusServiceUnavailable, `{"code":"` + canary + `","error":"x"}`, ""},
			{"a hold refusal whose message holds a record value", http.StatusConflict, `{"error":"gen_mismatch","message":"` + canary + `"}`, "gen_mismatch"},
			{"a broker error whose text holds a record value", http.StatusServiceUnavailable, `{"code":"DATABASE_UNAVAILABLE","error":"` + canary + `"}`, "DATABASE_UNAVAILABLE"},
		} {
			r.once(recordsRead, answer(x.status, x.body))
			_, err := c.Records(ctx, "h-answers", "done")
			if err == nil {
				t.Fatalf("%s: Records answered no error", x.name)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("%s: the error quotes the body: %v", x.name, err)
			}
			if want := "GET /v1/nats-hold/records answered " + strconv.Itoa(x.status); !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error %q does not name %q", x.name, err, want)
			}
			var route *holdrecord.RouteError
			switch coded := errors.As(err, &route); {
			case x.code == "" && (coded || !strings.Contains(err.Error(), "an unrecognized body")):
				t.Errorf("%s: answered %v, want a plain error naming an unrecognized body", x.name, err)
			case x.code != "" && (!coded || route.Code != x.code || route.Status != x.status):
				t.Errorf("%s: answered %v, want a RouteError %d %s", x.name, err, x.status, x.code)
			}
		}
	})

	t.Run("an answer that does not decode names its field and class, and quotes neither its number nor its text", func(t *testing.T) {
		r := newRig(t)
		c := r.client(t)
		r.once(recordsRead, answer(http.StatusOK, `{"records":[{"key":"hold","value":{},"gen":`+overflow+`,"sig":"x"}]}`))
		_, err := c.Records(ctx, "h-answers", "done")
		if err == nil || strings.Contains(err.Error(), overflow) || !strings.Contains(err.Error(), "its field records.0.gen does not fit uint64 (a type error)") {
			t.Errorf("a list whose gen overflows answered %v, want an error naming records.0.gen and a type error, without the number", err)
		}
		r.once(recordsRead, answer(http.StatusOK, canary))
		_, err = c.Records(ctx, "h-answers", "done")
		if err == nil || strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "(a syntax error)") {
			t.Errorf("a list that is not JSON answered %v, want an error naming a syntax error, without the body", err)
		}
		r.once(rowRead, answer(http.StatusOK, `{"gen":1,"phase":"held","hold_id":"h-answers","signed_at":"`+canary+`","sig":"x"}`))
		_, err = c.Row(ctx)
		if !errors.Is(err, holdrecord.ErrUnknown) || strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "signed_at") {
			t.Errorf("a row whose signed_at is not a time answered %v, want ErrUnknown naming signed_at, without its value", err)
		}
	})

	t.Run("a signed journal whose hold_gen overflows does not decode, and no error quotes the number", func(t *testing.T) {
		// The record is the operator's own, signed and stored through the routes: it verifies, and
		// only its decoding refuses it.
		r := newRig(t)
		c := r.client(t)
		hold(t, c, "h-overflow", holdrecord.WorkflowCutover, nil)
		_, gen := journal(t, c, "h-overflow")
		value := json.RawMessage(`{"workflow":"cutover","ops":[],"complete":false,"hold_gen":` + overflow + `}`)
		if _, err := c.PutRecord(ctx, "h-overflow", "done", "hold", gen, value); err != nil {
			t.Fatal(err)
		}
		const want = "hold h-overflow's done record does not decode: its field hold_gen does not fit uint64 (a type error)"
		_, _, err := c.Journal(ctx, "h-overflow")
		if err == nil || strings.Contains(err.Error(), overflow) || !strings.Contains(err.Error(), want) {
			t.Errorf("Journal answered %v, want an error naming %q, without the number", err, want)
		}
		_, err = c.Admit(ctx, "h-overflow", cmd("create-bucket"), nil)
		if err == nil || strings.Contains(err.Error(), overflow) || !strings.Contains(err.Error(), want) {
			t.Errorf("Admit answered %v, want an error naming %q, without the number", err, want)
		}
	})
}
