package sandbox

// What the runtime reads of the cause of a death the daemon tells in plain words
// (runtime.Observation.Cause, dispatch://INFRA-1117 R9): the memory limit of a `roles` container
// the kubelet ended OOMKilled, the container, use and request a node-pressure eviction's message
// names over its memory or its ephemeral-storage request, and the disk limit of an eviction for
// the pod's own disk. Each number is read where the end is, so the claims of the pod carry it
// whether or not the pod is still in the store when they are checked.

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// rolesReservation is the reservation the plain-cause tests' pod holds its `roles` container to:
// 19Gi of memory over an 8Gi request, and 80Gi of disk over a 6Gi request.
func rolesReservation() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("6Gi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("19Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("80Gi")},
	}
}

// reservedIssuePod is runningIssuePod with the pod's spec holding its `roles` container to
// reservation, which is where the runtime reads a limit the kubelet's own words do not give.
func reservedIssuePod(t *testing.T, reservation corev1.ResourceRequirements) *rig {
	t.Helper()
	labels := claimLabels(claim.RoleTester)
	name := SandboxName(workerToken)
	pod := podObject(name, recorded, sandboxUID, labels, runningStatus())
	pod.Spec.Containers = []corev1.Container{{Name: runtime.IssuePodContainer, Resources: reservation}}
	return newRig(t, []k8sruntime.Object{sandboxObject(t, name, sandboxUID, modeRunning, labels), pod}, withoutController())
}

// bytesOf is quantity in bytes, as the cause carries it.
func bytesOf(quantity string) int64 {
	q := resource.MustParse(quantity)
	return q.Value()
}

// leaveTheStore deletes the tester's pod and waits for the pod store to drop it, as the
// Agent Sandbox controller's delete of a failed pod does before a claim of it is checked.
func (g *rig) leaveTheStore() {
	g.t.Helper()
	name := SandboxName(workerToken)
	if err := g.kube.Tracker().Delete(podsGVR, testNamespace, name); err != nil {
		g.t.Fatal(err)
	}
	g.eventually("the pod store to drop the deleted pod", func() bool { return g.r.storedPod(name) == nil })
}

// An eviction for the pod's own disk carries no DisruptionTarget condition, and the kubelet's
// message names the limit the pod passed: the pod's own ephemeral-storage limit, a container's or
// an emptyDir's sizeLimit (upstream's podEphemeralStorageMessageFmt,
// containerEphemeralStorageMessageFmt and emptyDirMessageFmt). The death is marked as disk with that
// limit, and still is when the pod has left the store before the claim is checked, since the
// message leaves with the pod. A message that names ephemeral storage and no limit falls back to
// the limit the pod's spec holds it to. A message that shows no disk end is marked with no cause:
// a node's refusal at admission, which carries the same reason and no condition and passed no
// limit, and a node-pressure eviction for memory whose kubelet wrote no DisruptionTarget
// condition, which is no end of the pod's disk and must not be told as one with the spec's limit.
// Each end is read neither way, so it is still charged.
func TestPlainCauseADiskEvictionIsMarkedWithTheLimitThePodPassed(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		want          runtime.DeathCause
	}{
		{name: "the pod's own limit", message: "Pod ephemeral local storage usage exceeds the total limit of containers 10Gi. ",
			want: runtime.DeathCause{Kind: runtime.DeathDiskLimit, Limit: bytesOf("10Gi")}},
		{name: "a container's limit", message: `Container roles exceeded its local ephemeral storage limit "40Gi". `,
			want: runtime.DeathCause{Kind: runtime.DeathDiskLimit, Limit: bytesOf("40Gi")}},
		{name: "an emptyDir's sizeLimit", message: `Usage of EmptyDir volume "role-homes" exceeds the limit "4Gi". `,
			want: runtime.DeathCause{Kind: runtime.DeathDiskLimit, Limit: bytesOf("4Gi")}},
		{name: "a message naming ephemeral storage and no limit, read from the spec", message: lowOnDisk,
			want: runtime.DeathCause{Kind: runtime.DeathDiskLimit, Limit: bytesOf("80Gi")}},
		{name: "a node's refusal at admission", message: "The node had condition: [DiskPressure]. "},
		{name: "a node-pressure memory message with no disruption target",
			message: lowOnMemory + "Container roles was using 9437188Ki, request is 8Gi, has larger consumption of memory. "},
	} {
		for _, gone := range []bool{false, true} {
			name := tc.name
			if gone {
				name += ", the pod gone from the store"
			}
			t.Run(name, func(t *testing.T) {
				g := reservedIssuePod(t, rolesReservation())
				status := endedPodStatus(evictedReason)
				status.Message = tc.message
				g.update(g.pod(SandboxName(workerToken)), func(p *corev1.Pod) { p.Status = status })
				g.podEndHeld(recorded)
				if gone {
					g.leaveTheStore()
				}

				obs := g.observed(rolesLocator(workerToken, recorded))

				if obs.Kind != runtime.Gone || obs.PodEnd != "" || obs.Cause != tc.want {
					t.Fatalf("observed %s PodEnd=%q Cause=%+v (%s), want Gone read neither way with the cause %+v",
						obs.Kind, obs.PodEnd, obs.Cause, obs.Detail, tc.want)
				}
			})
		}
	}
}

