package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/procgroup"
)

// What gh-stack's git reads of the repository, held to what `legion gh stack` can read for
// certain. gh-stack runs `git fetch`, `git push`, `git rebase`, `git checkout` and `git commit`
// in the working directory with the call's token directory in its environment, so whatever the
// repository's configuration, remotes or stack file tells that git to run or to take as an option
// runs with it. The git gh runs reads no global or system file (ghChildForced) and the runtime's
// own pins (core.hooksPath, core.fsmonitor, ghChildGitConfig) on its command line, so the
// repository's own files are what is left, and each is read here before gh-stack runs:
//
//   - its configuration, key by key, against an allowlist (ghStackGitConfigKeys): any key a file
//     sets that is not on it is refused, since git has many keys that run a program (a remote's
//     vcs, uploadpack or receivepack, core.sshCommand, core.gitProxy, core.askPass, core.editor,
//     gpg.program, a filter's smudge or clean, a merge or diff driver, a submodule's update) or
//     move a fetch or a push elsewhere (url.*.insteadOf, a remote's proxy or mirror), and a list
//     of the ones to refuse misses the next one;
//   - its remotes: every line `git remote -v` prints, fetch and push, must be a github.com
//     OWNER/REPO URL with nothing the wrapper cannot read (isPlainGitHubURL), since git takes a
//     `<transport>::<address>` URL (`ssh::…`, `ext::…`) as a program to run, and a path or
//     another host is a repository the call's routing never counted;
//   - gh-stack's stack file, `<git common dir>/gh-stack`, whose branch names and commit ids
//     gh-stack hands to git as arguments: a branch name that starts with `-` or that
//     `git check-ref-format` refuses, and a head or base that is no commit id, are refused, and
//     so is a local branch whose name starts with `-`.

// ghStackGitConfigKeys are the configuration keys, canonical as gitConfigKey writes them, that a
// file of the repository may set for `legion gh stack` to run: what a clone, a Legion workspace
// (internal/workspace's credential and gh-resolved keys), gh-stack itself (gh-stack.remote,
// gh-stack.rerere-declined, rerere) and a person's everyday settings write, none of which makes
// git run a program or move a fetch or a push. The credential and http keys and the includes are
// judged by ghRepoGitConfigRefusal, which every call runs first.
var ghStackGitConfigKeys = regexp.MustCompile(`^(` + strings.Join([]string{
	`core\.(repositoryformatversion|filemode|bare|logallrefupdates|ignorecase|precomposeunicode|symlinks|autocrlf|safecrlf|eol|sparsecheckout|sparsecheckoutcone|untrackedcache|commitgraph|multipackindex|abbrev|quotepath|checkstat|trustctime|preloadindex|splitindex|packedgitwindowsize|packedgitlimit|deltabasecachelimit|bigfilethreshold|compression|loosecompression|warnambiguousrefs)`,
	`extensions\.(objectformat|partialclone|refstorage|preciousobjects|noop|worktreeconfig)`,
	`remote\..+\.(url|pushurl|fetch|promisor|partialclonefilter|tagopt|prune|prunetags|skipdefaultupdate|skipfetchall|gh-resolved)`,
	`remote\.pushdefault`,
	`branch\..+\.(remote|pushremote|merge|rebase|description)`,
	`gh-stack\.(remote|rerere-declined)`,
	`rerere\.(enabled|autoupdate)`,
	`(user|author|committer)\.(name|email)`, `user\.useconfigonly`,
	`init\.defaultbranch`,
	`pull\.(rebase|ff)`, `push\.(default|autosetupremote|followtags|useforceifincludes)`,
	`fetch\.(prune|prunetags|writecommitgraph|showforcedupdates|parallel)`,
	`gc\.(auto|autodetach|autopacklimit|writecommitgraph)`, `maintenance\.(auto|strategy)`,
	`index\.(version|threads|skiphash)`, `pack\.(threads|windowmemory|usebitmaps)`,
	`advice\.[a-z0-9]+`, `color\.(ui|branch|diff|status|interactive)`,
	`status\.(showuntrackedfiles|branch|short|aheadbehind)`,
	`rebase\.(autosquash|autostash|updaterefs|abbreviatecommands|instructionformat|missingcommitscheck|stat|forkpoint)`,
	`submodule\..+\.(url|active|branch)`,
	`credential\..+`, `http\..+`, `include\.path`, `includeif\..+\.path`,
}, "|") + `)$`)

