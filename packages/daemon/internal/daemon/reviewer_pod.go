package daemon

// A reviewer starts only in an issue pod that runs the configured image. The reviewer works in a
// git clone of its own, with a git that runs no program a configuration names
// (internal/workspace's ProvisionReviewClone and ReviewGitEnv), and only an image of this release
// serves that: an older image's role-init makes no clone and its launcher works the wrong path,
// so a reviewer started in a pod on such an image would review in the issue's shared workspace.
// The boot's image probe holds the configured image to this daemon's contract, so the configured
// image is the one this rule can trust; a pod on any other image is replaced first.
//
// The replacement is the pod's own update round (podupdate.go): it asks every role in the pod to
// push its work and answer, and replaces the pod once they have, or at its bound. A reviewer's
// start opens that round at once, outside the project's limits on openings, because a reviewer
// waits on it; a start while the round is already open joins it. The reviewer starts in the pod
// the first launch after the replacement makes, on the configured image.
//
// Under a runtime that runs no issue pods (tmux) nothing here holds anything: a pane runs on the
// daemon's own host, whose reviewer clone the daemon makes itself (specs.reviewerGit).

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

const (
	// reviewerStartReason is the reason a round a reviewer's start opens names first, before the
	// drift the runtime reads of the pod.
	reviewerStartReason = "reviewer start"
	// reviewerPodReplacedLine is the line written once for each pod a reviewer's start holds.
	reviewerPodReplacedLine = "supervise: the issue pod is replaced before its reviewer starts, because it runs an image other than the configured one"
)

// issuePodImages is what a runtime that runs issue pods answers of the image an issue's live pod
// runs (sandbox.Runtime.IssuePodImage). A runtime that runs no pods implements none of it.
type issuePodImages interface {
	IssuePodImage(issue string) (sandbox.PodImage, bool)
}

// reviewerPods holds a reviewer's start out of an issue pod on another image than the configured
// one, and keeps, by issue, the pod whose replacement a held start waits for, which the
// update-round sweep reads (waitsFor). It keeps nothing in the store: a restarted daemon holds the
// next start again, and that start records the pod again.
type reviewerPods struct {
	// images is the runtime's reading, nil under a runtime that runs no issue pods.
	images issuePodImages
	log    *slog.Logger

	mu      sync.Mutex
	waiting map[string]string
}

func newReviewerPods(rt runtime.Runtime, log *slog.Logger) *reviewerPods {
	images, _ := rt.(issuePodImages)
	return &reviewerPods{images: images, log: log, waiting: map[string]string{}}
}

// onAnotherImage is the live pod of c's issue and the image it runs, where c is a reviewer's claim
// and that pod runs another image than the configured one. A pod the runtime does not answer live
// (no Sandbox, a suspended one, a pod that ended) is none: the launch makes a fresh pod on the
// configured image.
func (p *reviewerPods) onAnotherImage(c supervise.Claim) (sandbox.PodImage, bool) {
	if p == nil || p.images == nil || c.Role != claim.RoleReviewer || c.Issue == "" {
		return sandbox.PodImage{}, false
	}
	image, live := p.images.IssuePodImage(c.Issue)
	return image, live && image.Image != image.Configured
}

// behind is the stale-plugin lane's term (staleForTheLane): c is a reviewer whose issue pod runs
// another image than the configured one, which the lane leaves to that pod's round. It records
// nothing, since the state's count reads it too.
func (p *reviewerPods) behind(c supervise.Claim) bool {
	_, other := p.onAnotherImage(c)
	return other
}

// check is the rule at a launch: a reviewer's launch into an issue pod on another image than the
// configured one is refused with a wait, the pod is recorded as the one the issue's reviewer
// waits for, and the line is written the first time a start is held for that pod. A reviewer's
// launch the rule lets through drops what was recorded for its issue.
func (p *reviewerPods) check(c supervise.Claim) error {
	image, other := p.onAnotherImage(c)
	if p == nil || c.Role != claim.RoleReviewer {
		return nil
	}
	p.mu.Lock()
	if !other {
		delete(p.waiting, c.Issue)
		p.mu.Unlock()
		return nil
	}
	first := p.waiting[c.Issue] != image.Pod
	p.waiting[c.Issue] = image.Pod
	p.mu.Unlock()
	if first {
		p.log.Info(reviewerPodReplacedLine, "issue", c.Issue, "pod", image.Pod, "image", image.Image, "configured_image", image.Configured)
	}
	return wait.Errorf("the reviewer of %s starts only in a pod on the configured image %s, and its pod %s runs %s: the pod's update round replaces it first",
		c.Issue, image.Configured, image.Pod, image.Image)
}

// waitsFor is whether a held reviewer start waits for pod, the pod issue's claims record, to be
// replaced. A recorded pod the claims no longer record has gone, and is dropped.
func (p *reviewerPods) waitsFor(issue, pod string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.waiting[issue]
	if ok && held != pod {
		delete(p.waiting, issue)
	}
	return ok && held == pod
}

// keepOnly drops what is recorded for every issue but issues, the issues with a live claim: an
// issue that has none starts no reviewer.
func (p *reviewerPods) keepOnly(issues []string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for issue := range p.waiting {
		if !slices.Contains(issues, issue) {
			delete(p.waiting, issue)
		}
	}
}

// gate wraps store so that every launch's recheck (supervise.Store.CheckLaunch, which a launch
// makes before it records a launch or calls the runtime) also holds a reviewer's start to this
// rule. The store's own check is asked first, so a tree's reserved cleanup is answered as it
// always is.
func (p *reviewerPods) gate(store supervise.Store) supervise.Store {
	return reviewerGatedStore{Store: store, pods: p}
}

type reviewerGatedStore struct {
	supervise.Store
	pods *reviewerPods
}

func (s reviewerGatedStore) CheckLaunch(ctx context.Context, c supervise.Claim) error {
	if err := s.Store.CheckLaunch(ctx, c); err != nil {
		return err
	}
	return s.pods.check(c)
}
