package sandbox

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// The folder of the leak check's index of sandbox lines, as the interface between the check and
// Legion names it: where every role reads the index, leakguard.IndexDir under the home of the
// image's user legion, and where an issue pod's roles container holds that folder, under its home
// mounts directory, which role-init binds back into every role's home. The controller's pod has no
// role namespaces, so its launcher writes the folder every role reads.
const (
	roleIndexDir  = "/home/legion/.cache/public-leak-check"
	issueIndexDir = "/var/run/legion/home/.cache/public-leak-check"
)

// Every pod Legion launches, an issue pod and the controller's pod, in each shape the goldens
// pin, carries the volume the daemon's index pushes land in: an emptyDir on the node's disk of
// 64Mi, which holds the index and the new one an install writes beside it before its rename, each
// at most the 16 MiB a push carries. Its launcher container mounts it at the folder that container
// holds and tells its launcher that folder (--leak-check-index-dir), so the launcher writes its
// pushed marker and every index there, and the check in every role finds them at its home's
// .cache/public-leak-check. A pod whose launcher were told no folder would write no marker, and
// the check in its roles would download the index itself; one told a folder no volume holds would
// write into the image's home, which each role's overlay hides from the others.
func TestEveryPodMountsTheLeakCheckIndexVolume(t *testing.T) {
	if want := "/home/legion/" + leakguard.IndexDir; roleIndexDir != want {
		t.Fatalf("the interface's folder is %s, and the check's own name for it is %s", roleIndexDir, want)
	}
	size := resource.MustParse("64Mi")
	if held := resource.NewQuantity(2*shimwire.MaxLeakCheckIndexBytes, resource.BinarySI); size.Cmp(*held) < 0 {
		t.Fatalf("the volume's %s cannot hold an index and the new one installed beside it, %s", size.String(), held.String())
	}
	r, err := configure(goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range manifestCases(t) {
		t.Run(name, func(t *testing.T) {
			tc.apply(r)
			l := launchOf(t, r, tc.spec)
			pod := r.podTemplate(l).Spec
			held := issueIndexDir
			if _, controller := l.kind.(controllerPod); controller {
				held = roleIndexDir
			}
			var volumes []corev1.Volume
			for _, volume := range pod.Volumes {
				if volume.Name == "leak-check-index" {
					volumes = append(volumes, volume)
				}
			}
			if len(volumes) != 1 {
				t.Fatalf("the pod carries %d volumes named leak-check-index (%+v), want one", len(volumes), volumes)
			}
			if dir := volumes[0].EmptyDir; dir == nil || dir.Medium != corev1.StorageMediumDefault || dir.SizeLimit == nil || dir.SizeLimit.Cmp(size) != 0 {
				t.Errorf("the leak-check-index volume is %+v, want an emptyDir on the node's disk of %s", volumes[0].VolumeSource, size.String())
			}
			c := containerNamed(t, pod, l.kind.container())
			mount := mountHolding(c, held+"/"+leakguard.IndexFile)
			if mount == nil || mount.Name != "leak-check-index" || mount.MountPath != held || mount.ReadOnly || mount.SubPath != "" {
				t.Errorf("the index at %s/%s is on mount %+v, want the leak-check-index volume mounted read-write at %s", held, leakguard.IndexFile, mount, held)
			}
			var told []string
			for i, word := range c.Command {
				if word == "--leak-check-index-dir" && i+1 < len(c.Command) {
					told = append(told, c.Command[i+1])
				}
			}
			if !slices.Equal(told, []string{held}) {
				t.Errorf("the launcher is told the index folders %q (%q), want %s once", told, c.Command, held)
			}
		})
	}
}

// An operator's mount over the index folder would hide the volume the launcher writes the index
// into, and one above it would hide the volume too: so CheckPod refuses a mount at the home's
// .cache, at the folder and under it, naming the folder as one of Legion's, in the words the
// upgrade note in docs/kubernetes.md quotes. A mount beside the folder under .cache stays the
// operator's, as TestCheckPodRefusesAMountARolesNamespaceHides holds of /home/legion/.cache/operator:
// role-init carries it back into every role. The volume's name is Legion's too.
func TestCheckPodRefusesAMountOverTheLeakCheckIndex(t *testing.T) {
	mountAt := func(path string) Pod {
		return Pod{
			Volumes:      []corev1.Volume{{Name: "share", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			VolumeMounts: []corev1.VolumeMount{{Name: "share", MountPath: path}},
		}
	}
	tools := testOptions().Tools
	for _, path := range []string{"/home/legion/.cache", roleIndexDir, roleIndexDir + "/x"} {
		want := "runtime.kubernetes.pod.volume_mounts[0].mount_path " + path + " overlaps " + roleIndexDir +
			", which Legion mounts in every pod: a mount may be neither at, under, nor above one of Legion's"
		if err := CheckPod(mountAt(path), nil, tools, nil, nil, ompsessions.Store{}, false); err == nil || err.Error() != want {
			t.Errorf("a mount at %s = %v, want %q", path, err, want)
		}
	}
	if err := CheckPod(mountAt("/home/legion/.cache/operator"), nil, tools, nil, nil, ompsessions.Store{}, false); err != nil {
		t.Errorf("a mount at /home/legion/.cache/operator was refused: %v; role-init carries it back", err)
	}
	named := Pod{Volumes: []corev1.Volume{{Name: "leak-check-index", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}
	want := "runtime.kubernetes.pod.volumes[0].name leak-check-index is a volume Legion puts in every pod"
	if err := CheckPod(named, nil, tools, nil, nil, ompsessions.Store{}, false); err == nil || err.Error() != want {
		t.Errorf("a volume named leak-check-index = %v, want %q", err, want)
	}
}
