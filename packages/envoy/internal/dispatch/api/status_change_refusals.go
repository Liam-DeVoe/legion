package api

// status_change_refusals.go gives every refusal of POST /api/v1/issues/{key}/status-change the
// changed that the route's declared refusals require (StatusChangeRefusal and
// StatusChangeConflict in the OpenAPI document). The handler and the keyed layer write it
// themselves, since they know which steps committed (writeStatusChangeRefusal,
// refuseStatusChangeKey). Every layer register puts outside the keyed layer - the read-only and
// scope gates, the sign-in and Dispatch-Login checks, the project and session checks, a refused
// path parameter, the drain's 413 - refuses before any step runs, with the {code, error} it
// answers on every route. Under such a refusal nothing ran, so its changed is [] whichever layer
// refused, and one layer of this route alone adds it, rather than a branch for this route in each
// of those layers.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
)

// addChangedToRefusals serves the status-change route so that an error answer (a status from 400
// to 599) whose body is one JSON object with no changed member goes out with "changed":[] added
// as its last member; every other member keeps the bytes it was written with, and every header
// the layers inside set is kept but Content-Length, which is set for the new body. Any other
// answer goes out byte for byte as written: a 1xx, a 2xx and a 3xx, which stream through as they
// are written, a refusal that already carries changed (the handler's and the keyed layer's), and
// one whose body is not one JSON object. Only an error answer is held, from its status until the
// handler returns. register mounts it on this route alone (routeHandler), outside every layer of
// the route's chain and inside compression, so it reads the JSON each layer wrote.
func addChangedToRefusals(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		answer := &changedRefusalWriter{ResponseWriter: w}
		handler(answer, r)
		// Only a handler that returned has an answer to end, as compressAnswers has it: one that
		// panics leaves its connection to be dropped rather than a held refusal to be sent.
		answer.finish()
	}
}

// changedRefusalWriter is the route's writer under addChangedToRefusals: it holds an error
// answer's status and body until the handler returns, and passes every other answer straight
// through. It implements neither Flush nor Hijack: the route streams nothing and upgrades
// nothing.
type changedRefusalWriter struct {
	http.ResponseWriter
	// status is the held error answer's status, and 0 while nothing is held.
	status int
	// passed is true once an answer that is not held has begun: a status that is no error, or a
	// Write before any status, which net/http answers 200.
	passed bool
	// body is the held error answer's body.
	body bytes.Buffer
}

func (w *changedRefusalWriter) WriteHeader(status int) {
	switch {
	case w.status != 0:
		// A status after the held one is dropped, as net/http drops one after the answer's own.
		return
	case !w.passed && status >= http.StatusBadRequest && status < 600:
		w.status = status
		return
	case status >= http.StatusOK:
		// A status that is no error begins an answer this writer does not hold. An informational
		// one (100 Continue, 103 Early Hints) passes through as well, but it is not the answer,
		// and the answer after it may still be an error to hold.
		w.passed = true
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write holds the body of a held error answer and passes every other body on; a first Write
// without WriteHeader is a 200, as net/http's is.
func (w *changedRefusalWriter) Write(body []byte) (int, error) {
	if w.status != 0 {
		return w.body.Write(body)
	}
	w.passed = true
	return w.ResponseWriter.Write(body)
}

// Unwrap gives http.ResponseController the writer this one holds answers for, so the drain sets
// its read deadline on the connection through this wrapper (readFor, drainingWriter.Unwrap).
func (w *changedRefusalWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish writes the held error answer, if there is one: with changed added to its body when
// withEmptyChanged adds it, and as it was written otherwise.
func (w *changedRefusalWriter) finish() {
	if w.status == 0 {
		return
	}
	body := w.body.Bytes()
	if refusal, added := withEmptyChanged(body); added {
		body = refusal
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(body) > 0 {
		_, _ = w.ResponseWriter.Write(body)
	}
}

// withEmptyChanged answers body with "changed":[] written just inside its closing brace, and true,
// when body is one JSON object with no member named changed; every byte of body before that brace
// and after it is kept. Any other body answers false.
func withEmptyChanged(body []byte) ([]byte, bool) {
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil || members == nil {
		// Not one JSON object: another value, JSON null among them, or no JSON at all.
		return nil, false
	}
	if _, has := members["changed"]; has {
		return nil, false
	}
	member := `,"changed":[]`
	if len(members) == 0 {
		member = `"changed":[]`
	}
	// The object's closing brace is its last byte but JSON's whitespace after it, such as the
	// newline json.Encoder ends each value with.
	closing := len(bytes.TrimRight(body, " \t\r\n")) - 1
	return slices.Concat(body[:closing], []byte(member), body[closing:]), true
}
