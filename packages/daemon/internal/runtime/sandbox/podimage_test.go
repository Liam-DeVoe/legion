package sandbox

import (
	"testing"
	"time"
)

// The image the live pod of an issue's Sandbox runs, asked by the issue alone (IssuePodImage),
// which is what the daemon reads before it starts a reviewer in that pod. A pod a launch would
// keep is answered with its uid, the image it runs and the configured image: the two are the same
// under the runtime that launched it, and differ under one whose configuration moved to another
// image. A pod under a suspended Sandbox is on its way out and a launch makes a fresh pod on the
// configured image, so it is no live pod, nor is an issue with no Sandbox at all.
func TestIssuePodImageReadsTheImageTheIssuesLivePodRuns(t *testing.T) {
	g := newRig(t, nil)
	// The pod of a suspended Sandbox stays terminating, so the suspended reading below is of a pod
	// that is still there.
	g.suspendDelay.Store(int64(time.Minute))
	loc := g.spawn(workerSpec(t))
	name := loc.Sandbox.Name

	if read, live := g.r.IssuePodImage(testTree); !live || read != (PodImage{Pod: loc.Sandbox.PodUID, Image: testImage, Configured: testImage}) {
		t.Errorf("IssuePodImage of %s under the runtime that launched its pod = %+v, live %t; want pod %s on %s, the configured image",
			testTree, read, live, loc.Sandbox.PodUID, testImage)
	}

	moved := secondRuntime(t, g, func(o *Options) { o.Image = nextImage })()
	g.eventually("the moved runtime to read the issue's pod", func() bool {
		_, live := moved.IssuePodImage(testTree)
		return live
	})
	if read, live := moved.IssuePodImage(testTree); !live || read != (PodImage{Pod: loc.Sandbox.PodUID, Image: testImage, Configured: nextImage}) {
		t.Errorf("IssuePodImage of %s under a runtime configured with %s = %+v, live %t; want pod %s on %s",
			testTree, nextImage, read, live, loc.Sandbox.PodUID, testImage)
	}

	suspendSandbox(t, g, moved, name)
	if pod := g.pod(name); pod == nil || string(pod.UID) != loc.Sandbox.PodUID {
		t.Fatalf("the pod of %s went with the suspension, so this case reads a pod that is gone rather than one on its way out", name)
	}
	if read, live := moved.IssuePodImage(testTree); live {
		t.Errorf("IssuePodImage of %s under a suspended Sandbox = %+v, live; want no live pod", testTree, read)
	}

	if read, live := moved.IssuePodImage("LEGION-999"); live {
		t.Errorf("IssuePodImage of an issue with no Sandbox = %+v, live; want no live pod", read)
	}
}
