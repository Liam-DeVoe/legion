package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sjawhar/envoy/internal/webhook"
)

// apiBodyLimit bounds the body of every /v1 route that reads one: 4 MiB, twice the largest request
// a caller sends. That request is a send or a publish (POST /v1/messages/send and
// /v1/messages/publish). Its envelope must fit the NATS max payload, the server's default of
// 1 MiB, which no deployment changes. The envelope carries the request's payload and only a
// one-line summary of its message (messageEnvelope), so a body whose message and payload each come
// near 1 MiB, about 2 MiB in all, is served. The bodies of the other routes are smaller. The
// largest registration (POST /v1/interests/subscribe) that the live listener held on 2026-11-16
// named 802 topics in about 35 KB, and a registration's topics and its entry's fields each go into
// one KV value under the same 1 MiB. A role claim is a few hundred bytes. A Legion agent batch is at
// most legionagents.MaxBatch entries of bounded fields, under 2 MiB.
const apiBodyLimit = 4 << 20

// apiBodyTooLarge is the error a /v1 route answers, with 413, to a body past apiBodyLimit: the
// webhook routes' text (webhook.BodyTooLarge), so every route names its limit in the same words.
var apiBodyTooLarge = webhook.BodyTooLarge(apiBodyLimit)

// readAPIBody decodes the JSON body of a /v1 request into into. A body past apiBodyLimit is
// answered 413 naming the limit, whatever it holds. A request whose Content-Length is past the
// limit is refused before any of its body is read. Any other body is read through
// http.MaxBytesReader, so no more than apiBodyLimit bytes of it are read and parsed. The decode
// stops after the first JSON value, so the rest of the body is then read through the same limit
// and dropped: a small value followed by more than the limit is refused as a body past it. The
// decode also stops at the first syntax error, before the limit is reached, so a body that does
// not decode is drained through the same limit too: one that runs past the limit is a body past
// it, and any other is answered 400 "invalid json". The status is 0 when the body decoded.
func readAPIBody(w http.ResponseWriter, r *http.Request, into any) (status int, message string) {
	if r.ContentLength > apiBodyLimit {
		return http.StatusRequestEntityTooLarge, apiBodyTooLarge
	}
	body := http.MaxBytesReader(w, r.Body, apiBodyLimit)
	err := json.NewDecoder(body).Decode(into)
	if err == nil {
		_, err = io.Copy(io.Discard, body)
	}
	var tooLarge *http.MaxBytesError
	if err != nil && !errors.As(err, &tooLarge) {
		if _, drained := io.Copy(io.Discard, body); !errors.As(drained, &tooLarge) {
			return http.StatusBadRequest, "invalid json"
		}
	}
	if tooLarge != nil {
		return http.StatusRequestEntityTooLarge, apiBodyTooLarge
	}
	return 0, ""
}

// decodeAPIBody is readAPIBody for a route that answers its refusal with writeJSONError. It
// reports whether the handler goes on.
func decodeAPIBody(w http.ResponseWriter, r *http.Request, into any) bool {
	status, message := readAPIBody(w, r, into)
	if status != 0 {
		writeJSONError(w, status, message)
		return false
	}
	return true
}
