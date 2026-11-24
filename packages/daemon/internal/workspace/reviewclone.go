package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/rolefile"
	"golang.org/x/sys/unix"
)

// ReviewHeadEnv names the variable a pod's reviewer is told the head the daemon gives it in
// (runtime/sandbox's agentEnv), which its role-init reads (internal/launcher) and hands
// ReviewRequestFor. A role-init of an image from before it reads no such variable, and a daemon
// from before it sets none, which ReviewRequestFor reads as no head given.
const ReviewHeadEnv = "LEGION_REVIEW_HEAD"

// ReviewRequestFor is the request a pod reviewer's role-init makes or refreshes its own clone with
// (internal/launcher): dir, its LEGION_WORKSPACE, must be the ReviewLocation of issue's workspace in
// a state directory's layout, and that workspace's shared clone is what it is cloned from; head is
// the head the daemon gives the reviewer (ReviewHeadEnv), "" from a daemon that gives none. A head
// the shared clone lacks is fetched into the review clone from GitHub, at the repository the
// layout names (the issue's recorded repository, which the runtime built the layout from), with
// the reviewer's own GitHub credential helper (OriginWithHelper), the one its gh bundle answers.
// Any other dir is refused, naming it.
func ReviewRequestFor(dir, issue, defaultBranch, head string) (ReviewRequest, error) {
	shared := IssueWorkspace(dir)
	located, err := locationOf(shared)
	if err != nil || shared == dir {
		return ReviewRequest{}, fmt.Errorf("%s is no reviewer's clone of an issue's workspace in a state directory's layout", dir)
	}
	if want, err := ReviewLocation(stateDirOf(shared), located.Repo, issue); err != nil || want != dir {
		return ReviewRequest{}, fmt.Errorf("%s is not the reviewer's clone of %s", dir, issue)
	}
	return ReviewRequest{Clone: located.Clone, Dir: dir, Repo: located.Repo, Issue: issue, DefaultBranch: defaultBranch, Head: head,
		Origin: OriginWithHelper(GitHubURL(located.Repo))}, nil
}

// ReviewOrigin is where and how a review provision fetches the head under review straight into a
// review clone it has just made, where the shared clone does not hold that head (reviewHead): URL,
// the issue's recorded repository on GitHub (GitHubURL) and never a URL the shared clone's
// configuration names, and the credential that fetch reads it with, made when the fetch runs and
// removed after it. The zero ReviewOrigin names none, and its provision fetches nothing from
// origin.
type ReviewOrigin struct {
	URL        string
	credential func() (remote, error)
}

// IsZero is whether o names no way to origin.
func (o ReviewOrigin) IsZero() bool { return o.credential == nil }

// OriginWithToken is the tmux daemon's way to url: token, the repository owner's read lease,
// asked when the fetch runs, under the shared clone's store lock as FromGitHub asks it, and handed
// to git as the one-shot credential under credentialDir (newProvisioningCredential), removed once
// the fetch is done.
func OriginWithToken(url string, token func() (string, error), credentialDir string) ReviewOrigin {
	return ReviewOrigin{URL: url, credential: func() (remote, error) {
		value, err := token()
		if err != nil {
			return remote{}, err
		}
		return newProvisioningCredential(credentialDir, value)
	}}
}

// OriginWithHelper is a pod reviewer's way to url: its own GitHub credential helper
// (GitHubCredentialHelper), which answers from the reviewer's gh bundle (LEGION_GH_OWNERS_DIR,
// GH_CONFIG_DIR) the role-init's environment names, for https://github.com alone and after a reset
// of every helper configured before it.
func OriginWithHelper(url string) ReviewOrigin {
	return ReviewOrigin{URL: url, credential: func() (remote, error) {
		return remote{env: []string{
			"GIT_ASKPASS=",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_COUNT=4",
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0=",
			"GIT_CONFIG_KEY_1=credential.https://github.com.helper",
			"GIT_CONFIG_VALUE_1=" + GitHubCredentialHelper,
			"GIT_CONFIG_KEY_2=credential.useHttpPath",
			"GIT_CONFIG_VALUE_2=true",
			"GIT_CONFIG_KEY_3=credential.interactive",
			"GIT_CONFIG_VALUE_3=false",
		}}, nil
	}}
}

