package consumernames

import (
	"context"
	"errors"
)

// status is not built yet: SCAFFOLD, replaced by its builder before any push (hardening ledger).
func (r *run) status(context.Context) error {
	return errors.New("envoy-infra consumer-names status: not built yet")
}
