// Package supervisetest is what a test outside supervise needs to stand in for a part of
// supervise.Store.
package supervisetest

import (
	"context"
	"sync"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// Holds is supervise.Store.HoldSessions as the store's claim_sessions table keeps it, in memory:
// the first claim of a project to record a name holds it, all of one call's names or none. The
// zero value holds nothing and is ready to use.
type Holds struct {
	mu    sync.Mutex
	holds map[holdKey]claim.Token
}

type holdKey struct {
	project string
	hold    supervise.Hold
}

// HoldSessions is supervise.Store.HoldSessions.
func (h *Holds) HoldSessions(_ context.Context, project string, token claim.Token, holds []supervise.Hold) ([]claim.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.holds == nil {
		h.holds = map[holdKey]claim.Token{}
	}
	holders := make([]claim.Token, len(holds))
	mine := true
	for i, hold := range holds {
		holder, held := h.holds[holdKey{project, hold}]
		if !held {
			holder = token
		}
		holders[i] = holder
		mine = mine && holder == token
	}
	if mine {
		for _, hold := range holds {
			h.holds[holdKey{project, hold}] = token
		}
	}
	return holders, nil
}
