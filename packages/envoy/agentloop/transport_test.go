package agentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

// TestRetryableBoundsTheWaitTheEndpointNames: the one retry of a model call reads the wait the
// endpoint names by the one rule every reader of a server's wait in this module keeps
// (internal/serverwait). A Retry-After of 9999999999 seconds and an X-RateLimit-Reset of
// math.MaxInt64 with no requests left are past what a Duration holds and read as one hour, which
// a run bounded under an hour, such as the judge's 20 s modelBound, has no time for, so the call
// is not made again (this test's run has two minutes): converted as it came, the count read as a
// negative wait, which any run had time for, and the call was made again at once. A negative
// Retry-After reads as no wait, an ordinary value of each header as itself, and an answer naming
// none waits defaultRetryWait. The test counts the rows it drove and fails under the number the
// table must hold.
func TestRetryableBoundsTheWaitTheEndpointNames(t *testing.T) {
	const rowsToDrive = 6
	now := time.Now()
	exhausted := func(reset string) http.Header {
		return http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset}}
	}
	rows := []struct {
		name     string
		status   int
		header   http.Header
		min, max time.Duration
		// again is whether a run with two minutes left waits the wait and makes the call again.
		again bool
	}{
		{"a 429 asking 9999999999 seconds", http.StatusTooManyRequests, http.Header{"Retry-After": {"9999999999"}}, time.Hour, time.Hour, false},
		{"a 429 whose X-RateLimit-Reset is math.MaxInt64 with no requests left", http.StatusTooManyRequests, exhausted(strconv.FormatInt(math.MaxInt64, 10)), time.Hour, time.Hour, false},
		{"a 429 asking a negative wait", http.StatusTooManyRequests, http.Header{"Retry-After": {"-30"}}, 0, 0, true},
		{"a 429 asking 30 seconds", http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, 30 * time.Second, 30 * time.Second, true},
		// The reset is whole seconds, and retryable reads it against its own clock a moment
		// later, so the wait is up to two seconds short of the 90 it names.
		{"a 503 whose X-RateLimit-Reset is 90 seconds out with no requests left", http.StatusServiceUnavailable, exhausted(strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)), 88 * time.Second, 90 * time.Second, true},
		{"a 503 naming no wait", http.StatusServiceUnavailable, http.Header{}, defaultRetryWait, defaultRetryWait, true},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	driven := 0
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			driven++
			wait, retry := retryable(&http.Response{StatusCode: row.status, Header: row.header}, nil)
			if !retry || wait < row.min || wait > row.max {
				t.Errorf("retryable = %s (retry %t), want %s to %s and a retry", wait, retry, row.min, row.max)
			}
			if again := waitable(ctx, wait); again != row.again {
				t.Errorf("a run with two minutes left makes the call again after %s: %t, want %t", wait, again, row.again)
			}
		})
	}
	if driven < rowsToDrive {
		t.Fatalf("the table drove %d rows, want %d", driven, rowsToDrive)
	}
}

// orderedFinal is a Final schema each of whose objects of more than one key is written out of
// sorted order: the root's keywords, its properties (kind, text, seen, needs), the kind
// property's keywords, and seen, a nested object whose keywords are written as the root's and
// whose own properties are when, by and at. orderedAnswer is a reply of that form.
const (
	orderedFinal = `{"type":"object","properties":{` +
		`"kind":{"type":"string","enum":["answered","not_answered"]},` +
		`"text":{"type":"string"},` +
		`"seen":{"type":"object","properties":{"when":{"type":"string"},"by":{"type":"string"},"at":{"type":"string"}},` +
		`"required":["when","by","at"],"additionalProperties":false},` +
		`"needs":{"type":"string"}},` +
		`"required":["kind","text","seen","needs"],"additionalProperties":false}`
	orderedAnswer = `{"kind":"answered","text":"plum","seen":{"when":"today","by":"the reader","at":"noon"},"needs":""}`
)

