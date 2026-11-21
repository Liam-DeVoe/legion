package consumernames

import (
	"context"
	"fmt"
)

// dispatch runs the run's action.
func (r *run) dispatch(ctx context.Context) error {
	switch r.o.action {
	case actionStatus:
		return r.status(ctx)
	case actionBackup:
		return r.backup(ctx)
	case actionPrepare:
		return r.prepareCutover(ctx)
	case actionCreateBucket:
		return r.createBucket(ctx)
	case actionReplay:
		return r.replay(ctx)
	case actionConvert:
		return r.convert(ctx)
	case actionCleanup:
		return r.cleanup(ctx)
	case actionRestore:
		return r.restore(ctx)
	case actionReplace:
		return r.replaceListener(ctx)
	case actionEnroll:
		return r.enroll(ctx)
	case actionRepair:
		return r.repairSentinel(ctx)
	}
	return fmt.Errorf("no action %q", r.o.action)
}
