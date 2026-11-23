package tmux

import (
	"context"
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

var _ runtime.SlotCredentials = (*Runtime)(nil)

// WriteSlotCredential satisfies runtime.SlotCredentials: the pane of token's claim, while it is
// watched, has its gh files written for role's App (writePaneCredential, the refresh's write),
// built by appauth.Bundles on the pane's claim's issue, so a role a plan defines holds the App of its
// type. Only the directories whose hosts.yml differs are rewritten. A runtime with no credential
// function wrote no pane any gh files, and a claim with no watched pane has none to write: its
// next spawn writes the App runtime.SlotRole answers then.
func (r *Runtime) WriteSlotCredential(ctx context.Context, token claim.Token, role claim.Role) error {
	if r.gitHubCredential == nil {
		return nil
	}
	bundles := appauth.NewBundles(r.gitHubCredential, r.controllerCredential, r.roleApps)
	for _, entry := range r.trackedProcesses() {
		if entry.locator.Claim != token {
			continue
		}
		changed, watched, bundle, err := r.writePaneCredential(ctx, bundles, entry, role)
		if err != nil {
			return fmt.Errorf("write the pane of %s for %s: %w", token, role, err)
		}
		if !watched {
			continue
		}
		if len(changed) > 0 {
			written := bundle.Dirs[bundle.Default]
			r.log.Info("tmux runtime: github credential written for a step", "claim", token, "role", role, "app", written.App,
				"expiresAt", written.ExpiresAt, "dirs", changed)
		}
		return nil
	}
	return nil
}

// SlotHoldsCredential satisfies runtime.SlotCredentials: nil, since a pane reads its gh files from
// the claim's own directory, which WriteSlotCredential writes before it answers, and no projection
// stands between the write and the pane.
func (r *Runtime) SlotHoldsCredential(context.Context, claim.Token, claim.Role) error {
	return nil
}