// ReviewRootOf is the directory every reviewer's clone of the state directory sits under
// (ReviewRoot), for the issue's workspace dir in that directory's layout: what a pod's role-init
// covers in every role's namespace but the reviewer's.
func ReviewRootOf(dir string) (string, error) {
	if _, err := locationOf(dir); err != nil {
		return "", err
	}
	return ReviewRoot(stateDirOf(dir)), nil
}

// ReviewRequest is where ProvisionReviewClone makes a reviewer's clone and what from. Clone is the
// shared clone the review clone takes its objects from, and its record of origin's branches; Dir
// is the review clone (ReviewLocation). Head is the commit the daemon gives the reviewer, the head
// of the issue's pull request it records: the review clone stands there or the provision is
// refused. Origin is how a head the shared clone lacks is fetched into it from its origin first;
// with the zero Origin such a head is refused. A daemon that records no head for the issue gives
// none, and the head is then Issue's branch (Bookmark) as the shared clone last saw it at origin,
// else DefaultBranch's, for an issue whose branch origin no longer holds.
// StoreLock and CheckoutSlot are Request's, for the same steps: the lock is held while the shared
// clone is read, and the checkout takes a place. A pod's reviewer passes neither: its role-init is
// the one provision of its pod's review clone, and a git clone or fetch reads a shared clone that
// another role's git writes at that moment as git reads any repository it shares. Log, when set,
// is told each stale lock a refresh removes, and each review clone made again because it named
// another object store.
type ReviewRequest struct {
	Clone, Dir           string
	Repo                 ghrepo.Repository
	Issue, DefaultBranch string
	Head                 string
	Origin               ReviewOrigin
	StoreLock            sync.Locker
	CheckoutSlot         func(ctx context.Context) (release func(), err error)
	Log                  func(string)
}

// reviewProvisionEnv is what the git of ProvisionReviewClone's clone, fetch, head and checkout
// reads besides the runner's pins: no global or system configuration, the fsmonitor off and no
// hook, and the file transport alone, the shared clone being a local path.
var reviewProvisionEnv = append(ReviewGitEnv(ReviewGitConfig()), "GIT_ALLOW_PROTOCOL=file")

// reviewStagingSuffix ends the name of the temporary sibling a review clone is made in.
const reviewStagingSuffix = ".review-"

