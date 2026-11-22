package seedcorpus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestTheSeedLeavesEveryEventItDidNotWriteAsItWas plants another writer's work in a database of
// its own, through the API as a server writes it: a project, an issue whose events it then marks
// published, and one comment whose event stays unpublished, the one unpublished event the
// database holds. A seed of that database is refused, naming how many events it holds; a seed
// that says the database is a test copy writes the corpus and leaves every event that stood
// before it exactly as it was, the unpublished one still unpublished, and marks none of its own
// events published either, since publishing them is the server's.
func TestTheSeedLeavesEveryEventItDidNotWriteAsItWas(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	fixtures, err := FindFixtures(".")
	if err != nil {
		t.Fatal(err)
	}
	server, err := startAPI(ctx, database, []string{alice})
	if err != nil {
		t.Fatal(err)
	}
	plant := &seeder{api: server, database: database, project: "OTHER"}
	var issue model.Issue
	planted := plant.ensureProject(ctx)
	if planted == nil {
		planted = server.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues", map[string]any{
			"project": "OTHER", "title": "Another writer's issue", "spec": "Somebody else's work.\n", "force": true,
		}, &issue, http.StatusCreated)
	}
	if planted == nil {
		_, planted = database.Pool.Exec(ctx, `update events set published_at = now() where published_at is null`)
	}
	if planted == nil {
		_, planted = plant.comment(ctx, asPerson(alice), issue.Key, "A comment whose event nobody has published.", "")
	}
	if err := errors.Join(planted, server.close(ctx)); err != nil {
		t.Fatalf("plant another writer's work: %v", err)
	}
	before := eventRows(t, database.Pool)
	var unpublished []string
	for id, row := range before {
		if strings.Contains(row, `"published_at": null`) {
			unpublished = append(unpublished, id)
		}
	}
	if len(before) == 0 || len(unpublished) != 1 {
		t.Fatalf("the plant left %d events, %d of them unpublished, want one unpublished: %v", len(before), len(unpublished), before)
	}

	if _, err := Seed(ctx, database, Options{Fixtures: fixtures}); !errors.Is(err, ErrForeignEvents) ||
		!strings.Contains(err.Error(), fmt.Sprintf("it holds %d events", len(before))) {
		t.Fatalf("a seed of a database holding %d events answered %v, want ErrForeignEvents naming them", len(before), err)
	}
	if _, err := Seed(ctx, database, Options{Fixtures: fixtures, TestCopy: true}); err != nil {
		t.Fatalf("a seed of the test copy: %v", err)
	}
	after := eventRows(t, database.Pool)
	for id, row := range before {
		if after[id] != row {
			t.Errorf("the seed changed event %s, which it did not write:\n  before %s\n  after  %s", id, row, after[id])
		}
	}
	own, published := 0, 0
	for id, row := range after {
		if _, stood := before[id]; stood {
			continue
		}
		own++
		if !strings.Contains(row, `"published_at": null`) {
			published++
			t.Errorf("the seed marked its own event %s published: %s", id, row)
		}
	}
	t.Logf("%d events stood before the seed, %d unpublished (%s); the seed wrote %d events and marked %d published",
		len(before), len(unpublished), unpublished[0], own, published)
	if own == 0 {
		t.Errorf("the seed wrote no event, so the read of its own events read nothing")
	}
}

// eventRows is every event of the database as its whole row reads, as jsonb text, by its id.
func eventRows(t *testing.T, q graphstore.Queryer) map[string]string {
	t.Helper()
	rows, err := q.Query(context.Background(), `select e.id::text, to_jsonb(e)::text from events e`)
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	defer rows.Close()
	read := map[string]string{}
	for rows.Next() {
		var id, row string
		if err := rows.Scan(&id, &row); err != nil {
			t.Fatalf("read an event: %v", err)
		}
		read[id] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the events: %v", err)
	}
	return read
}
