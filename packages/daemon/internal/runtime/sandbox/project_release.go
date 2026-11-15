package sandbox

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

var _ runtime.ProjectPods = (*Runtime)(nil)

// ProjectSandboxes is every Sandbox of the project the informer holds (it lists and watches on
// legion.dev/project alone), not counting one already being deleted, in name order: what a
// project release takes, and what its dry run reads. Each is told by its labels, as the orphan
// sweep tells them (podKindOf, labelCache, labelProbe), and an issue's pod carries the issue and
// tree its labels name, which SuspendIssue checks the Sandbox against. The pod is the one the
// Sandbox owns now and that has not ended, and its image is read as IssuePodRunsConfiguredImage
// reads it (imageDrift), so a release and an idle release never disagree about which pods run
// another image.
func (r *Runtime) ProjectSandboxes(context.Context) ([]runtime.ProjectSandbox, error) {
	if err := r.sandboxFeed.check(); err != nil {
		return nil, fmt.Errorf("list the project's Sandboxes: %w", err)
	}
	var listed []runtime.ProjectSandbox
	for _, obj := range r.sandboxes.GetStore().List() {
		u := obj.(*unstructured.Unstructured)
		if u.GetDeletionTimestamp() != nil {
			continue
		}
		listed = append(listed, r.projectSandbox(u))
	}
	slices.SortFunc(listed, func(a, b runtime.ProjectSandbox) int { return strings.Compare(a.Name, b.Name) })
	return listed, nil
}

// projectSandbox is u as ProjectSandboxes lists it.
func (r *Runtime) projectSandbox(u *unstructured.Unstructured) runtime.ProjectSandbox {
	labels := u.GetLabels()
	listed := runtime.ProjectSandbox{Name: u.GetName(), Kind: projectSandboxKind(labels)}
	if listed.Kind == runtime.ProjectSandboxIssue {
		listed.Issue, listed.Tree = labels[labelIssue], labels[labelTree]
	}
	pod := r.storedPod(u.GetName())
	if ownedBy(pod, u.GetUID()) && !terminal(pod) {
		listed.PodUID = string(pod.UID)
		_, drifted := imageDrift(pod, r.image)
		listed.ConfiguredImage = !drifted
	}
	return listed
}

// projectSandboxKind is the kind labels name: a probe's and a cache's marks first, since neither
// carries an issue or a role, then the two kinds podKindOf tells apart, and unknown for any other
// labelling.
func projectSandboxKind(labels map[string]string) runtime.ProjectSandboxKind {
	switch {
	case labels[labelProbe] != "":
		return runtime.ProjectSandboxProbe
	case labels[labelCache] != "":
		return runtime.ProjectSandboxCache
	}
	switch kind, _ := podKindOf(labels); kind.(type) {
	case issuePod:
		return runtime.ProjectSandboxIssue
	case controllerPod:
		return runtime.ProjectSandboxController
	}
	return runtime.ProjectSandboxUnknown
}

// DeleteProjectSandbox deletes the repository cache's or the image probe's Sandbox name for a
// project release, through the delete the cache sweep retires a cache with (deleteFenced): under
// the pod's launch turn, fenced to the Sandbox as the API holds it now, so its foreground delete
// takes the cache's volume and Secret with it, and behind the session-volume guard, whose refusal
// is answered. The Sandbox is read from the API rather than the informer's store, because the
// fence is the object's resourceVersion and a store a write behind would make the delete a
// conflict, which the orphan sweep would judge again at a sweep the release holds. A Sandbox
// already gone is deleted; one of another kind is refused, since an issue's pod and the
// controller's go through their own deletes.
func (r *Runtime) DeleteProjectSandbox(ctx context.Context, name string) error {
	release, err := r.lockPod(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	u, err := r.sandboxClient().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Sandbox %s: read it: %w", name, err)
	}
	labels := u.GetLabels()
	if labels[labelProject] != r.project {
		return fmt.Errorf("delete Sandbox %s: it is no Sandbox of project %s", name, r.project)
	}
	kind := projectSandboxKind(labels)
	if kind != runtime.ProjectSandboxCache && kind != runtime.ProjectSandboxProbe {
		return fmt.Errorf("delete Sandbox %s: it is a Sandbox of kind %s, which this delete does not take", name, kind)
	}
	if u.GetDeletionTimestamp() != nil {
		return nil
	}
	s, err := decodeSandbox(u)
	if err != nil {
		return err
	}
	return r.deleteFenced(ctx, s, "sandbox runtime: the project release deleted a Sandbox", "kind", string(kind))
}
