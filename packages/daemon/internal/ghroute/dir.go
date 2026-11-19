package ghroute

import (
	"fmt"
	"os"

	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
)

// DirFor is the gh directory of the bundle under ownersDir that answers for the repository
// owner/name, the one pick every caller makes: `legion gh` routes every repository and owner a
// command names by it (TargetOf's dirFor), `legion git-credential` the repository a request's
// path names, and the daemon's gh tool every call of an answer over that answer's own bundle
// (internal/ghtool). It is the owner's own directory (ownerDir), except where a configured
// rename moved that one repository to another owner, which is the `to` owner's directory
// instead. name is the repository as the bundle keys one (ghconfig.RepositoryName) and is ""
// where the caller names an owner and no repository of it — a `gh` command's selector naming the
// owner alone, a credential request whose path carries one segment — which is the owner's own
// directory, the widest token the bundle holds for that account and never another's.
//
// The pairs are the bundle's own file, written by the daemon that wrote the directories
// (ghconfig.ReadPairs, ghconfig.PairsFile), and no other source is read. In a pod the file is a
// read-only projection of the role's Secret, which no agent can write. Under tmux the bundle's
// root is the pane user's own, so a pane can rewrite the file until the daemon's next refresh
// writes its own back (ghconfig.WriteBundle), and that hands out nothing new: a pair routes a
// request only to a directory of this bundle, and every one of those holds a lease of the
// role's own App, so a rewritten file reaches no token the pane does not already hold.
// The rule is ghconfig.RouteDir, which the daemon routes the bundle's own default by as well
// (appauth's bundleDirs), so the directory GH_CONFIG_DIR names for an issue is the one this
// answers for the issue's repository.
//
// A pair whose `to` owner the bundle holds no directory for is refused, naming the pair and the
// directory: no lease of this bundle reaches the repository that pair moved — the role's App is
// not installed on the owner holding it, or GitHub refused that lease — and the `from` owner's
// token, which is what an owner-only answer would hand out, cannot write it. Failing closed
// leaves git and gh saying so, where the other answer is a push that is refused at the remote.
// That refusal holds OwnerNotServedError's words inside a longer line, so the leak guard reads
// it as the setup failure it is and not as an owner the project does not serve.
func DirFor(ownersDir, owner, name string) (string, error) {
	if name == "" {
		return ownerDir(ownersDir, owner)
	}
	pairs, err := ghconfig.ReadPairs(ownersDir)
	if err != nil {
		return "", fmt.Errorf("read the gh bundle's configured renames: %w", err)
	}
	dir, pair, moved := ghconfig.RouteDir(pairs, owner, name)
	if !moved {
		return ownerDir(ownersDir, owner)
	}
	answer, err := ownerDir(ownersDir, dir)
	if err != nil {
		return "", fmt.Errorf("the configured rename %s -> %s serves %s from %s's gh directory, and %w; GitHub holds the repository under the name it moved to, so no other token of this bundle reaches it",
			pair.From, pair.To, owner+"/"+name, dir, err)
	}
	return answer, nil
}

// ownerDir is the gh directory of the bundle under ownersDir that answers for owner: `<owner>`,
// the one directory the bundle holds for that account, whose lease reaches every repository its
// installation holds (appauth.GitHubCredential). It is the answer for every repository of that
// owner but one a configured rename moved away from it, which its one caller routes by the
// repository instead (DirFor).
//
// An owner the bundle holds no directory for is refused: the role's App has no installation on
// owner, so no token of this bundle reaches anything of it, and an answer of nothing would have
// git prompt. The refusal is leakguard.OwnerNotServedError, which the leak guard reads, exactly,
// in `legion gh`'s refusal of its REST visibility read: it then reads that repository's
// visibility with no credential instead.
func ownerDir(ownersDir, owner string) (string, error) {
	dir := ghconfig.Dir(ownersDir, owner)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir, nil
	}
	return "", &leakguard.OwnerNotServedError{Owner: owner, OwnersDir: ownersDir}
}
