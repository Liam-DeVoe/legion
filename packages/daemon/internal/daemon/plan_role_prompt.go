package daemon

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// ownersPromptText is what the daemon's copy of the prompt of a role of a plan holds: the prompt,
// between a line before it and a line after it that name it as the owner's, so the agent reads that
// text as the owner's, between Legion's headless mechanics before it and Legion's worker part after
// it, which hold where the two differ. Both lines carry a mark made from the prompt's own hash, so
// a prompt cannot hold the closing line of its own copy and end the block early: a text that held
// it would hash to another mark.
func ownersPromptText(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	mark := hex.EncodeToString(sum[:8])
	closing := "End of the role prompt the issue's owner wrote (" + mark + ")."
	return "The text from here to the line \"" + closing + "\" is the prompt of your role, as the architect that owns this issue " +
		"wrote it in its plan. Where it and Legion's own instructions differ, Legion's instructions hold.\n\n" + prompt + "\n\n" + closing + "\n"
}

// planRolePromptFile is the name, after the claim's token and a "-", of the daemon's copy of the
// prompt of a role an owner's plan defines (planRolePromptName).
const planRolePromptFile = "role-prompt.md"

// planRolePromptName is token's copy of the prompt of its role, a role an owner's plan defines:
// `<token>-role-prompt.md` in the secrets directory, one of the files a launch writes for its claim
// (ownedBy), so it goes with the claim's other files once the claim's process ends (pruning,
// pruneAllBut).
func planRolePromptName(token claim.Token) string { return string(token) + "-" + planRolePromptFile }

// writePlanRolePrompt writes prompt, the role's text in the plan in force of the claim's issue, as
// token's copy (planRolePromptName), between the lines that name it as the owner's
// (ownersPromptText), as the claim's system-prompt file is written (runtime.WriteSecretFile: 0600
// in the 0700 secrets directory, replaced whole), and answers its path. A launch reads a role's
// prompt from a file (runtime.PromptParts.RolePromptPaths). Every launch of the claim writes its
// copy afresh from the stored plan before it composes its prompt, so no launch reads a copy an
// earlier launch left.
func writePlanRolePrompt(stateDir string, token claim.Token, prompt string) (string, error) {
	return runtime.WriteSecretFile(stateDir, planRolePromptName(token), "role prompt", ownersPromptText(prompt))
}