// ghStackPushSettings are the allowlisted keys gh-stack takes the remote it pushes to and fetches
// from, where --remote is absent (internal/git/gitops.go, ResolveRemote): each must name a remote
// `git remote -v` lists, or `.`, git's name for this repository (no URL rewrite is allowlisted to
// move it).
func ghStackPushSetting(key string) bool {
	return key == "remote.pushdefault" || key == "gh-stack.remote" ||
		strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".pushremote") || strings.HasSuffix(key, ".remote"))
}

// ghStackRemotes refuses a remote line that is no plain github.com OWNER/REPO URL, a remote whose
// name starts with `-`, and a --remote value that names no listed remote, and answers the names
// of the remotes `git remote -v` lists.
func ghStackRemotes(call ghStackCall, lines []ghRemote) ([]string, error) {
	var names []string
	for _, line := range lines {
		if line.marker {
			continue
		}
		if strings.HasPrefix(line.name, "-") {
			return nil, fmt.Errorf("the working directory's remote %q starts with `-`, which gh-stack would hand to git, where it is an option of git's own, so `gh stack` sends nothing; rename the remote", line.name)
		}
		if !line.isPlainGitHubURL() {
			kind := "fetch"
			if line.push {
				kind = "push"
			}
			return nil, fmt.Errorf("the working directory's remote %q has the %s URL %q, which is no https github.com OWNER/REPO URL, and the git gh-stack runs would fetch or push there, or run the program a `<transport>::` URL names, outside the repository this call is routed by, so `gh stack` sends nothing; point the remote at https://github.com/OWNER/REPO or remove it", line.name, kind, line.url)
		}
		names = append(names, line.name)
	}
	for _, remote := range call.remotes {
		if !slices.Contains(names, remote) {
			return nil, fmt.Errorf("`gh stack` gives --remote %q, which is no remote `git remote -v` lists here (%s), so it sends nothing; name one of them", remote, strings.Join(slices.Compact(slices.Sorted(slices.Values(names))), ", "))
		}
	}
	return names, nil
}

// ghStackGitConfigRefusal reads every key the repository's configuration files set, as gh-stack's
// git reads them (`git config --show-origin --null --list` under the environment gh gets), leaves
// the command line's own entries alone (the runtime's pins, ghChildGitConfig), and refuses a key
// that is not on ghStackGitConfigKeys, naming it and its file, and a push setting that names no
// remote of names and is not `.`.
func ghStackGitConfigRefusal(names []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "--show-origin", "--null", "--list")
	cmd.Env = append(ghChildEnviron(os.Environ(), nil), "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("cannot read the repository's git configuration (%v: %s), which gh-stack's git would read, so `gh stack` sends nothing", err, strings.TrimSpace(stderr.String()))
	}
	// Each entry is origin NUL key, then LF and the value where the key has one, NUL.
	fields := strings.Split(strings.TrimSuffix(stdout.String(), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		origin := fields[i]
		if origin == "command line:" {
			continue
		}
		key, value, _ := strings.Cut(fields[i+1], "\n")
		key = gitConfigKey(key)
		switch {
		case !ghStackGitConfigKeys.MatchString(key):
			return fmt.Errorf("the repository's git configuration (%s) sets %s, which is no setting `gh stack` lets its git read: many such settings make git run a program or fetch and push elsewhere, with this call's token directory in its environment, so it sends nothing; unset it with `git config --unset-all %s`", origin, key, key)
		case ghStackPushSetting(key) && value != "." && !slices.Contains(names, value):
			return fmt.Errorf("the git setting %s names %q, which is no remote `git remote -v` lists here, and gh-stack would push or fetch there, outside the repository this call is routed by, so `gh stack` sends nothing; set it to one of them or unset it", key, value)
		}
	}
	return nil
}

