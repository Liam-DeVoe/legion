package sandbox

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// CleanupTree deletes every issue Sandbox of tree, and with each the role Secrets it owns and the
// kept volume it owns where its pod keeps one (deleteSandbox says how its foreground delete takes
// them). It keeps nothing for a child the tree carried into a tree of its own: an issue pod's
// ephemeral disk is the pod's own, which went with that pod, and a kept volume goes with its
// Sandbox here, so there is no disk to hand over and the child's next launch creates a Sandbox of
// its own (ensureSandbox). The tree's cleanup reservation calls it (store.CleanupReservedTree)
// once every stored claim of the tree has retired, or runs nothing and belongs to an issue that
// moved, so no launch creates a Sandbox of the tree meanwhile. The API's own listing by the
// tree's label is the census: each Sandbox it names is deleted, in any order, and awaited, and the
// listing is read again until none is left. Each delete is fenced to the listed object's UID and
// resourceVersion, and one the object changed since — the precondition the API refuses with 409,
// as a controller writing status through a foreground delete does — lists again after a recheck
// interval rather than spinning the census; a Sandbox the new tree's launch relabelled meanwhile
// fails the same fence and the next listing no longer names it, so it is left to that tree. A
// Sandbox already gone is gone, so a retry after a failure finishes the rest.
func (r *Runtime) CleanupTree(ctx context.Context, tree string) error {
	for {
		sandboxes, err := r.treeSandboxes(ctx, tree)
		if err != nil {
			return err
		}
		if len(sandboxes) == 0 {
			return nil
		}
		for _, object := range sandboxes {
			err := r.deleteSandbox(ctx, object)
			if apierrors.IsConflict(err) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(recheckInterval):
				}
				break
			}
			if err != nil {
				return err
			}
		}
	}
}

// treeSandboxes lists tree's issue Sandboxes from the API, the image probe's left out.
func (r *Runtime) treeSandboxes(ctx context.Context, tree string) ([]*unstructured.Unstructured, error) {
	list, err := r.sandboxClient().List(ctx, metav1.ListOptions{
		LabelSelector: labelProject + "=" + r.project + "," + labelTree + "=" + labelValue(tree),
	})
	if err != nil {
		return nil, fmt.Errorf("cleanup tree %s: list its Sandboxes: %w", tree, err)
	}
	var sandboxes []*unstructured.Unstructured
	for i := range list.Items {
		if object := &list.Items[i]; object.GetLabels()[labelProbe] == "" {
			sandboxes = append(sandboxes, object)
		}
	}
	return sandboxes, nil
}

// deleteSandbox deletes object with foreground propagation, fenced to its UID and resourceVersion,
// forgets its launchers' credentials once the API has accepted the delete, records that this
// runtime's delete took it (noteDeleted), and waits until the API no longer has it. Every delete is
// foreground: the Sandbox owns its role Secrets by owner reference — and an issue Sandbox that owns
// an `issue` claim template also the PVC that the template made: a kept volume (keptVolume), or the
// disk of the release before the pod-owned disk — and foreground propagation has garbage collection
// remove them before the Sandbox is gone, so the Sandbox's absence is the API's confirmation that
// what it owned is gone — the restricted daemon identity has no PVC verb to confirm it with. An
// issue's ephemeral disk is not among them: it is its pod's own, and goes when the delete takes the
// pod. A conflict is returned as it is.
func (r *Runtime) deleteSandbox(ctx context.Context, object *unstructured.Unstructured) error {
	s, err := decodeSandbox(object)
	if err != nil {
		return err
	}
	if err := r.guardSessionVolumeReap(ctx, s); err != nil {
		return err
	}
	name, uid, version := object.GetName(), object.GetUID(), object.GetResourceVersion()
	policy := metav1.DeletePropagationForeground
	options := metav1.DeleteOptions{
		PropagationPolicy: &policy,
		Preconditions:     &metav1.Preconditions{UID: &uid, ResourceVersion: &version},
	}
	err = r.sandboxClient().Delete(ctx, name, options)
	if err != nil && !apierrors.IsNotFound(err) {
		if apierrors.IsConflict(err) {
			return err
		}
		return fmt.Errorf("delete Sandbox %s: %w", name, err)
	}
	r.forgetLauncherCredentials(name)
	r.noteDeleted(name, uid)
	return r.awaitSandboxDeleted(ctx, name)
}