// ProvisionReviewClone makes the reviewer's own clone at request.Dir, or brings the one there to
// the head. A new one is a `git clone --no-checkout --no-hardlinks --no-local` of the shared clone,
// which takes its objects through git's transport, hash-checked, rather than as a copy of the
// shared clone's files, so no role's write of the shared clone's object files reaches the review
// clone, and no objects/info/alternates the shared clone holds names a store it reads; a new clone
// that holds one all the same is refused (refuseAlternates). Then origin's
// branches as the shared clone records them (its refs/remotes/origin, which the clone alone does
// not copy), and the head (reviewHead): a head the shared clone does not hold is fetched from
// request.Origin straight into the new clone, whose configuration only this provision has written
// so far, reading nothing of the shared clone (fetchFromOrigin). Then origin set to GitHub with the
// shared clone's credential helper and gh's default repository, a checkout of the head, detached,
// and jj colocated with it, since
// the shim's adoption of the working copy and every role's prompt run jj in the workspace. It is
// made under a temporary sibling and renamed into place only once whole, so a clone that stands at
// Dir is a finished one, and one that stands there without its .jj is no clone of this function's,
// and is removed and made again; a temporary sibling an earlier attempt left when it was killed is
// removed before the next is made.
//
// A clone already there is brought to the head as a fresh one would stand, at every provision, so
// a reviewer never starts on the head its clone was first made at: the git lock files a reviewer's
// git killed mid-write left in it are removed, since its only writer is the reviewer, which is not
// running while its next launch provisions; origin's branches are fetched again from the shared
// clone's objects, by its path and with no network; and `jj new <head>` puts the working copy on
// the head, leaving what the reviewer wrote there before in a change of its own. Two such clones
// are made again instead, each with a line to request.Log. One whose object store names another
// (objects/info/alternates, which on the tmux runtime any pane can write, every pane running as
// one user; remakeNamingAnotherStore) is removed first, so it neither reaches the reviewer nor
// stops every start of it after. One whose head the shared clone does not hold, where
// request.Origin is to fetch it (errHeadOnlyAtOrigin), is made again beside it and replaces it once
// whole (makeReviewClone), so that fetch, which carries a credential, reads only a configuration
// this provision has just made, and a head that still cannot be had leaves the clone there as it
// was. Either way the clone's HEAD is then read, and a clone that stands anywhere but the head is
// refused, naming both commits (requireReviewHead).
//
// No other role's run writes it: a clone copies neither the shared clone's configuration nor its
// hooks, and no normal git operation rewrites an object file another clone links to. Its clone,
// fetch, head and checkout read no global or system configuration and run no fsmonitor or hook
// (reviewProvisionEnv), so nothing planted in the shared clone or the machine's configuration
// runs here; the reviewer's own git is held the same way (ReviewGitEnv, InstallReviewGit). It goes
// with the issue's workspace (RemoveReviewClone).
func ProvisionReviewClone(ctx context.Context, run Runner, request ReviewRequest) error {
	switch {
	case !filepath.IsAbs(request.Clone) || !filepath.IsAbs(request.Dir):
		return fmt.Errorf("review clone %q of %q: each must be an absolute path", request.Dir, request.Clone)
	case underDirectory(request.Clone, request.Dir) || underDirectory(request.Dir, request.Clone):
		return fmt.Errorf("review clone %s and the shared clone %s: neither may lie in the other", request.Dir, request.Clone)
	case request.Repo.IsZero():
		return errors.New("review clone repository is required")
	case request.Issue == "":
		return errors.New("review clone issue is required")
	case request.Head != "" && !fullCommitID(request.Head):
		return fmt.Errorf("review clone of %s: the head under review %q is no full commit id", request.Issue, request.Head)
	}
	if err := refuseSymlinkedLayout(request.Dir, request.Dir); err != nil {
		return err
	}
	complete := false
	if exists, err := pathExists(request.Dir); err != nil {
		return err
	} else if exists {
		if complete, err = pathExists(filepath.Join(request.Dir, ".jj")); err != nil {
			return err
		}
		if !complete {
			if err := rolefile.RemoveTree(request.Dir); err != nil {
				return fmt.Errorf("remove incomplete review clone %s: %w", request.Dir, err)
			}
		}
	}
	if complete {
		remade, err := remakeNamingAnotherStore(request)
		if err != nil {
			return err
		}
		complete = !remade
	}
	if complete {
		err := refreshReviewClone(ctx, run, request)
		if !errors.Is(err, errHeadOnlyAtOrigin) {
			return err
		}
		if request.Log != nil {
			request.Log(fmt.Sprintf("review clone %s: %v; a clone is made again beside it, whose fetch from %s reads only a configuration made now, and replaces it once whole", request.Dir, err, request.Origin.URL))
		}
		return makeReviewClone(ctx, run, request, true)
	}
	return makeReviewClone(ctx, run, request, false)
}