// ghStackCommitID is a commit id as gh-stack's stack file records one: SHA-1 or SHA-256 hex.
var ghStackCommitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ghStackRef is a branch of gh-stack's stack file, as gh-stack v0.3.0's internal/stack writes it.
type ghStackRef struct {
	Branch      string `json:"branch"`
	Head        string `json:"head"`
	Base        string `json:"base"`
	PullRequest *struct {
		MergedHead string `json:"mergedHead"`
	} `json:"pullRequest"`
}

// ghStackFileRefusal refuses a stack file gh-stack would hand git something unchecked from: one
// it cannot read as gh-stack's JSON, a trunk or branch name that starts with `-` or that `git
// check-ref-format` refuses as refs/heads/<name>, and a head, base or merged head that is neither
// empty nor a commit id; and a local branch whose name starts with `-`. Outside a repository, or
// in one with no stack file, it reads nothing more.
func ghStackFileRefusal() error {
	common, err := gitLines("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if len(common) != 1 {
		return nil
	}
	refs, err := gitLines("for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if strings.HasPrefix(strings.TrimPrefix(ref, "refs/heads/"), "-") {
			return fmt.Errorf("the local branch %s starts with `-`, and gh-stack hands a branch name to git, where it is an option of git's own, so `gh stack` sends nothing; rename or delete it", ref)
		}
	}
	path := filepath.Join(common[0], "gh-stack")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read gh-stack's stack file %s (%v), whose branches gh-stack hands to git, so `gh stack` sends nothing", path, err)
	}
	var file struct {
		Stacks []struct {
			Trunk    ghStackRef   `json:"trunk"`
			Branches []ghStackRef `json:"branches"`
		} `json:"stacks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("cannot read gh-stack's stack file %s as gh-stack writes it (%v), so `gh stack` sends nothing", path, err)
	}
	for _, stack := range file.Stacks {
		for _, ref := range append([]ghStackRef{stack.Trunk}, stack.Branches...) {
			if err := ghStackBranchName(path, ref.Branch); err != nil {
				return err
			}
			ids := []string{ref.Head, ref.Base}
			if ref.PullRequest != nil {
				ids = append(ids, ref.PullRequest.MergedHead)
			}
			for _, id := range ids {
				if id != "" && !ghStackCommitID.MatchString(id) {
					return fmt.Errorf("gh-stack's stack file %s records %q for branch %q, which is no commit id, and gh-stack hands it to git, so `gh stack` sends nothing", path, id, ref.Branch)
				}
			}
		}
	}
	return nil
}

// ghStackBranchName refuses a stack file's branch name that starts with `-`, or that `git
// check-ref-format refs/heads/<name>` refuses.
func ghStackBranchName(path, name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("gh-stack's stack file %s names the branch %q, which gh-stack would hand to git, where a name that starts with `-` is an option of git's own, so `gh stack` sends nothing", path, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "refs/heads/"+name)
	cmd.Env = append(ghChildEnviron(os.Environ(), nil), "LC_ALL=C")
	err := cmd.Run()
	switch end := procgroup.Ended(err); {
	case err == nil:
		return nil
	case end.ExitedWith(1):
		return fmt.Errorf("gh-stack's stack file %s names the branch %q, which is no branch name `git check-ref-format` reads, so `gh stack` sends nothing", path, name)
	default:
		return fmt.Errorf("cannot check the branch %q of gh-stack's stack file %s (%v), so `gh stack` sends nothing", name, path, err)
	}
}
