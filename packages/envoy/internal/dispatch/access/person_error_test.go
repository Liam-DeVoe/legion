package access

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// failingQuerier answers every read with one failure, as a pool that lost its database does.
type failingQuerier struct{}

type failingRow struct{}

var errDatabaseGone = errors.New("the database is not answering")

func (failingRow) Scan(...any) error { return errDatabaseGone }

func (failingQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return failingRow{} }

func (failingQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errDatabaseGone
}

// The person these reads are asked about. Each part of the address is distinctive, so a part
// that reached the error is found whichever part it is.
const (
	hiddenLocal  = "zq.hidden.person"
	hiddenDomain = "zq-secret.example"
	hiddenEmail  = hiddenLocal + "@" + hiddenDomain
)

// namesThePerson reports the part of hiddenEmail that text holds, or "" when it holds none.
func namesThePerson(text string) string {
	for _, part := range []string{hiddenEmail, hiddenLocal, hiddenDomain, strings.ToUpper(hiddenLocal)} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(part)) {
			return part
		}
	}
	return ""
}

// TestAnAccessReadErrorNamesNoPerson: a read about one person that fails answers an error naming
// the read and the failure, and no part of the person's email. Every caller knows whom it asked,
// and the callers that log the error (the whoami warning, the API's failed-handler line) would
// otherwise write the address (CodeQL go/clear-text-logging, alert #143).
func TestAnAccessReadErrorNamesNoPerson(t *testing.T) {
	ctx := context.Background()
	reads := map[string]func() error{
		"MemberKind": func() error {
			_, err := MemberKind(ctx, failingQuerier{}, " "+strings.ToUpper(hiddenEmail)+" ")
			return err
		},
		"PersonAnswer": func() error {
			_, err := PersonAnswer(ctx, failingQuerier{}, hiddenEmail)
			return err
		},
		"Answer": func() error {
			_, err := Answer(ctx, failingQuerier{}, "ACC", hiddenEmail)
			return err
		},
		"Listed": func() error {
			_, err := Listed(ctx, failingQuerier{}, hiddenEmail)
			return err
		},
	}
	for name, read := range reads {
		err := read()
		if err == nil {
			t.Fatalf("%s over a database that does not answer = nil error, want the failure", name)
		}
		if !errors.Is(err, errDatabaseGone) {
			t.Errorf("%s = %q, want it to wrap the database's failure", name, err)
		}
		if part := namesThePerson(err.Error()); part != "" {
			t.Errorf("%s = %q, which names %q of the person it was asked about", name, err, part)
		}
	}
}
