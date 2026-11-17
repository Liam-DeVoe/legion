package claim

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

// issueBranchPrefix begins the GitHub branch of every issue Legion works.
const issueBranchPrefix = "legion/"

// issueBranchShape is every name an issue's stored branch can have (record.Store.IssueBranch):
// the prefix and one path segment of letters, digits and hyphens. It holds both names the store
// keeps — legion/<16 lowercase hex> for an issue recorded from the release that stopped naming
// branches after their issue, and legion/<KEY> for an issue recorded before it, which the
// migration that made the table copied in as the branch that issue was already on. A branch of
// this shape is only possibly an issue's: which issue, if any, is the store's answer, never the
// name's. The narrow alphabet also keeps the name safe to quote into a jj revset.
var issueBranchShape = regexp.MustCompile(`^legion/[A-Za-z0-9][A-Za-z0-9-]*$`)

// newIssueBranchBytes is how many random bytes a new issue branch carries after its prefix, each
// written as two lowercase hex digits: 64 bits, so two issues of one deployment drawing the same
// name is not a case the store meets in practice, and its unique index refuses it if it does.
const newIssueBranchBytes = 8

// NewIssueBranch is a fresh name for an issue's branch, legion/<16 lowercase hex>, drawn from
// crypto/rand. It names no issue: this repository and many it serves are public, and a branch
// name is published with every pull request on it, so no Dispatch key goes into one. The name
// contains no hyphen, so no reader that looks for a key in a branch name, in any letter case,
// finds one in it. The store keeps the name it drew for the issue for good
// (record.Store.AllocateIssueBranch), and every reader of an issue's branch reads it there.
func NewIssueBranch() (string, error) {
	random := make([]byte, newIssueBranchBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("draw an issue branch name: %w", err)
	}
	return issueBranchPrefix + hex.EncodeToString(random), nil
}

// IsIssueBranch reports whether branch has the shape of an issue's stored branch
// (issueBranchShape). It is a cheap filter for what can only be no issue's — intake drops a pull
// request event on any other branch before it costs a transaction — and the check a value meets
// before the daemon quotes it into a command; it never says which issue a branch is.
func IsIssueBranch(branch string) bool { return issueBranchShape.MatchString(branch) }
