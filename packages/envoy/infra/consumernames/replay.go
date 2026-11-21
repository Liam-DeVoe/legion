package consumernames

import (
	"context"
	"errors"
)

// replay is not built yet: SCAFFOLD, replaced by its builder before any push (hardening ledger).
func (r *run) replay(context.Context) error {
	return errors.New("envoy-infra consumer-names replay: not built yet")
}
