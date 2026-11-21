package consumernames

import (
	"context"
	"errors"
)

// cleanup is not built yet: SCAFFOLD, replaced by its builder before any push (hardening ledger).
func (r *run) cleanup(context.Context) error {
	return errors.New("envoy-infra consumer-names cleanup: not built yet")
}
