package api

// v1_successor.go is how a /api/v1 handler hands on a value its successor's path needs and its own
// request does not carry (decision 9 of the plan of dispatch://INFRA-4071): the artifact a slug
// names, the issue a document belongs to. Rows 6, 17, 89, 91, 92, 161, 164 and 165 of the route
// map name a /api/v2 route whose path holds such a value (handlerResolvedSuccessors), and each of
// their handlers records it here once it has resolved it. The /api/v1 wrapper puts a holder on the
// request and fills the answer's Link header from it; where no holder is there, a recording is
// nothing, so a handler records whichever route it answers and its answer does not change.

import "net/http"

// successorValues is the holder a /api/v1 request carries for the values its handler resolves: a
// pointer on the request's context, so a layer that copies the request still writes into the one
// holder the wrapper reads.
type successorValues struct {
	values map[string]string
}

// successorValuesKey is the request-context key of the *successorValues.
type successorValuesKey struct{}

// successorValue records value as the successor's path value name - `id` for the artifact a slug
// names, `key` for the issue a document belongs to - in the holder r carries, and does nothing
// where r carries none.
func successorValue(r *http.Request, name, value string) {
	holder, held := r.Context().Value(successorValuesKey{}).(*successorValues)
	if !held {
		return
	}
	if holder.values == nil {
		holder.values = map[string]string{}
	}
	holder.values[name] = value
}
