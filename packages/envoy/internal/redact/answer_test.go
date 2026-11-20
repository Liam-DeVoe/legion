package redact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// answerOf is the Answer of a GET of a server that answers status and the body answer makes of the
// request's Authorization, sent as a bearer of redacttest.Token with net/http's own client, as most
// clients of this repository send theirs.
func answerOf(t *testing.T, status int, answer func(auth string) string) redact.Answer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer(r.Header.Get("Authorization")))
	}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+redacttest.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	read, err := redact.ReadAnswer(resp, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// TestAnAnswersDecodeErrorQuotesNoValue: a 200 answer echoes the request's bearer into a member
// the client decodes as a time, as a number, or into nothing JSON at all. encoding/json's error,
// and time.Time's, quote the value; Decode's names the member and the destination's type and
// quotes none of it (the review of #4368 round 4, finding 1). Captures redact.answer-decode/<case>.
func TestAnAnswersDecodeErrorQuotesNoValue(t *testing.T) {
	type renewed struct {
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
		Seconds        int       `json:"seconds"`
	}
	for _, c := range []struct{ name, body, names string }{
		{"time", `{"lease_expires_at":"renewed with {auth}"}`, `"lease_expires_at"`},
		{"number", `{"seconds":"renewed with {auth}"}`, `"seconds"`},
		{"not-json", `renewed with {auth}`, "not JSON"},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer := answerOf(t, http.StatusOK, func(auth string) string { return strings.ReplaceAll(c.body, "{auth}", auth) })
			var into renewed
			err := answer.Decode(&into)
			if err == nil {
				t.Fatal("Decode took an answer that does not decode")
			}
			redacttest.RequireNone(t, "redact.answer-decode/"+c.name, fmt.Sprintf("%v | %+v | %#v", err, err, err))
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("Decode's error is %q, want it to name %s", err, c.names)
			}
			if errors.Unwrap(err) != nil {
				t.Errorf("Decode's error unwraps to %v, whose text quotes the value", errors.Unwrap(err))
			}
		})
	}
}

// TestAnAnswersWordsAndDecodedBoxesHoldItsRecord: a refusal that echoes the request's bearer. Its
// Words, and every RemoteText Decode fills - a member, one in a slice, one in a map, one behind a
// pointer - print it redacted, and compare as they came. Captures redact.answer-words and
// redact.answer-decoded/<member>.
func TestAnAnswersWordsAndDecodedBoxesHoldItsRecord(t *testing.T) {
	answer := answerOf(t, http.StatusBadGateway, func(auth string) string {
		return fmt.Sprintf(`{"error":"upstream refused %[1]s","list":["refused %[1]s"],"byKey":{"a":"refused %[1]s"},"ptr":"refused %[1]s"}`, auth)
	})
	if answer.Status() != http.StatusBadGateway {
		t.Errorf("Status = %d, want 502", answer.Status())
	}
	redacttest.RequireNone(t, "redact.answer-words", answer.Words().String())
	var envelope struct {
		Error redact.RemoteText            `json:"error"`
		List  []redact.RemoteText          `json:"list"`
		ByKey map[string]redact.RemoteText `json:"byKey"`
		Ptr   *redact.RemoteText           `json:"ptr"`
	}
	if err := answer.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Ptr == nil || len(envelope.List) != 1 || len(envelope.ByKey) != 1 {
		t.Fatalf("Decode filled %+v, want every member", envelope)
	}
	for member, box := range map[string]redact.RemoteText{"error": envelope.Error, "list": envelope.List[0], "byKey": envelope.ByKey["a"], "ptr": *envelope.Ptr} {
		redacttest.RequireNone(t, "redact.answer-decoded/"+member, box.String())
	}
	if !envelope.Error.Equal("upstream refused Bearer " + redacttest.Token) {
		t.Error("the decoded error does not compare as it came")
	}
}

