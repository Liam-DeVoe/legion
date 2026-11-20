// Package textversionstest holds what the tests of more than one package write and read around
// the keyword table, search_text_versions, which the fill of package textversions writes from
// the event log: the project and the issue the tests' events name (SeedProject, SeedIssue), an
// issue's run of events of every type a keyword rule reads (WriteIssue), the rows the fill wrote
// for given events (RowsOf, RequireRowsOf, RequireOnlyRowsOf, AwaitRows) and the fill's cursor
// (Cursor). A later rules version or a change of the schema changes them here, once, for the
// tests of textversions, of the API and of the dispatch command.
//
// It is an ordinary package rather than a _test.go file because Go cannot share test-only code
// across packages; only tests import it. It does not import textversions, whose own tests import
// it.
package textversionstest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Project is the project SeedProject writes, which the issues of WriteIssue and of SeedIssue are
// in.
const Project = "ACME"

// Issue is the issue SeedIssue writes, ACME-1, on which the tests that write comment events alone
// write them. WriteIssue writes the issue of its number itself, so a test writes ACME-1 through
// one of the two.
const Issue = Project + "-1"

// SeedProject writes project ACME.
func SeedProject(t testing.TB, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(t.Context(),
		`insert into projects (key, name, default_assignee) values ($1, 'Acme', 'alice')`, Project); err != nil {
		t.Fatalf("seed the project: %v", err)
	}
}

// SeedIssue writes project ACME (SeedProject) and its issue ACME-1, Issue.
func SeedIssue(t testing.TB, database *store.Store) {
	t.Helper()
	SeedProject(t, database)
	if _, err := database.Pool.Exec(t.Context(), `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, $2, 1, 'Rotate the signing keys', '{"kind":"user","id":"alice"}', 'A')
	`, Issue, Project); err != nil {
		t.Fatalf("seed the issue: %v", err)
	}
}

// Written is an event a test wrote and how many keyword versions it starts.
type Written struct {
	ID       int64
	Versions int
}

// OneEach is the events ids, each starting one version, as the comment.created of a new comment
// does.
func OneEach(ids ...int64) []Written {
	written := make([]Written, len(ids))
	for index, id := range ids {
		written[index] = Written{ID: id, Versions: 1}
	}
	return written
}

// WriteIssue writes issue ACME-n of Project, which must be there (SeedProject), with its primary
// document at versions 1 and 2, and fourteen events of it in id order, in one transaction, the
// events appended through broker as the routes append them: one of every type a rule reads, each
// changing the text it reads, so each starts one version, and two that start none, an
// issue.updated that keeps the title and an issue.claimed, a type no rule reads. It answers each
// event with the versions it starts.
func WriteIssue(t testing.TB, database *store.Store, broker *events.Broker, n int) []Written {
	t.Helper()
	ctx := t.Context()
	key := fmt.Sprintf("%s-%d", Project, n)
	title := fmt.Sprintf("Rotate the signing keys of region %d", n)
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin issue %s: %v", key, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var artifact string
	if _, err := tx.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, $2, $3, $4, '{"kind":"user","id":"alice"}', 'A')
	`, key, Project, n, title); err != nil {
		t.Fatalf("insert issue %s: %v", key, err)
	}
	if err := tx.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ($1, $2, 'spec', 'spec.md', 'doc', true, '{"kind":"user","id":"alice"}')
		returning id::text
	`, key, Project).Scan(&artifact); err != nil {
		t.Fatalf("insert the primary document of %s: %v", key, err)
	}
	second := fmt.Sprintf("# Rotation %d\n\nMove each signing key of region %d to its successor, then audit the verifiers.", n, n)
	if _, err := tx.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 1, $2, '[]'), ($1::uuid, 2, $3, '[]')
	`, artifact, fmt.Sprintf("# Rotation %d\n\nMove each signing key to its successor.", n), second); err != nil {
		t.Fatalf("insert the versions of %s's primary document: %v", key, err)
	}

	actor := model.Actor{Kind: "user", ID: "alice"}
	issueKey := key
	comment, message, ask := uuid.NewString(), uuid.NewString(), uuid.NewString()
	issueEvent := func(eventType, title string) model.Event {
		return model.Event{IssueKey: &issueKey, Type: eventType, Actor: actor,
			Payload: map[string]any{"key": key, "title": title, "primary_artifact_id": artifact}}
	}
	post := func(eventType, id, body string) model.Event {
		return model.Event{IssueKey: &issueKey, Type: eventType, Actor: actor,
			Payload: map[string]any{"id": id, "issue_key": key, "body": body}}
	}
	askEvent := func(eventType, question string, answer *model.AskAnswer) model.Event {
		return model.Event{IssueKey: &issueKey, Type: eventType, Actor: actor, Payload: model.AskEventPayload{Ask: model.Ask{
			ID: ask, IssueKey: &issueKey, Author: actor, Kind: "question", Question: question, State: "open",
			Options: []model.AskOption{{Label: "Rotate now"}, {Label: "Wait a week"}}, Answer: answer,
		}}}
	}
	note := "Rotate them before the audit."
	steps := []struct {
		event    model.Event
		versions int
	}{
		{issueEvent("issue.created", title), 1},
		{model.Event{IssueKey: &issueKey, Type: "artifact.version", Actor: actor,
			Payload: map[string]any{"artifact_id": artifact, "version": map[string]any{"number": 2, "size": len(second)}}}, 1},
		{issueEvent("issue.updated", title+", then audit"), 1},
		{issueEvent("issue.updated", title+", then audit"), 0},
		{model.Event{IssueKey: &issueKey, Type: "issue.claimed", Actor: actor, Payload: map[string]any{"key": key}}, 0},
		{issueEvent("issue.closed", title+", audited"), 1},
		{post("comment.created", comment, "The verifiers read the old key until Monday."), 1},
		{post("comment.answered", uuid.NewString(), "Monday works; the audit starts Tuesday."), 1},
		{post("comment.edited", comment, "The verifiers read the old key until Tuesday."), 1},
		{post("message.created", message, "Can the rotation wait for the freeze to end?"), 1},
		{post("message.answered", uuid.NewString(), "It can; the freeze ends Friday."), 1},
		{askEvent("ask.opened", "Rotate the keys now?", nil), 1},
		{askEvent("ask.edited", "Rotate the signing keys now?", nil), 1},
		{askEvent("ask.answered", "Rotate the signing keys now?",
			&model.AskAnswer{User: "alice", Selected: []string{"Rotate now"}, Text: &note, At: time.Now()}), 1},
	}
	var out []Written
	if err := store.RunWrite(ctx, tx, func(batch *store.WriteBatch) error {
		for _, step := range steps {
			versions := step.versions
			if err := broker.QueueAppend(batch, step.event, func(appended model.Event) {
				out = append(out, Written{ID: appended.ID, Versions: versions})
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append the events of %s: %v", key, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit issue %s: %v", key, err)
	}
	return out
}