// A `roles` container the kubelet ended OOMKilled used more than its memory limit, and its death
// carries that limit as the pod's spec holds it, wherever the end is read: off the container's own
// terminated state, off the pod that ended with it, and off the LastTerminationState of one the
// kubelet restarted whose role launcher reports no child. An end of another reason carries no
// cause, and neither does an OOMKilled container the spec gives no memory limit, which the kernel
// ended for the node's memory under no limit of its own: that one keeps the out-of-memory mark.
func TestPlainCauseAnOOMKilledRolesContainerCarriesItsMemoryLimit(t *testing.T) {
	name := SandboxName(workerToken)
	memoryLimit := runtime.DeathCause{Kind: runtime.DeathMemoryLimit, Limit: bytesOf("19Gi")}
	for _, tc := range []struct {
		name     string
		status   corev1.PodStatus
		launcher *shimwire.LauncherState
		want     runtime.DeathCause
	}{
		{name: "the container ended OOMKilled",
			status: oneContainerStatus(corev1.PodRunning, endedContainer(runtime.IssuePodContainer, outOfMemoryEnd)), want: memoryLimit},
		{name: "the pod ended with its container OOMKilled",
			status: oneContainerStatus(corev1.PodFailed, endedContainer(runtime.IssuePodContainer, outOfMemoryEnd)), want: memoryLimit},
		{name: "the kubelet restarted it and the role's launcher reports no child",
			status:   oneContainerStatus(corev1.PodRunning, restartedContainer(runtime.IssuePodContainer, outOfMemoryEnd)),
			launcher: &shimwire.LauncherState{}, want: memoryLimit},
		{name: "the container ended with reason Error",
			status: oneContainerStatus(corev1.PodRunning, endedContainer(runtime.IssuePodContainer, erroredEnd))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := reservedIssuePod(t, rolesReservation())
			g.update(g.pod(name), func(p *corev1.Pod) { p.Status = tc.status })
			g.eventually("the pod store to see "+tc.name, func() bool {
				pod := g.r.storedPod(name)
				return pod != nil && reflect.DeepEqual(pod.Status, tc.status)
			})
			if tc.launcher != nil {
				g.reportLauncher(workerToken, *tc.launcher)
			}

			obs := g.observed(rolesLocator(workerToken, recorded))

			if obs.Kind != runtime.Gone || obs.Cause != tc.want {
				t.Fatalf("observed %s Cause=%+v (%s), want Gone with the cause %+v", obs.Kind, obs.Cause, obs.Detail, tc.want)
			}
		})
	}
	t.Run("a container the spec gives no memory limit", func(t *testing.T) {
		unlimited := rolesReservation()
		delete(unlimited.Limits, corev1.ResourceMemory)
		g := reservedIssuePod(t, unlimited)
		g.update(g.pod(name), func(p *corev1.Pod) {
			p.Status = oneContainerStatus(corev1.PodFailed, endedContainer(runtime.IssuePodContainer, outOfMemoryEnd))
		})
		g.eventually("the pod store to see the ended pod", func() bool {
			pod := g.r.storedPod(name)
			return pod != nil && terminal(pod)
		})

		obs := g.observed(rolesLocator(workerToken, recorded))

		if obs.Kind != runtime.Gone || !obs.OutOfMemory || obs.Cause != (runtime.DeathCause{}) {
			t.Fatalf("observed %s OutOfMemory=%t Cause=%+v (%s), want Gone marked out of memory with no cause: there is no limit to name",
				obs.Kind, obs.OutOfMemory, obs.Cause, obs.Detail)
		}
	})
}

