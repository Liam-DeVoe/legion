package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// BenchmarkAnAdmittedRequest is admission's own cost for one request the server has room for:
// admitted around a handler that does nothing, on a request whose context is cancellable and
// carries the store's mark, as net/http's and trackTransactions make every request's.
func BenchmarkAnAdmittedRequest(b *testing.B) {
	handler := admitted(admission.New(admission.DefaultLimits()), 1, admission.NoLane, func(http.ResponseWriter, *http.Request) {})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil).WithContext(store.WithTransactionTracking(parent))
	answer := httptest.NewRecorder()
	b.ReportAllocs()
	for b.Loop() {
		handler(answer, request)
	}
}
