package sandbox

import (
	"context"
	"errors"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// A release that fails after authorize answered that the pod goes, and before it changed anything,
// says so (runtime.PodUntouchedError), which ends the release's record that its teardown took the
// pod (the daemon's releaseIssue): the Sandbox's read failed, it could not be decoded, it names
// another tree, or the API server refused the write that sets it Suspended — a conflict, an
// invalid patch (its test of the uid), a forbidden verb, a throttled request — and stored nothing.
// The Sandbox still runs its pod in each. The controls say no such thing, since the pod may be
// going: a write with no answer, one answered with a timeout or a server error, or one whose
// context ended, any of which the server may have applied; a delete that fails once the release
// has set the Sandbox Suspended; and a refused write over a Sandbox that was Suspended already,
// whose pod an earlier act is taking down.
func TestAReleaseThatFailsBeforeItTouchesThePodSaysSo(t *testing.T) {
	sandboxes := schema.GroupResource{Group: sandboxGVR.Group, Resource: sandboxGVR.Resource}
	refused := func(err error) func(*rig, string) k8stesting.ReactionFunc {
		return func(*rig, string) k8stesting.ReactionFunc {
			return func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, err }
		}
	}
	// served answers the release's read with the stored Sandbox as edit changes it.
	served := func(edit func(t *testing.T, u *unstructured.Unstructured)) func(*rig, string) k8stesting.ReactionFunc {
		return func(g *rig, name string) k8stesting.ReactionFunc {
			return func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				object, err := g.dyn.Tracker().Get(sandboxGVR, testNamespace, name)
				if err != nil {
					return true, nil, err
				}
				u := object.(*unstructured.Unstructured).DeepCopy()
				edit(g.t, u)
				return true, u, nil
			}
		}
	}
	for _, tc := range []struct {
		name      string
		verb      string
		react     func(*rig, string) k8stesting.ReactionFunc
		untouched bool
		mode      string
		// suspended has the Sandbox Suspended before the release, with a pod whose init container
		// failed on its kept volume, so the release's one write is the reset mark's.
		suspended bool
	}{
		{name: "the Sandbox's read fails", verb: "get", react: refused(errors.New("the API did not answer")),
			untouched: true, mode: modeRunning},
		{name: "the Sandbox does not decode", verb: "get", react: served(func(t *testing.T, u *unstructured.Unstructured) {
			if err := unstructured.SetNestedField(u.Object, int64(7), "spec", "operatingMode"); err != nil {
				t.Fatal(err)
			}
		}), untouched: true, mode: modeRunning},
		{name: "the Sandbox names another tree", verb: "get", react: served(func(_ *testing.T, u *unstructured.Unstructured) {
			labels := u.GetLabels()
			labels[labelTree] = "another-tree"
			u.SetLabels(labels)
		}), untouched: true, mode: modeRunning},
		{name: "the API server refuses the write as a conflict (409)", verb: "patch",
			react: refused(apierrors.NewConflict(sandboxes, "sandbox", errors.New("the object has been modified"))), untouched: true, mode: modeRunning},
		{name: "the API server refuses the write as invalid, its uid test failed (422)", verb: "patch",
			react: refused(apierrors.NewInvalid(schema.GroupKind{Group: sandboxGVR.Group, Kind: "Sandbox"}, "sandbox", nil)), untouched: true, mode: modeRunning},
		{name: "the API server forbids the write (403)", verb: "patch",
			react: refused(apierrors.NewForbidden(sandboxes, "sandbox", errors.New("a policy denied the patch"))), untouched: true, mode: modeRunning},
		{name: "the API server throttles the write (429)", verb: "patch",
			react: refused(apierrors.NewTooManyRequests("the server is throttling", 1)), untouched: true, mode: modeRunning},
		{name: "control: the write that suspends the Sandbox has no answer", verb: "patch",
			react: refused(errors.New("the API did not answer")), mode: modeRunning},
		{name: "control: the write's answer is a timeout (504)", verb: "patch",
			react: refused(apierrors.NewTimeoutError("the write took too long", 1)), mode: modeRunning},
		{name: "control: the write's answer is a server error (500)", verb: "patch",
			react: refused(apierrors.NewInternalError(errors.New("etcd is unavailable"))), mode: modeRunning},
		{name: "control: the write's context ended", verb: "patch", react: refused(context.DeadlineExceeded), mode: modeRunning},
		{name: "control: the write's answer is not found (404): the Sandbox is gone already", verb: "patch",
			react: refused(apierrors.NewNotFound(sandboxes, "sandbox")), mode: modeRunning},
		{name: "control: the write's answer is its request's timeout (408), which may have applied it", verb: "patch",
			react: refused(apierrors.NewGenericServerResponse(http.StatusRequestTimeout, "patch", sandboxes, "sandbox", "", 0, true)), mode: modeRunning},
		{name: "control: the write's answer is gone (410), which may have applied it", verb: "patch",
			react: refused(apierrors.NewGone("the object's version is gone")), mode: modeRunning},
		{name: "control: the Sandbox's delete fails once it is Suspended", verb: "delete",
			react: refused(errors.New("the API did not answer")), mode: modeSuspended},
		{name: "control: a refused write over a Sandbox Suspended already (403)", verb: "patch",
			react: refused(apierrors.NewForbidden(sandboxes, "sandbox", errors.New("a policy denied the patch"))), mode: modeSuspended, suspended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var g *rig
			var name string
			if tc.suspended {
				g = newRig(t, nil, withOptions(keptVolumes(SandboxMicroVM)))
				name = g.spawn(workerSpec(t)).Sandbox.Name
				g.editPod(name, "the pod store to see the failed init container", func(p *corev1.Pod) {
					p.Status = corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{
						{Name: fetchContainer, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}},
						{Name: initContainer, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}},
					}}
				})
				object, err := g.dyn.Tracker().Get(sandboxGVR, testNamespace, name)
				if err != nil {
					t.Fatal(err)
				}
				suspended := object.(*unstructured.Unstructured).DeepCopy()
				if err := unstructured.SetNestedField(suspended.Object, modeSuspended, "spec", "operatingMode"); err != nil {
					t.Fatal(err)
				}
				if err := g.dyn.Tracker().Update(sandboxGVR, suspended, testNamespace); err != nil {
					t.Fatal(err)
				}
				if s := g.sandbox(name); s == nil || s.mode() != modeSuspended || volumeFault(s, g.r.storedPod(name)) == "" {
					t.Fatalf("setup: the Sandbox %s is Suspended %t with the fault %q, want it Suspended with a pod that failed on its kept volume",
						name, s != nil && s.mode() == modeSuspended, volumeFault(s, g.r.storedPod(name)))
				}
			} else {
				g = newRig(t, nil)
				name = g.spawn(workerSpec(t)).Sandbox.Name
			}
			g.dyn.PrependReactor(tc.verb, "sandboxes", tc.react(g, name))

			err := g.r.ReleaseIssue(g.ctx, testTree, testTree, authorized)

			var untouched *runtime.PodUntouchedError
			if err == nil || errors.As(err, &untouched) != tc.untouched {
				t.Fatalf("the release = %v, untouched %t; want a failure, untouched %t", err, errors.As(err, &untouched), tc.untouched)
			}
			s := g.sandbox(name)
			if s == nil {
				t.Fatalf("the Sandbox %s is gone, want it standing", name)
			}
			if s.mode() != tc.mode {
				t.Fatalf("the Sandbox %s is %s after the failed release, want %s", name, s.mode(), tc.mode)
			}
		})
	}
}
