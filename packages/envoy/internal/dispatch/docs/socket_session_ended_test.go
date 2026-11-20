package docs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// failingIdentity is an identity whose every read fails with err, as a read of a session the
// database ended under it does.
type failingIdentity struct{ err error }

func (f failingIdentity) Login(*http.Request) (string, error) { return "", f.err }

// A document socket whose read of the person's sign-in found its database session ended - by the
// standby that took the publisher hold over from this process, an operator or a database
// shutdown - answers 503 DATABASE_SESSION_ENDED with Retry-After: 1, as the API answers such a
// request, and not the 401 that reads as a refused sign-in. Any other failure of that read still
// answers 401.
func TestADocumentSocketWhoseSessionWasEndedAnswersDatabaseSessionEnded(t *testing.T) {
	database := storetest.Open(t)
	ended := fmt.Errorf("read the sign-in's generation: %w", &pgconn.PgError{Severity: "FATAL", Code: "57P01", Message: "terminating connection due to administrator command"})
	for _, row := range []struct {
		name       string
		err        error
		status     int
		retryAfter string
	}{
		{"the session ended under the sign-in's read", ended, http.StatusServiceUnavailable, "1"},
		{"another failure of the sign-in's read", errors.New("no session cookie"), http.StatusUnauthorized, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			service := New(Deps{
				Store:     database,
				Events:    events.NewBroker(),
				Identity:  failingIdentity{err: row.err},
				ServerURL: "https://dispatch.example",
				Settle:    time.Hour,
			})
			t.Cleanup(func() {
				if err := service.Shutdown(context.Background()); err != nil {
					t.Errorf("shutdown document service: %v", err)
				}
			})
			recorder := httptest.NewRecorder()
			service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ws/doc/"+uuid.NewString(), nil))
			body := recorder.Body.String()
			if recorder.Code != row.status || recorder.Header().Get("Retry-After") != row.retryAfter {
				t.Fatalf("the socket answered %d, Retry-After %q: %s; want %d, Retry-After %q",
					recorder.Code, recorder.Header().Get("Retry-After"), body, row.status, row.retryAfter)
			}
			if named := strings.Contains(body, store.SessionEndedCode); named != (row.status == http.StatusServiceUnavailable) {
				t.Errorf("the socket's answer names %s: %t: %s", store.SessionEndedCode, named, body)
			}
		})
	}
}
