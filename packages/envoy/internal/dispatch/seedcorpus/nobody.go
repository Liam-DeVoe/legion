package seedcorpus

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// nobodysRunWords are the words of the run of the command bar whose author names nobody, which
// the self-check finds it by.
const nobodysRunWords = "Which run of the seeded issue's project did nobody ask?"

// writeNobodysRun writes one run of the command bar whose author names no credential - the
// shared bearer's empty author, {} - and its interaction node, which c-visibility's V7 reads as a
// run that reaches nobody. No route of this tree records such a run: the going POST refuses an
// asker that names nobody before it records anything (api's going_routes.go), and so did the
// release this tree lands after. A release before that refusal recorded one, and this build reads
// such a row all the same (api's goingRunIsTheCallers, graphstore.AuthorCredentialIsSQL). So the
// loader writes the going_requests row as that release wrote it, its second statement of its own
// beside nestReplies (decision 5 of dispatch://INFRA-1098's plan), and the node through the
// store's own writer of a run an earlier build recorded with no node (graphstore.WriteRun), which
// the frame path and envoy-dispatch backfill-graph-store call, in the same transaction. It records
// the run's id under model.interaction_nobody.
func (s *seeder) writeNobodysRun(ctx context.Context) error {
	var id string
	err := pgx.BeginFunc(ctx, s.database.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			insert into going_requests (author, project, words, ends_at)
			values ('{}'::jsonb, $1, $2, now() + interval '30 seconds')
			returning id::text
		`, s.project, nobodysRunWords).Scan(&id); err != nil {
			return fmt.Errorf("record the run whose author names nobody: %w", err)
		}
		written, err := graphstore.WriteRun(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("write the interaction node of the run %s whose author names nobody: %w", id, err)
		}
		if !written.Held || written.Nodes == 0 {
			return fmt.Errorf("the store's writer of a run wrote no interaction node of %s", id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.corpus.set("model.interaction_nobody", id)
	return nil
}