// lowOnMemory is the start of the message the kubelet's eviction manager writes on a pod it evicts
// for node memory pressure: upstream's nodeLowMessageFmt and thresholdMetMessageFmt, each sentence
// ending in a space, which a sentence per container of the pod over its request follows.
const lowOnMemory = "The node was low on resource: memory. Threshold quantity: 100Mi, available: 59860Ki. "

// An eviction for node memory pressure carries the DisruptionTarget condition whoever's memory
// filled the node, and its message names each container of the evicted pod using more memory than
// it requested (upstream's containerMessageFmt). One that names a container of this pod is the
// pod's own memory use: it is read neither way, so it is charged, and the death carries that
// container, its use and its request as the message gives them, the pod gone from the store or
// not. One whose message names no container of this pod — a name the pod's spec does not hold, or
// none at all — is a neighbour's, read as an end from outside the pod and continued with no cause.
// An end the daemon itself started is its own whatever the kubelet then wrote, so a pod the daemon
// is ending that the kubelet evicts continues no role and carries no cause.
func TestPlainCauseANodePressureEvictionNamingThisPodsContainerIsItsOwnMemoryUse(t *testing.T) {
	const act = "the daemon suspended the pod's sandbox to close or release its issue"
	ownUse := runtime.DeathCause{Kind: runtime.DeathMemoryRequest, Container: runtime.IssuePodContainer,
		Use: bytesOf("9437188Ki"), Request: bytesOf("8Gi")}
	for _, tc := range []struct {
		name, message string
		intended      bool
		gone          bool
		reading       runtime.PodEndReading
		want          runtime.DeathCause
	}{
		{name: "the message names this pod's container over its request",
			message: lowOnMemory + "Container roles was using 9437188Ki, request is 8Gi, has larger consumption of memory. ", want: ownUse},
		{name: "the same, the pod gone from the store", gone: true,
			message: lowOnMemory + "Container roles was using 9437188Ki, request is 8Gi, has larger consumption of memory. ", want: ownUse},
		{name: "the message names a container that is not this pod's",
			message: lowOnMemory + "Container planner was using 16082620Ki, request is 768Mi, has larger consumption of memory. ",
			reading: runtime.PodEndNotOurs},
		{name: "the message names no container", message: lowOnMemory, reading: runtime.PodEndNotOurs},
		{name: "the daemon was ending the pod", intended: true, reading: runtime.PodEndOurs,
			message: lowOnMemory + "Container roles was using 9437188Ki, request is 8Gi, has larger consumption of memory. "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := reservedIssuePod(t, rolesReservation())
			if tc.intended {
				g.r.meanToEndPod(recorded, act)
			}
			status := endedPodStatus(evictedReason, disruptionTarget("TerminationByKubelet"))
			status.Message = tc.message
			g.update(g.pod(SandboxName(workerToken)), func(p *corev1.Pod) { p.Status = status })
			if end := g.podEndHeld(recorded); end.reading != tc.reading {
				t.Fatalf("read %+v of the evicted pod, want the reading %q", end, tc.reading)
			}
			if tc.gone {
				g.leaveTheStore()
			}

			obs := g.observed(rolesLocator(workerToken, recorded))

			if obs.Kind != runtime.Gone || obs.PodEnd != tc.reading || obs.Cause != tc.want {
				t.Fatalf("observed %s PodEnd=%q Cause=%+v (%s), want Gone PodEnd=%q with the cause %+v",
					obs.Kind, obs.PodEnd, obs.Cause, obs.Detail, tc.reading, tc.want)
			}
		})
	}
}

