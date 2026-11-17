package grapherase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// An issue, a document, an ask and a message each hold their text in their own row: the issue's
// title, the document's name and versions, the ask's question, the message's body. The erase
// reads text only from the comments and the document blocks that derive from a node, so a run
// over one of these nodes would delete the node and its tuples, record an erasure and answer
// applied, and leave the row and its text where they were; the project census would then read the
// row as missing a node, and the backfill would write the node again. The erase refuses each of
// the four kinds before it opens a connection, as it refuses a role, a page and a session: run
// as an operator runs it, without --confirm and then with a digest, each run answers the refusal
// even given a database URL nothing listens on, and over the real database the node, its tuples
// and its row with its text are all still there, with no erasure recorded.
//
// Its control is the refusal taken out of Erase: the dry run then lists the node, the confirmed
// run over that listing answers applied, and the row's text is still in place.
func TestTheEraseRefusesAnIssueADocumentAnAskAndAMessageAndLeavesThemWhole(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedObjectNodes(t, database)

	const refusal = "text is in its own row, which the erase does not reach"
	for _, node := range []struct{ kind, id, table, column, key string }{
		{graphstore.KindIssue, seeded.issue, "issues", "title", "key"},
		{graphstore.KindArtifact, seeded.documentRef, "artifacts", "name", "ref_key"},
		{graphstore.KindAsk, seeded.ask, "asks", "question", "id::text"},
		{graphstore.KindMessage, seeded.message, "messages", "body", "id::text"},
	} {
		t.Run(node.kind, func(t *testing.T) {
			before := readTypeNode(t, database, node.kind, node.id)
			if !before.row || before.out == 0 {
				t.Fatalf("the seed holds %s:%s as %+v, want a node with tuples, so this test would prove nothing", node.kind, node.id, before)
			}
			text := rowText(t, database, node.table, node.column, node.key, node.id)
			for _, url := range []string{"postgres://nobody@127.0.0.1:1/nothing?connect_timeout=1", database.Pool.Config().ConnString()} {
				listing, err := Erase(ctx, url, node.kind, node.id, "", 0)
				if err == nil && listing != nil {
					// The refusal is not there: run the confirmed erase an operator runs next,
					// so the failure says what that erase does to the row.
					confirmed, confirmErr := Erase(ctx, url, node.kind, node.id, listing.Digest, 0)
					t.Fatalf("erase --node %s:%s listed it and its confirmed run answered applied %t (err %v); the %s row still reads %q",
						node.kind, node.id, confirmed != nil && confirmed.Applied, confirmErr, node.table,
						rowText(t, database, node.table, node.column, node.key, node.id))
				}
				for _, confirm := range []string{"", "any digest"} {
					erasure, err := Erase(ctx, url, node.kind, node.id, confirm, 0)
					if err == nil || !strings.Contains(err.Error(), "refuses "+node.kind+":"+node.id+": ") || !strings.Contains(err.Error(), refusal) {
						t.Errorf("erase --node %s:%s (confirm %q) over %s answered %v (applied %t), want the refusal %q before any connection",
							node.kind, node.id, confirm, url, err, erasure != nil && erasure.Applied, refusal)
					}
				}
			}
			if after := readTypeNode(t, database, node.kind, node.id); after != before {
				t.Errorf("erase --node %s:%s left the store holding %+v of it, want %+v as before the runs", node.kind, node.id, after, before)
			}
			if after := rowText(t, database, node.table, node.column, node.key, node.id); after != text {
				t.Errorf("erase --node %s:%s left its %s row reading %q, want %q as before the runs", node.kind, node.id, node.table, after, text)
			}
		})
	}
}

// objectNodes is what seedObjectNodes wrote: an issue, its document, an ask on the issue and a
// message on it, each a row and its node in the project.
type objectNodes struct {
	issue, documentRef, ask, message string
}

// seedObjectNodes writes project ERASE, issue ERASE-1 with a document, an ask and a message, each
// row with its node as its create writes it (graphstore.IssueNode and its siblings) and the base
// attributes that node write states.
func seedObjectNodes(t *testing.T, database *store.Store) objectNodes {
	t.Helper()
	ctx := context.Background()
	seeded := objectNodes{
		issue: "ERASE-1", documentRef: "ERASE-1/notes-md",
		ask: "22222222-2222-4222-8222-000000000001", message: "22222222-2222-4222-8222-000000000002",
	}
	now := time.Now().UTC()
	alice := model.Actor{Kind: "user", ID: "alice"}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the seed: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := graphstore.Write(ctx, tx, []graphstore.Node{
		graphstore.IssueNode(seeded.issue, "ERASE", alice, now),
		graphstore.ArtifactNode(seeded.documentRef, "ERASE", "", alice, now),
		graphstore.AskNode(seeded.ask, "ERASE", nil, alice, now),
		graphstore.MessageNode(seeded.message, "ERASE", nil, alice, now),
	}, nil); err != nil {
		t.Fatalf("write the nodes: %v", err)
	}
	for _, statement := range []string{
		`insert into projects (key, name, default_assignee) values ('ERASE', 'Erase', 'alice')`,
		`insert into issues (key, project_key, number, title, created_by, rank)
		 values ('ERASE-1', 'ERASE', 1, 'A title somebody wrote', '{"kind":"user","id":"alice"}', 'U')`,
		`insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
		 values ('ERASE-1', 'ERASE', 'notes-md', 'Notes somebody named', 'doc', '{"kind":"user","id":"alice"}')`,
		`insert into asks (id, issue_key, author, question, options, multiple, urgency, kind)
		 values ('` + seeded.ask + `', 'ERASE-1', '{"kind":"user","id":"alice"}', 'A question somebody asked', '[]', false, 'med', 'question')`,
		`insert into messages (id, issue_key, author, body)
		 values ('` + seeded.message + `', 'ERASE-1', '{"kind":"user","id":"alice"}', 'A message somebody wrote')`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("seed the rows: %v\n%s", err, statement)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the seed: %v", err)
	}
	return seeded
}

// rowText reads column of the table's row whose key reads id.
func rowText(t *testing.T, database *store.Store, table, column, key, id string) string {
	t.Helper()
	var text string
	if err := database.Pool.QueryRow(context.Background(),
		`select `+column+` from `+table+` where `+key+` = $1`, id).Scan(&text); err != nil {
		t.Fatalf("read %s.%s of %s: %v", table, column, id, err)
	}
	return text
}
