package daemon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// keptPodControllerSource is the processed-event source of the copies of workspace-kept notices
// that the outbox queues for the controller (tellControllerOfKeptPod). Its event id is the dedupe
// key the undelivered notice went under (record.Notice.Published: a re-held copy's is its original
// row's), so one notice queues one controller row however often its row runs and however many
// copies carry it.
const keptPodControllerSource = "workspace-kept-controller"

// tellControllerOfKeptPod hands a workspace-kept notice (record.WorkspaceKeptNotice) whose owning
// architect will not hold its role again, for why, to the project's controller (INFRA-341's
// reviewer's point 1 on R6 of dispatch://INFRA-3271). The daemon counts a kept pod told when it
// queues the notice (store's recordDecline), and keeps the pod until a read finds it clean, so the
// notice must not finish with nobody told: the controller gets a controller notice of its own with
// the notice's words (record.ControllerNotice, published on the controller topic by its own row,
// controllerNotice), queued once under key in a transaction that marks key processed, so a row
// whose finish did not commit runs again and queues nothing more. The undelivered row then
// finishes. It writes one line, once the transaction has committed, saying which.
func (r *outbox) tellControllerOfKeptPod(ctx context.Context, issue string, row int64, notice record.Notice, key string,
	architect claim.Token, why string) error {
	copied, err := record.NewOutboxRow(issue, record.ControllerNotice(notice), r.now().UTC())
	if err != nil {
		return fmt.Errorf("the controller's copy of the workspace-kept notice of %s is a row the outbox refuses: %w", issue, err)
	}
	args := []any{"row", row, "kind", notice.Kind, "issue", issue, "architect", architect, "because", why, "key", key}
	if err := store.Transact(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		fresh, err := r.records.MarkProcessed(ctx, tx, keptPodControllerSource, key)
		if err != nil {
			return err
		}
		store.OnCommit(ctx, func() {
			if !fresh {
				r.log.Info("outbox workspace-kept notice handed to the controller before under this key", args...)
				return
			}
			r.log.Info("outbox workspace-kept notice sent to the controller: its architect will not hold its role again", args...)
		})
		if !fresh {
			return nil
		}
		return r.records.Enqueue(ctx, tx, copied)
	}); err != nil {
		return fmt.Errorf("send the controller the workspace-kept notice of %s: %w", issue, err)
	}
	return nil
}