// lowOnDisk is the start of the message the kubelet's eviction manager writes on a pod it evicts
// for node disk pressure, built as lowOnMemory is: upstream's nodeLowMessageFmt for
// ephemeral-storage and thresholdMetMessageFmt, each sentence ending in a space.
const lowOnDisk = "The node was low on resource: ephemeral-storage. Threshold quantity: 10Gi, available: 3Gi. "

// An eviction for node disk pressure carries the DisruptionTarget condition whoever's disk filled
// the node, and its message names each container of the evicted pod using more ephemeral storage
// than it requested, the container's own writable layer and logs (upstream's containerMessageFmt
// with ephemeral-storage). One that names a container of this pod is the pod's own disk use: it is
// read neither way, so it is charged, and the death carries that container, its use and its
// request as the message gives them, the pod gone from the store or not. One whose message names
// no container of this pod is a neighbour's, read as an end from outside the pod and continued
// with no cause, and so is one whose pod filled an emptyDir volume, whose use the message names
// no container for.
func TestPlainCauseADiskPressureEvictionNamingThisPodsContainerIsItsOwnDiskUse(t *testing.T) {
	const overRequest = "Container roles was using 7Gi, request is 6Gi, has larger consumption of ephemeral-storage. "
	ownUse := runtime.DeathCause{Kind: runtime.DeathDiskRequest, Container: runtime.IssuePodContainer,
		Use: bytesOf("7Gi"), Request: bytesOf("6Gi")}
	for _, tc := range []struct {
		name, message string
		gone          bool
		reading       runtime.PodEndReading
		want          runtime.DeathCause
	}{
		{name: "the message names this pod's container over its request", message: lowOnDisk + overRequest, want: ownUse},
		{name: "the same, the pod gone from the store", gone: true, message: lowOnDisk + overRequest, want: ownUse},
		{name: "the message names a container that is not this pod's",
			message: lowOnDisk + "Container planner was using 12Gi, request is 1Gi, has larger consumption of ephemeral-storage. ",
			reading: runtime.PodEndNotOurs},
		{name: "the pod's disk use was in an emptyDir, which names no container", message: lowOnDisk, reading: runtime.PodEndNotOurs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := reservedIssuePod(t, rolesReservation())
			status := endedPodStatus(evictedReason, disruptionTarget("TerminationByKubelet"))
			status.Message = tc.message
			g.update(g.pod(SandboxName(workerToken)), func(p *corev1.Pod) { p.Status = status })
			if end := g.podEndHeld(recorded); end.reading != tc.reading {
				t.Fatalf("read %+v of the evicted pod, want the reading %q", end, tc.reading)
			}
			if tc.gone {
				g.leaveTheStore()
			}

			obs := g.observed(rolesLocator(workerToken, recorded))

			if obs.Kind != runtime.Gone || obs.PodEnd != tc.reading || obs.Cause != tc.want {
				t.Fatalf("observed %s PodEnd=%q Cause=%+v (%s), want Gone PodEnd=%q with the cause %+v",
					obs.Kind, obs.PodEnd, obs.Cause, obs.Detail, tc.reading, tc.want)
			}
		})
	}
}
