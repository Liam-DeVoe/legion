package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghroute"
)

const gitCredentialUsage = "usage: legion git-credential get|store|erase  (git's credential helper for a shared clone; git runs it, never a person)"

// gitCredentialOperationUsage is each operation's own usage, which its --help prints.
var gitCredentialOperationUsage = map[string]string{
	"get":   "usage: legion git-credential get  (reads git's request on stdin; answers the token of the owner its path names, or nothing, exit 1)",
	"store": "usage: legion git-credential store  (reads git's request on stdin and writes nothing: the daemon is the one writer of the gh files)",
	"erase": "usage: legion git-credential erase  (reads git's request on stdin and erases nothing: the daemon is the one writer of the gh files)",
}

// gitCredentialStdin is where `legion git-credential` reads git's credential request: the
// process's stdin, which a test replaces.
var gitCredentialStdin io.Reader = os.Stdin

// runGitCredential is every shared clone's credential helper (workspace.GitHubCredentialHelper),
// which git runs with its operation and the request on stdin. `get` answers a request for
// https://github.com with the installation-token username and the token of the gh directory that
// answers for the repository the request's path names (credential.useHttpPath, which every
// shared clone sets): its first two segments are that repository, and the directory is the one
// the bundle under LEGION_GH_OWNERS_DIR holds for it (ghroute.DirFor) and nothing else. A role's
// bundle holds one directory per owner the role's App is installed on, and the lease in it
// reaches every repository that installation holds, so the owner's own directory answers for
// every repository of that owner but one a configured rename moved to another owner: that one is
// answered from the directory of the owner it moved to, which the bundle's pairs file names, and
// the path's second segment is what tells the two apart. Any other host or
// protocol, a request with no path, a path whose owner is no GitHub login or one the bundle
// holds no directory for — an owner the role's App is not installed on — a pair whose `to`
// owner has no directory, an unset
// LEGION_GH_OWNERS_DIR, and a hosts.yml with no token answer nothing, exit 1, naming why on
// stderr, and git, which every shared clone tells credential.interactive=false, then fails
// rather than prompts. `store` and `erase` change nothing: the daemon is the one writer of the
// gh files.
func runGitCredential(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && helpRequested(args[0]) {
		fmt.Fprintln(stderr, gitCredentialUsage)
		return 0
	}
	if len(args) == 2 && helpRequested(args[1]) {
		if usage, ok := gitCredentialOperationUsage[args[0]]; ok {
			fmt.Fprintln(stderr, usage)
			return 0
		}
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, gitCredentialUsage)
		return 2
	}
	switch args[0] {
	case "store", "erase":
		_, _ = io.Copy(io.Discard, gitCredentialStdin)
		return 0
	case "get":
	default:
		fmt.Fprintln(stderr, gitCredentialUsage)
		return 2
	}
	request := readCredentialRequest(gitCredentialStdin)
	if request["protocol"] != "https" {
		fmt.Fprintf(stderr, "legion git-credential: protocol %q is not https; answering nothing\n", request["protocol"])
		return 1
	}
	if request["host"] != "github.com" {
		fmt.Fprintf(stderr, "legion git-credential: host %q is not github.com; answering nothing\n", request["host"])
		return 1
	}
	ownersDir := os.Getenv("LEGION_GH_OWNERS_DIR")
	if ownersDir == "" {
		fmt.Fprintln(stderr, "legion git-credential: LEGION_GH_OWNERS_DIR is unset; answering nothing")
		return 1
	}
	if request["path"] == "" {
		fmt.Fprintln(stderr, "legion git-credential: git sent no path (credential.useHttpPath is off), so the request names no owner; answering nothing")
		return 1
	}
	path := strings.TrimPrefix(request["path"], "/")
	segment, rest, _ := strings.Cut(path, "/")
	owner := strings.ToLower(segment)
	if !ghconfig.IsOwner(owner) {
		fmt.Fprintf(stderr, "legion git-credential: the path %s names the owner %q, which is no GitHub login; answering nothing\n", request["path"], segment)
		return 1
	}
	// The second segment is the repository, which is what tells a repository a configured rename
	// moved from every other repository of its owner; a path with none names the owner alone.
	name, _, _ := strings.Cut(rest, "/")
	dir, err := ghroute.DirFor(ownersDir, owner, ghconfig.RepositoryName(name))
	if err != nil {
		fmt.Fprintf(stderr, "legion git-credential: %v; answering nothing\n", err)
		return 1
	}
	hosts, err := ghconfig.ReadRegular(filepath.Join(dir, ghconfig.HostsFile))
	if err != nil {
		fmt.Fprintf(stderr, "legion git-credential: %v; answering nothing\n", err)
		return 1
	}
	token, err := ghconfig.TokenFromHosts(hosts)
	if err != nil {
		fmt.Fprintf(stderr, "legion git-credential: %s: %v; answering nothing\n", dir, err)
		return 1
	}
	fmt.Fprintf(stdout, "username=%s\npassword=%s\n", ghconfig.GitUser, token)
	return 0
}

// readCredentialRequest reads git's credential protocol: `key=value` lines up to a blank line or
// the end of input, the last value of a key winning.
func readCredentialRequest(r io.Reader) map[string]string {
	request := map[string]string{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			request[key] = value
		}
	}
	return request
}