// TestAnAnswerRefusesADestinationThatHoldsItsBodyWhole: a string, a byte slice, a json.RawMessage
// and an interface value would each hold the body as plain text, and Decode fills none of them.
func TestAnAnswerRefusesADestinationThatHoldsItsBodyWhole(t *testing.T) {
	answer := answerOf(t, http.StatusOK, func(auth string) string { return `"` + auth + `"` })
	var s string
	var b []byte
	var raw json.RawMessage
	var a any
	for name, v := range map[string]any{"string": &s, "bytes": &b, "raw": &raw, "any": &a} {
		if err := answer.Decode(v); err == nil {
			t.Errorf("Decode filled a %s with the body whole", name)
		}
	}
	redacttest.RequireNone(t, "redact.answer-whole", fmt.Sprint(s, string(b), string(raw), a))
}

// TestAnAnswerPrintsNothingOfItsBody: an Answer printed by every fmt verb, by encoding/json and by
// slog prints nothing of the body it holds. Captures redact.answer-printed.
func TestAnAnswerPrintsNothingOfItsBody(t *testing.T) {
	answer := answerOf(t, http.StatusOK, func(string) string { return redacttest.Words })
	var out bytes.Buffer
	for _, verb := range []string{"%s", "%q", "%v", "%+v", "%#v", "%x", "%X"} {
		fmt.Fprintf(&out, verb+"\n", answer)
	}
	if data, err := json.Marshal(struct{ A redact.Answer }{answer}); err == nil {
		out.Write(data)
	}
	slog.New(slog.NewTextHandler(&out, nil)).Info("answer", "answer", answer)
	slog.New(slog.NewJSONHandler(&out, nil)).Info("answer", "answer", answer)
	redacttest.RequireNone(t, "redact.answer-printed", out.String())
}

// TestReadAnswerCutsABodyAtItsLimit: a body longer than the limit is the Answer of its first limit
// bytes and an error that wraps ErrAnswerTooLarge; StreamTo writes those bytes, and a body within
// the limit whole.
func TestReadAnswerCutsABodyAtItsLimit(t *testing.T) {
	for _, c := range []struct {
		body  string
		limit int64
		want  string
		large bool
	}{{"0123456789", 4, "0123", true}, {"0123456789", 10, "0123456789", false}} {
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(c.body))}
		answer, err := redact.ReadAnswer(resp, c.limit)
		if errors.Is(err, redact.ErrAnswerTooLarge) != c.large || (!c.large && err != nil) {
			t.Errorf("ReadAnswer of %d bytes within %d: %v", len(c.body), c.limit, err)
		}
		var got bytes.Buffer
		if _, err := answer.StreamTo(&got); err != nil || got.String() != c.want {
			t.Errorf("StreamTo wrote %q, %v; want %q", got.String(), err, c.want)
		}
	}
}

// TestStreamAnswerStopsAtItsLimit: a body within the limit streams whole; one past it streams its
// first limit bytes and then fails with ErrAnswerTooLarge; closing the stream closes the body.
func TestStreamAnswerStopsAtItsLimit(t *testing.T) {
	for _, c := range []struct {
		body  string
		limit int64
		want  string
		large bool
	}{{"0123456789", 4, "0123", true}, {"0123456789", 10, "0123456789", false}} {
		closed := false
		body := closer{Reader: strings.NewReader(c.body), closed: &closed}
		stream := redact.StreamAnswer(&http.Response{StatusCode: http.StatusOK, Body: body}, c.limit)
		got, err := io.ReadAll(stream)
		if string(got) != c.want || errors.Is(err, redact.ErrAnswerTooLarge) != c.large || (!c.large && err != nil) {
			t.Errorf("StreamAnswer of %d bytes within %d read %q, %v", len(c.body), c.limit, got, err)
		}
		if err := stream.Close(); err != nil || !closed {
			t.Errorf("Close = %v, closed the body %t", err, closed)
		}
	}
}

type closer struct {
	io.Reader
	closed *bool
}

func (c closer) Close() error { *c.closed = true; return nil }
