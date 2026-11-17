package api

import (
	"context"
	"net/http"
)

// apiVersion is the version of the API a route is mounted under, which decides the name and the
// status each refusal code answers with (a Code of package apicode). The version reaches the error writers on
// the request's context, which every layer of a route hands on: a ResponseWriter cannot carry
// it, since the keyed layer's keyedAnswer hides the writer beneath it on purpose (request_keys.go).
type apiVersion int

const (
	apiV1 apiVersion = 1
	apiV2 apiVersion = 2
)

// apiVersionKey is the request-context key of the route's apiVersion.
type apiVersionKey struct{}

// withAPIVersion serves handler with version on the request's context. register wraps every
// route it mounts in it, outermost, so every layer of the route and its handler answer for one
// version.
func withAPIVersion(version apiVersion, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(context.WithValue(r.Context(), apiVersionKey{}, version)))
	}
}

// requestAPIVersion is the version the mount put on r, and apiV1 for a request that carries none:
// every route but a /api/v2 one answers as /api/v1 always has, the two websockets and a request a
// test drives at a handler directly among them. A nil r is the test hooks' fan-out, which another
// process's notification reaches with no request at all (runTestHook).
func requestAPIVersion(r *http.Request) apiVersion {
	if r == nil {
		return apiV1
	}
	if version, ok := r.Context().Value(apiVersionKey{}).(apiVersion); ok {
		return version
	}
	return apiV1
}