// sandboxTaken is what one of this runtime's own writes took from the Sandbox of uid before the
// informer's store shows it (Runtime.taken): its delete, or, where released is true, a release's
// record on a Sandbox it kept that its Secrets are deleted (secretsReleasedAnnotation).
type sandboxTaken struct {
	uid      types.UID
	released bool
}

// noteDeleted records that this runtime's delete took the Sandbox name of uid (taken), once the
// API has accepted the delete or answered the object already gone. It records nothing where the
// informer's store no longer holds that object: the store has caught up, and the event that told
// it has already run, or is about to run, the notify that would drop the record, so a record
// written then would outlive it. Both reads are under mu, which notify holds for its own.
func (r *Runtime) noteDeleted(name string, uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, err := r.storedObject(name); err == nil && u != nil && u.GetUID() == uid {
		r.taken[name] = sandboxTaken{uid: uid}
	}
}

// noteReleased records that this runtime's release wrote its record that the Secrets are deleted
// on the Sandbox name of uid, which it kept (taken), once the API has answered that patch. It
// records nothing where the store no longer holds that object, or already shows the record, for
// the reason noteDeleted gives: IssueSandbox reads such a stored Sandbox absent of itself.
func (r *Runtime) noteReleased(name string, uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, err := r.storedObject(name); err == nil && u != nil && u.GetUID() == uid && !recordsRelease(u, uid) {
		r.taken[name] = sandboxTaken{uid: uid, released: true}
	}
}

// forgetTaken drops what is kept of this runtime's write to the Sandbox name (taken): a launch
// that takes a release's record off a kept Sandbox makes it the issue's Sandbox again, whatever
// version of it the store holds.
func (r *Runtime) forgetTaken(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.taken, name)
}

// takenHere is whether the Sandbox name of uid is one this runtime's own write took and the store
// does not show yet (taken).
func (r *Runtime) takenHere(name string, uid types.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	took, held := r.taken[name]
	return held && took.uid == uid
}

// recordsRelease is whether u is the object of uid and carries a release's record, with its own
// uid, that its Secrets are deleted (secretsReleasedAnnotation), read in place.
func recordsRelease(u *unstructured.Unstructured, uid types.UID) bool {
	if u == nil || u.GetUID() != uid {
		return false
	}
	record, _, _ := unstructured.NestedString(u.Object, "metadata", "annotations", secretsReleasedAnnotation)
	return record != "" && record == string(uid)
}

// deleteStoredSandbox deletes the Sandbox name as the API holds it now, in the foreground, and
// returns once the API no longer has it; one already gone is gone. Each delete is fenced to the
// object as read (deleteSandbox), so one the Sandbox controller wrote in between — a status write —
// is refused as a conflict, read again and deleted again after a recheck interval. Both callers
// hold the pod's launch turn, so nothing of this daemon recreates the Sandbox under the loop: the
// end of an issue's pod (endIssuePod: a close's SuspendIssue, and a release's ReleaseIssue of a
// Sandbox that keeps no volume), and a launch replacing an issue Sandbox of another disk shape
// (replaceSupersededSandbox). Each logs the delete it wanted at its own call site.
func (r *Runtime) deleteStoredSandbox(ctx context.Context, name string) error {
	for {
		object, err := r.sandboxClient().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read sandbox %s: %w", name, err)
		}
		if err := r.deleteSandbox(ctx, object); !apierrors.IsConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(recheckInterval):
		}
	}
}

// awaitSandboxDeleted waits, up to the boot timeout, until the API no longer has the Sandbox name.
func (r *Runtime) awaitSandboxDeleted(ctx context.Context, name string) error {
	deadline := time.NewTimer(r.bootTimeout)
	defer deadline.Stop()
	for {
		_, err := r.sandboxClient().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("confirm Sandbox %s deletion: %w", name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("confirm Sandbox %s deletion: timed out after %s", name, r.bootTimeout)
		case <-time.After(recheckInterval):
		}
	}
}
