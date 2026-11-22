package grapherase

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// ownerRows reads the owner row of every row the erase writes that another writer writes under an
// owner: the node's projection rows stand under their owner (nodeOwners), a block's own
// document - its versions and reviews, its search rows, its history, its windows, its excerpts -
// under that document's issue, or the document's own artifacts row where no issue owns it, and
// every document whose margin holds a comment's body under its own owner the same way, since the
// run replaces that document's history. The rest of what the erase writes is owed no owner row,
// since none of its other writers takes one: an event payload already written (the outbox's
// record of what it published takes no owner row), a tuple, which is appended once and never
// written again, the keyword versions, whose writer, the keyword fill, the run holds back by its
// cursor row instead (textversions.HoldFillBack), the term statistics, the display trees, and the
// node's own row. It reads no graph_nodes, graph_tuples or events row, so lockOwnersThenNode reads
// it before it holds anything. Beside the owners it answers the id of every document it read
// those through, every owner document among them, whose projects ownerProjects reads.
func (e *Erasure) ownerRows(ctx context.Context, tx pgx.Tx) (events.OwnerRows, []string, error) {
	owners, documents, err := nodeOwners(ctx, tx, e.Kind, e.ID)
	if err != nil {
		return nil, nil, err
	}
	document, _, err := e.ownDocument(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if document != nil {
		owners.Add(document.issue, document.artifact, "document "+document.refKey)
		documents = append(documents, document.artifact)
	}
	margins, err := e.marginDocuments(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	for _, margin := range margins {
		owners.Add(margin.issue, margin.artifact, "document "+margin.refKey)
		documents = append(documents, margin.artifact)
	}
	return owners, documents, nil
}

// ownerProjects reads, in key order, the projects whose term statistics totals row a confirmed
// run takes before its owner locks (api.TakeErasedTermTotalsTx): the project of each owner issue
// (issues.project_key) and of each document ownerRows read (artifacts.project_key), every owner
// document among them. Those are the projects of every item the run changes, which the term
// statistics count by the item's issue else its document.
//
// A document's own project is read beside its issue's because nothing in the schema holds the
// two equal. Every writer of this tree writes a document of an issue with its issue's project
// (api's storeArtifact and createIssue, cmd/dispatch-seed) and nothing moves either one
// (api/project_scope.go), but artifacts carries no constraint or trigger tying its project_key to
// its issue's, so a row written by any other path can name another project, and a comment on that
// document, or its search row, is counted in that project's statistics.
func (e *Erasure) ownerProjects(ctx context.Context, tx pgx.Tx, owners events.OwnerRows, documents []string) ([]string, error) {
	issues, _ := owners.Owners()
	rows, err := tx.Query(ctx, `
		select project_key from issues where key = any($1::text[])
		union
		select project_key from artifacts where id = any($2::uuid[]) and project_key is not null
		order by 1`, issues, documents)
	if err != nil {
		return nil, fmt.Errorf("read the projects of the owners of %s:%s: %w", e.Kind, e.ID, err)
	}
	projects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the projects of the owners of %s:%s: %w", e.Kind, e.ID, err)
	}
	return projects, nil
}

// nodeOwners reads the owner row each projection row of the node stands under: an issue's own
// comment stands under the issue, and every other row under the owner of the document it belongs
// to, which is that document's issue or, for a document no issue owns, the document's own
// artifacts row - the owner docs' lockArtifactOwner finds and events.LockOwnerRows takes. A node
// no row projects stands under none. Beside the owners it answers the id of each document a
// projection row belongs to.
func nodeOwners(ctx context.Context, tx pgx.Tx, kind, id string) (events.OwnerRows, []string, error) {
	arms := make([]string, len(graphstore.ProjectedTables))
	for index, held := range graphstore.ProjectedTables {
		issue := "null::text"
		if held.Issue != "" {
			issue = held.Issue
		}
		arms[index] = `select ` + issue + `, artifact_id from ` + held.Table + ` where node_kind = $1 and node_address = $2`
	}
	rows, err := tx.Query(ctx, `
		select coalesce(held.issue_key, a.issue_key, ''), coalesce(a.id::text, '')
		from (`+strings.Join(arms, " union all ")+`) as held (issue_key, artifact_id)
		left join artifacts a on a.id = held.artifact_id`, kind, id)
	if err != nil {
		return nil, nil, fmt.Errorf("read the owners of %s:%s: %w", kind, id, err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var owner [2]string
		err := row.Scan(&owner[0], &owner[1])
		return owner, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read the owners of %s:%s: %w", kind, id, err)
	}
	owners := events.OwnerRows{}
	var documents []string
	for _, owner := range held {
		owners.Add(owner[0], owner[1], kind+":"+id)
		if owner[1] != "" {
			documents = append(documents, owner[1])
		}
	}
	return owners, documents, nil
}
