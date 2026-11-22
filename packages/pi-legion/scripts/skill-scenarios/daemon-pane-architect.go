package main

import (
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/daemon"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// init gives daemon-pane.go the daemon's own composition of what an architect is told after its
// addressing (architectAddressing). This file is the one part of the program that calls
// daemon.ArchitectAddressing, so daemon-pane.ts builds it in, as cmd/daemon-pane/architect.go
// beside daemon-pane.go, only inside a checkout whose daemon declares that function; the program
// still builds inside an older one, where it composes no architect's prompt.
func init() {
	architectAddressing = rigArchitectAddressing
}

// rigArchitectAddressing is what the architect of c is told after its addressing under the rig's
// deployment, which defines no role, lists no model and leaves `gates.design` unset
// (config.Gates.Steps), composed by the daemon's own function, as workerSpawnSpec composes it.
func rigArchitectAddressing(c supervise.Claim) string {
	return daemon.ArchitectAddressing(c, nil, nil, config.Gates{}.Steps())
}
