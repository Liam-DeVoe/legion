package sessionanswers

import (
	"bytes"
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/benchtime"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestStatementTimings measures the agents' answers read (query) against main's (mainQuery) on
// the seeded measurement database (benchtime), which DISPATCH_BENCH_DATABASE_URL names. CI never
// sets that variable, so CI never runs a measurement.
//
// It is protocol 1: the two texts differ in SQL alone, and one schema runs both, since the schema
// keeps events_person_words_with_declines, which main's text reads, beside
// events_person_words_destinations, which the read reads. There is one pair for each of the three
// sessions benchtime binds by the rule the seed's report prints them by (answerswindow.Read): the
// session with old deliveries, and the sessions with the most and the fewest deliveries in the
// window. Each side is bound as List binds the read: the session, the start of the seed's answers
// window, read from the database by its one rule, and the session's topic.
//
// For each session, the two texts must answer the same rows, at least one, compared as
// TestTheAnswersReadAnswersMainsRows compares them (requireTheSameAnswerRows); the pair is timed
// (benchtime.Runner.Pair); and each side's shared buffers at its top plan node are read under the
// plan made for its values and under the generic plan, every count and both plans of each side
// are logged, and the read must read fewer than main's under each plan
// (benchtime.Runner.PairBuffers). Each of these is a subtest, so one that fails leaves the rest of
// the run to log its own numbers.
func TestStatementTimings(t *testing.T) {
	runner := benchtime.Open(t, os.Getenv(benchtime.DatabaseURLEnv))
	subjects := runner.Subjects()
	database := openMeasurementStore(t)
	for _, session := range subjects.AnswerSessions {
		name := "the answers read for " + session.Name
		arguments := []any{session.Session, subjects.AnswersSince, contracts.LegacyAgentSubject(session.Session)}
		mains := benchtime.Statement{SQL: mainQuery, Args: arguments}
		reads := benchtime.Statement{SQL: query, Args: arguments}
		t.Run(name+", its rows", func(t *testing.T) {
			requireTheSameAnswerRows(t, database, session, subjects.AnswersSince)
		})
		runner.Pair(t, name, mains, reads)
		t.Run(name+", its top node's buffers", func(t *testing.T) {
			runner.PairBuffers(t, name, mains, reads)
		})
	}
}

// requireTheSameAnswerRows holds query to mainQuery's rows for session since since, compared as
// requireMainsRows compares them over the fixture: the same columns, and the same rows in the
// same order, each column the same bytes the server sent. main's read must answer some row, so
// the comparison holds something and the pair beside it times a read that lists items.
func requireTheSameAnswerRows(t *testing.T, database *store.Store, session benchtime.AnswerSession, since time.Time) {
	t.Helper()
	want := readAnswerRows(t, database, mainQuery, session.Session, since)
	got := readAnswerRows(t, database, query, session.Session, since)
	t.Logf("%s, since %s: main's read answers %d rows, and the read %d",
		session, since.Format(time.RFC3339Nano), len(want.raw), len(got.raw))
	if len(want.raw) == 0 {
		t.Errorf("main's read answers no row for %s, so the comparison holds nothing", session)
	}
	if !slices.Equal(got.columns, want.columns) {
		t.Fatalf("the read answers the columns %v for %s, want main's %v", got.columns, session, want.columns)
	}
	if len(got.raw) != len(want.raw) {
		t.Fatalf("the read answers %d rows for %s, want main's %d:\n%s\nmain's:\n%s", len(got.raw), session, len(want.raw), got, want)
	}
	for row := range want.raw {
		for column := range want.raw[row] {
			if !bytes.Equal(got.raw[row][column], want.raw[row][column]) || (got.raw[row][column] == nil) != (want.raw[row][column] == nil) {
				t.Errorf("the read answers row %d's %s for %s as %v, want main's %v", row, want.columns[column], session,
					got.values[row][column], want.values[row][column])
			}
		}
	}
}

// openMeasurementStore opens a pool on the measurement database for the rows the two texts
// answer, which readAnswerRows reads through a store, as the oracle test reads them. The times and
// the buffers are read on the runner's one connection, not through this pool.
func openMeasurementStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), os.Getenv(benchtime.DatabaseURLEnv))
	if err != nil {
		t.Fatalf("open the measurement database: %v", err)
	}
	t.Cleanup(database.Pool.Close)
	return database
}