// makeReviewClone makes a new review clone (ProvisionReviewClone) under a temporary sibling of
// request.Dir and renames it into place once whole. With replace, the clone already at Dir is
// removed (RemoveReviewClone) only then, once the new one stands on the head, so a new clone that
// cannot be made leaves the one there as it was.
func makeReviewClone(ctx context.Context, run Runner, request ReviewRequest, replace bool) error {
	parent := filepath.Dir(request.Dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("make the review clone's parent: %w", err)
	}
	abandoned, err := filepath.Glob(filepath.Join(parent, filepath.Base(request.Dir)+reviewStagingSuffix+"*"))
	if err != nil {
		return err
	}
	for _, dir := range abandoned {
		if err := rolefile.RemoveTree(dir); err != nil {
			return fmt.Errorf("remove an abandoned review clone %s: %w", dir, err)
		}
	}
	temporary, err := os.MkdirTemp(parent, filepath.Base(request.Dir)+reviewStagingSuffix)
	if err != nil {
		return fmt.Errorf("make the review clone's temporary directory: %w", err)
	}
	defer func() { _ = rolefile.RemoveTree(temporary) }()
	head, err := func() (string, error) {
		store := &storeLock{locker: request.StoreLock}
		store.lock()
		defer store.unlock()
		clone := []string{"git", "clone", "--quiet", "--no-checkout", "--no-hardlinks", "--no-local", request.Clone, temporary}
		result, err := runCommandTimeout(ctx, run, clone, reviewProvisionEnv, parent, CloneTimeout)
		if _, err := checkedResult(clone, result, err); err != nil {
			return "", err
		}
		if err := refuseAlternates(temporary); err != nil {
			return "", err
		}
		if err := fetchForReview(ctx, run, temporary, "origin"); err != nil {
			return "", err
		}
		return reviewHead(ctx, run, request, temporary, "origin", true)
	}()
	if err != nil {
		return err
	}
	gitDir := "--git-dir=" + filepath.Join(temporary, ".git")
	if _, err := RunChecked(ctx, run, []string{"git", gitDir, "remote", "set-url", "origin", GitHubURL(request.Repo)}, reviewProvisionEnv, temporary); err != nil {
		return err
	}
	if err := configureRepositoryCredential(ctx, run, temporary, GitHubCredentialHelper); err != nil {
		return err
	}
	if err := configureCloneDefaultRepository(ctx, run, temporary); err != nil {
		return err
	}
	if err := pointOriginHeadAtDefault(ctx, run, temporary, request.DefaultBranch); err != nil {
		return err
	}
	err = inCheckoutPlace(ctx, request, func(bound time.Duration) error {
		for _, step := range []struct {
			dir  string
			argv []string
			env  []string
		}{
			{temporary, []string{"git", "checkout", "--quiet", "--detach", head}, reviewProvisionEnv},
			// jj opens no repository here, so it runs in the parent (cloneSteps.dir says why).
			{parent, []string{"jj", "git", "init", "--colocate", temporary}, nil},
		} {
			result, err := runCommandTimeout(ctx, run, step.argv, step.env, step.dir, bound)
			if _, err := checkedResult(step.argv, result, err); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := requireReviewHead(ctx, run, temporary, head); err != nil {
		return err
	}
	if replace {
		if err := RemoveReviewClone(request.Dir); err != nil {
			return fmt.Errorf("remove review clone %s for the one made again: %w", request.Dir, err)
		}
	}
	if err := os.Rename(temporary, request.Dir); err != nil {
		if complete, statErr := pathExists(filepath.Join(request.Dir, ".jj")); cloneCollision(err) && statErr == nil && complete {
			return nil
		}
		return fmt.Errorf("rename the review clone into place: %w", err)
	}
	return nil
}

// refreshReviewClone brings the review clone at request.Dir to the head (ProvisionReviewClone):
// the stale locks under its git directory removed, origin's branches and the head fetched again
// from the shared clone by its path, under its store lock, then `jj new <head>` in the clone, in a
// checkout place.
func refreshReviewClone(ctx context.Context, run Runner, request ReviewRequest) error {
	log := request.Log
	if log == nil {
		log = func(string) {}
	}
	why := "a review clone's only writer is its reviewer, which is not running while its launch provisions"
	if err := removeLocksUnder(filepath.Join(request.Dir, ".git"), time.Now(), log, why); err != nil {
		return err
	}
	head, err := func() (string, error) {
		store := &storeLock{locker: request.StoreLock}
		store.lock()
		defer store.unlock()
		if err := fetchForReview(ctx, run, request.Dir, request.Clone); err != nil {
			return "", err
		}
		return reviewHead(ctx, run, request, request.Dir, request.Clone, false)
	}()
	if err != nil {
		return err
	}
	err = inCheckoutPlace(ctx, request, func(bound time.Duration) error {
		argv := []string{"jj", "new", head, "--color=never", "-R", request.Dir}
		result, err := runCommandTimeout(ctx, run, argv, nil, request.Dir, bound)
		_, err = checkedResult(argv, result, err)
		return err
	})
	if err != nil {
		return err
	}
	return requireReviewHead(ctx, run, request.Dir, head)
}

// requireReviewHead refuses the review clone at dir unless the commit its git HEAD names is head,
// the one the daemon gives the reviewer (or, where it gives none, the one the provision brought it
// to): a reviewer reads the change through that HEAD, so a clone left at any other commit is
// refused, naming both, rather than handed to the reviewer.
func requireReviewHead(ctx context.Context, run Runner, dir, head string) error {
	result, err := RunChecked(ctx, run, []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "rev-parse", "HEAD"}, reviewProvisionEnv, dir)
	if err != nil {
		return err
	}
	if at := strings.TrimSpace(result.Stdout); at != head {
		return fmt.Errorf("review clone %s stands on %s, not on %s, the head under review", dir, at, head)
	}
	return nil
}

// alternatesOf is the objects/info/alternates file of the review clone at dir, and whether one is
// there: a file that names another object store, which the clone's git would read objects from.
func alternatesOf(dir string) (string, bool, error) {
	alternates := filepath.Join(dir, ".git", "objects", "info", "alternates")
	if _, err := os.Lstat(alternates); err == nil {
		return alternates, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return alternates, false, fmt.Errorf("read the review clone's %s: %w", alternates, err)
	}
	return alternates, false, nil
}

// refuseAlternates refuses the new review clone at dir where its object store names another
// (alternatesOf): its git would read objects from a store that another role may write. A clone
// that --no-local made holds none, so this is a guard on git's own behaviour, and such a clone is
// refused rather than read.
func refuseAlternates(dir string) error {
	alternates, found, err := alternatesOf(dir)
	if err != nil || !found {
		return err
	}
	return fmt.Errorf("review clone %s: %s names another object store, which its git would read", dir, alternates)
}

// alternatesShown bounds what a log line quotes of a planted alternates file.
const alternatesShown = 1024

// remakeNamingAnotherStore removes the review clone at request.Dir where its object store names
// another (alternatesOf), and reports that it did, so the provision makes the clone again from the
// shared clone: a clone made with --no-local holds no such file, so one there was written into the
// clone since, and on the tmux runtime any pane can write it. It tells request.Log the file and
// what it names (its first alternatesShown bytes, read only from a regular file), so the planted
// path is on record, and removes the clone as the issue's workspace removal does
// (RemoveReviewClone). What the reviewer wrote in the clone goes with it.
func remakeNamingAnotherStore(request ReviewRequest) (bool, error) {
	alternates, found, err := alternatesOf(request.Dir)
	if err != nil || !found {
		return false, err
	}
	named := "a file that is no regular file"
	if file, err := os.OpenFile(alternates, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0); err == nil {
		if info, err := file.Stat(); err == nil && info.Mode().IsRegular() {
			body, _ := io.ReadAll(io.LimitReader(file, alternatesShown))
			named = strconv.Quote(strings.TrimSpace(string(body)))
		}
		_ = file.Close()
	}
	if request.Log != nil {
		request.Log(fmt.Sprintf("review clone %s: %s names another object store, %s, which its git would read; the clone is removed and made again from the shared clone", request.Dir, alternates, named))
	}
	if err := RemoveReviewClone(request.Dir); err != nil {
		return false, fmt.Errorf("remove review clone %s, whose %s names another object store: %w", request.Dir, alternates, err)
	}
	return true, nil
}

// fullCommitID is whether id is a commit id as git writes one in full, lower-case hex of SHA-1's
// 40 digits or SHA-256's 64: a head the daemon gives is read by git as an object id and never as
// an option, a ref or a revision expression.
func fullCommitID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// inCheckoutPlace runs checkout, a step that writes the review clone's whole working copy, holding
// one of request's checkout places and given the checkout's bound, as Provision's own checkout is.
func inCheckoutPlace(ctx context.Context, request ReviewRequest, checkout func(bound time.Duration) error) error {
	slot := Request{CheckoutSlot: request.CheckoutSlot}
	release, err := slot.checkoutPlace(ctx)
	if err != nil {
		return fmt.Errorf("check out review clone %s: wait for a checkout's place: %w", request.Dir, err)
	}
	defer release()
	if err := checkout(slot.checkoutBound()); err != nil {
		return fmt.Errorf("check out review clone %s: %w", request.Dir, err)
	}
	return nil
}

// fetchForReview fetches origin's branches as the shared clone records them (its
// refs/remotes/origin) from from — the clone's own origin while it still names the shared clone,
// else the shared clone's path — into the review clone at dir, pruning the ones the shared clone
// no longer has.
func fetchForReview(ctx context.Context, run Runner, dir, from string) error {
	_, err := RunChecked(ctx, run, []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "fetch", "--quiet", "--no-tags",
		"--prune", from, "+refs/remotes/origin/*:refs/remotes/origin/*"}, reviewProvisionEnv, dir)
	return err
}

// reviewHeadRef is the ref a review clone holds at the head the daemon gave its reviewer, which jj
// shows the reviewer as the remote bookmark head-under-review@legion: jj reads a commit only
// through a ref it imports, and a head fetched by its id has no other.
const reviewHeadRef = "refs/remotes/legion/head-under-review"

// errHeadOnlyAtOrigin is a refresh's answer where the shared clone does not hold the head under
// review and request.Origin is to fetch it: the clone already there is made again
// (ProvisionReviewClone), and the new one fetches it from origin.
var errHeadOnlyAtOrigin = errors.New("the head under review is no commit of the shared clone")

// reviewHead is the head the review clone at dir is to stand on, read under the shared clone's
// store lock once origin's branches are fetched (fetchForReview). Where the daemon gives a head
// (request.Head), it is that commit: fetched by its id from from — the clone's own origin while it
// still names the shared clone, else the shared clone's path — where the branches did not bring
// it. Where the shared clone does not hold it either and request.Origin names a way to it, a fresh
// clone (fresh: dir was made by this provision, so its configuration is this provision's alone)
// fetches it straight from origin (fetchFromOrigin), and a clone already there answers
// errHeadOnlyAtOrigin, so it is made again first. It is refused only where it still cannot be had,
// naming it and the commit the shared clone records for Issue's branch, with what each fetch
// answered, since what the shared clone's refs say is no part of what the reviewer reads. The
// clone's reviewHeadRef is then set to it. Where the daemon gives no head, it is Issue's branch at
// origin, else DefaultBranch's, as the fetch recorded them.
func reviewHead(ctx context.Context, run Runner, request ReviewRequest, dir, from string, fresh bool) (string, error) {
	if request.Head == "" {
		for _, branch := range []string{Bookmark(request.Issue), request.DefaultBranch} {
			if branch == "" {
				continue
			}
			if at, err := originBranch(ctx, run, dir, branch); err != nil || at != "" {
				return at, err
			}
		}
		return "", fmt.Errorf("review clone of %s: the shared clone %s records neither %s nor %q at origin", request.Repo, request.Clone, Bookmark(request.Issue), request.DefaultBranch)
	}
	gitDir := "--git-dir=" + filepath.Join(dir, ".git")
	held, err := holdsCommit(ctx, run, dir, request.Head)
	if err != nil {
		return "", err
	}
	if !held {
		argv := []string{"git", gitDir, "fetch", "--quiet", "--no-tags", from, request.Head}
		fetched, err := runCommand(ctx, run, argv, reviewProvisionEnv, dir)
		if err := answered(argv, fetched, err); err != nil {
			return "", err
		}
		if held, err = holdsCommit(ctx, run, dir, request.Head); err != nil {
			return "", err
		}
		answers := []string{"the shared clone answered: " + strings.TrimSpace(fetched.Stderr)}
		switch {
		case held:
		case request.Origin.IsZero():
			answers = append(answers, "no origin is given to fetch it from")
		case !fresh:
			return "", fmt.Errorf("%w: %s holds no %s", errHeadOnlyAtOrigin, request.Clone, request.Head)
		default:
			if failed := fetchFromOrigin(ctx, run, request, dir); failed != "" {
				answers = append(answers, failed)
			} else if held, err = holdsCommit(ctx, run, dir, request.Head); err != nil {
				return "", err
			} else if !held {
				answers = append(answers, fmt.Sprintf("origin %s answered its fetch and gave no such commit", request.Origin.URL))
			}
		}
		if !held {
			recorded, err := originBranch(ctx, run, dir, Bookmark(request.Issue))
			if err != nil {
				return "", err
			}
			if recorded == "" {
				recorded = "nothing"
			}
			return "", fmt.Errorf("review clone of %s: the head under review %s is no commit of the shared clone %s, whose %s at origin is %s, and it could not be had (%s)",
				request.Repo, request.Head, request.Clone, Bookmark(request.Issue), recorded, strings.Join(answers, "; "))
		}
	}
	if _, err := RunChecked(ctx, run, []string{"git", gitDir, "update-ref", reviewHeadRef, request.Head}, reviewProvisionEnv, dir); err != nil {
		return "", err
	}
	return request.Head, nil
}

// fetchFromOrigin fetches request.Head straight into the review clone at dir, one this provision
// has just made, from request.Origin.URL, the issue's recorded repository, with request.Origin's
// credential, made for this fetch and removed after it, and answers why it could not, "" where git
// fetched it. Nothing of the shared clone is read: neither the URL its configuration names nor any
// other key of it, so no url.<base>.insteadOf, http.proxy or credential helper another role
// planted there sees the fetch or its credential. The fetch reads the new clone's configuration
// alone, which this provision made and nothing else has written, and no global or system
// configuration, with the fsmonitor off and no hook (the runner's pins), every credential helper
// configured before request.Origin's reset, and that URL's one transport alone: https for GitHub,
// the file transport for a local repository, and nothing else, so a URL that names a command
// (ext::) or any other transport is never fetched from. It writes no FETCH_HEAD and starts no gc.
func fetchFromOrigin(ctx context.Context, run Runner, request ReviewRequest, dir string) string {
	url := request.Origin.URL
	protocol := ""
	switch {
	case strings.HasPrefix(url, "https://"):
		protocol = "https"
	case filepath.IsAbs(url):
		protocol = "file"
	default:
		return fmt.Sprintf("origin %q is neither GitHub over https nor a local repository, so nothing was fetched from it", url)
	}
	credential, err := request.Origin.credential()
	if err != nil {
		return fmt.Sprintf("no credential to fetch it from origin %s: %v", url, err)
	}
	defer func() { _ = credential.remove() }()
	argv := []string{"git", "-c", "core.fsmonitor=false", "--git-dir=" + filepath.Join(dir, ".git"), "fetch", "--quiet", "--no-tags",
		"--no-write-fetch-head", "--no-auto-gc", url, request.Head}
	env := slices.Concat(isolatedGitConfig, []string{"GIT_ALLOW_PROTOCOL=" + protocol}, credential.env)
	result, err := runCommandTimeout(ctx, run, argv, env, dir, CloneTimeout)
	result.Stderr = credential.scrub(result.Stderr)
	if err := answered(argv, result, err); err != nil {
		return credential.scrub(fmt.Sprintf("its fetch from origin %s did not answer: %v", url, err))
	}
	if result.ExitCode != 0 {
		return fmt.Sprintf("origin %s answered: %s", url, strings.TrimSpace(result.Stderr))
	}
	return ""
}

// originBranch is the commit the review clone at dir records for branch at origin, as its last
// fetch of the shared clone's record brought it, "" where it records none.
func originBranch(ctx context.Context, run Runner, dir, branch string) (string, error) {
	argv := []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/" + branch + "^{commit}"}
	found, err := runCommand(ctx, run, argv, reviewProvisionEnv, dir)
	if err := answered(argv, found, err); err != nil {
		return "", err
	}
	if found.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(found.Stdout), nil
}

// pointOriginHeadAtDefault makes the review clone's refs/remotes/origin/HEAD name the issue's
// default branch at origin, or removes it where the clone records no such branch, before
// `jj git init --colocate` reads it. jj writes the branch origin's HEAD names into the repository's
// configuration as its trunk() alias, `<branch>@origin`, with no present() around it. The clone is
// a clone of the shared clone, whose HEAD can name the issue's own branch, and once that branch
// leaves origin every jj command of the clone fails resolving trunk(): jj 0.46 resolves it for the
// short prefixes of each commit it prints, so the next provision's `jj new` fails too. The default
// branch is what trunk() means, and with no origin HEAD jj keeps its built-in trunk(), which
// names its branches through present().
func pointOriginHeadAtDefault(ctx context.Context, run Runner, dir, defaultBranch string) error {
	gitDir := "--git-dir=" + filepath.Join(dir, ".git")
	at, err := originBranch(ctx, run, dir, defaultBranch)
	if err != nil {
		return err
	}
	argv := []string{"git", gitDir, "update-ref", "--no-deref", "-d", "refs/remotes/origin/HEAD"}
	if at != "" {
		argv = []string{"git", gitDir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/" + defaultBranch}
	}
	_, err = RunChecked(ctx, run, argv, reviewProvisionEnv, dir)
	return err
}

// holdsCommit is whether the review clone at dir holds commit, a full commit id, as a commit.
func holdsCommit(ctx context.Context, run Runner, dir, commit string) (bool, error) {
	argv := []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "cat-file", "-e", commit + "^{commit}"}
	found, err := runCommand(ctx, run, argv, reviewProvisionEnv, dir)
	if err := answered(argv, found, err); err != nil {
		return false, err
	}
	return found.ExitCode == 0, nil
}

// RemoveReviewClone removes the reviewer's clone at dir, which its issue's workspace removal does
// with the workspace. It renames the clone aside first (removingSuffix, as Remove does), so a kill
// in the slower delete after leaves no half a clone at dir, which ProvisionReviewClone would
// otherwise take for a whole one; an aside an earlier removal left is cleared first.
func RemoveReviewClone(dir string) error {
	aside := removingPath(dir)
	if err := rolefile.RemoveTree(aside); err != nil {
		return fmt.Errorf("clear a stale removal of review clone %s: %w", dir, err)
	}
	if exists, err := pathExists(dir); err != nil {
		return err
	} else if exists {
		if err := os.Rename(dir, aside); err != nil {
			return fmt.Errorf("rename review clone %s aside before removing it: %w", dir, err)
		}
	}
	return rolefile.RemoveTree(aside)
}
