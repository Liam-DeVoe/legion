package graphcensus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// WriteRun is the backfill's writer for one run of the command bar: it states in the store, in
// the caller's transaction, the run a going_requests row holds where the store holds no
// interaction node of it, which a pod of the release before writes during a roll, recording a run
// with no node and appending its frames with no frame value. It writes, from the row and the run's
// going_events rows, exactly what the going POST and the frame path write of a run
// (graphstore.InteractionFacts, graphstore.FrameTuple): the interaction node, its words and its
// ends_at, its authored_by tuple with its author's own node where the row's author names somebody,
// and one frame value for each going_events row of the run, in sequence, in one graphstore.Write.
// A row whose author names nobody gets a node reserved for an author nobody proves, which nobody
// reads, as the stream reads that row.
//
// id names the run as going_requests.id reads it; the node is keyed by the row's own id::text, the
// lowercase form the POST writes. It answers whether the record holds the run: false where it holds
// no such row, and nothing is written then. A run whose node the store holds already is left as it
// is, answering true: its node and every frame written with it are another write's, and this
// writer mends nothing.
//
// It takes the run's going_requests row `for no key update` before it reads whether the node is
// there, so two writers of one run - two frames this build writes of a run an older pod recorded,
// or the backfill beside one - queue at that row, and the second, reading in a statement of its
// own once the first has committed, finds the node there and writes nothing. The lock conflicts
// with no insert of a frame: a going_events row's foreign key takes the run's row `for key share`.
// It is the step the frame path (api's appendGoingEvent) takes for a run with no node, before it
// takes the node's row.
func WriteRun(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var run graphstore.Interaction
	var author []byte
	err := tx.QueryRow(ctx, `
		select id::text, author, project, words, created_at, ends_at
		from going_requests
		where id = $1::uuid
		for no key update
	`, id).Scan(&run.ID, &author, &run.Project, &run.Words, &run.At, &run.EndsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read run %s: %w", id, err)
	}
	_, stored, err := graphstore.ReadNode(ctx, tx, graphstore.KindInteraction, run.ID)
	if err != nil {
		return false, err
	}
	if stored {
		return true, nil
	}
	if err := json.Unmarshal(author, &run.Author); err != nil {
		return false, fmt.Errorf("decode the author of run %s: %w", run.ID, err)
	}
	nodes, tuples := graphstore.InteractionFacts(run)
	rows, err := tx.Query(ctx, `
		select `+graphstore.FrameValueSQL("e.sequence", "e.name", "e.data")+`, e.created_at
		from going_events e
		where e.request_id = $1::uuid
		order by e.sequence
	`, run.ID)
	if err != nil {
		return false, fmt.Errorf("read the frames of run %s: %w", run.ID, err)
	}
	frames, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (graphstore.Tuple, error) {
		var value string
		var at time.Time
		if err := row.Scan(&value, &at); err != nil {
			return graphstore.Tuple{}, err
		}
		return graphstore.FrameTuple(run.ID, value, at), nil
	})
	if err != nil {
		return false, fmt.Errorf("read the frames of run %s: %w", run.ID, err)
	}
	if err := graphstore.Write(ctx, tx, nodes, append(tuples, frames...)); err != nil {
		return false, fmt.Errorf("write the interaction node of run %s: %w", run.ID, err)
	}
	return true, nil
}