// TestTheFinalSchemaReachesTheProviderInTheOrderItIsWritten: on every protocol Run serves, each
// object of the schema the call's body carries holds its keys in the order Request.Final writes
// them, the nested object's included. Under structured output the model writes its reply's keys
// in the schema's order, and the SDK's Anthropic and Responses adapters rebuild the schema as a
// map, which Go writes with its keys sorted (doc.go, "The structured reply").
func TestTheFinalSchemaReachesTheProviderInTheOrderItIsWritten(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind  Kind
		reply reply
		// schema is the path of the schema in the protocol's request body.
		schema []string
	}{
		{kind: AnthropicMessages, schema: []string{"output_config", "format", "schema"},
			reply: anthropicStream("end_turn", anthropicUsage{Input: 1, Output: 1}, anthropicText(orderedAnswer))},
		{kind: AzureAnthropic, schema: []string{"output_config", "format", "schema"},
			reply: anthropicStream("end_turn", anthropicUsage{Input: 1, Output: 1}, anthropicText(orderedAnswer))},
		{kind: OpenAIResponses, schema: []string{"text", "format", "schema"},
			reply: openaiStream(openaiUsage{Input: 1, Output: 1}, "", openaiMessage("msg_1", orderedAnswer))},
		{kind: OpenAIChat, schema: []string{"response_format", "json_schema", "schema"},
			reply: chatStream("stop", chatUsage{Prompt: 1, Completion: 1}, orderedAnswer)},
	} {
		t.Run(string(c.kind), func(t *testing.T) {
			t.Parallel()
			s := newStandIn(t, c.reply)
			f := newFixture(t, s, c.kind, 1)
			f.req.Final = json.RawMessage(orderedFinal)
			if result, err := Run(context.Background(), f.req); err != nil || result.Fault != "" {
				t.Fatalf("result %+v, %v; want the answer and no fault", result, err)
			}
			got := s.requests()
			if len(got) != 1 {
				t.Fatalf("%d model calls, want 1", len(got))
			}
			schema := json.RawMessage(got[0].body)
			for _, key := range c.schema {
				var object map[string]json.RawMessage
				if err := json.Unmarshal(schema, &object); err != nil {
					t.Fatalf("the request body at %q: %v", key, err)
				}
				if schema = object[key]; schema == nil {
					t.Fatalf("the request body holds no %q on the path %v", key, c.schema)
				}
			}
			inFinalsOrder(t, schema, json.RawMessage(orderedFinal), "the schema")
		})
	}
}

// inFinalsOrder fails t where an object of sent, the schema a request body carries, does not
// begin with the keys of the same object of final, in final's order. It goes on into the values
// of each object whose keys are in that order and into each array's elements, so it fails once
// for the outermost object out of order on a path, and names that object by its path from at.
func inFinalsOrder(t *testing.T, sent, final json.RawMessage, at string) {
	t.Helper()
	switch bytes.TrimSpace(final)[0] {
	case '{':
		sentKeys, sentValues := objectMembers(t, sent)
		finalKeys, finalValues := objectMembers(t, final)
		if len(sentKeys) < len(finalKeys) || !slices.Equal(sentKeys[:len(finalKeys)], finalKeys) {
			t.Errorf("%s reaches the provider with its keys in the order %v, want them to begin with %v, Final's order",
				at, sentKeys, finalKeys)
			return
		}
		for i, key := range finalKeys {
			inFinalsOrder(t, sentValues[i], finalValues[i], at+"."+key)
		}
	case '[':
		var sentElements, finalElements []json.RawMessage
		if err := json.Unmarshal(sent, &sentElements); err != nil {
			t.Fatalf("%s: %v", at, err)
		}
		if err := json.Unmarshal(final, &finalElements); err != nil {
			t.Fatalf("%s in Final: %v", at, err)
		}
		if len(sentElements) != len(finalElements) {
			t.Errorf("%s reaches the provider with %d elements, want Final's %d", at, len(sentElements), len(finalElements))
			return
		}
		for i := range finalElements {
			inFinalsOrder(t, sentElements[i], finalElements[i], fmt.Sprintf("%s[%d]", at, i))
		}
	}
}

// objectMembers is the keys of the JSON object value in the order its bytes hold them, and each
// key's value.
func objectMembers(t *testing.T, value json.RawMessage) ([]string, []json.RawMessage) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(value))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("not an object: %s", value)
	}
	var keys []string
	var values []json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		var v json.RawMessage
		if err := decoder.Decode(&v); err != nil {
			t.Fatal(err)
		}
		keys, values = append(keys, token.(string)), append(values, v)
	}
	return keys, values
}
