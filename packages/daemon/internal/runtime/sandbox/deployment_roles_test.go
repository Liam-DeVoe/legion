package sandbox

import (
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/bootprobe"
	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The runtime holds the roles of its deployment (Options.DeploymentApps) as a field of its own:
// an overlay of a role the deployment defines is taken, the image probe is told it, and two
// runtimes built in one process each keep their own list, so no package variable holds it.
func TestTheRuntimeTakesADeploymentRolesOverlays(t *testing.T) {
	withChecker := testOptions()
	withChecker.DeploymentApps = appauth.DeploymentApps{"checker": appauth.Review}
	withChecker.RoleConfigFiles = map[claim.Role][]string{"checker": {"/etc/checker.yml"}}
	a, err := configure(withChecker)
	if err != nil {
		t.Fatalf("configure with the checker's overlays = %v, want no error", err)
	}
	if want := append(slices.Clone(claim.Roles), "checker"); !slices.Equal(a.issueRoles, want) {
		t.Errorf("the runtime's issue roles = %v, want %v", a.issueRoles, want)
	}
	command := containerNamed(t, a.probeManifest("legion-probe", ImageProbe{Contract: 5, RoleReferences: testRoleReferences}, time.Time{}).Spec.PodTemplate.Spec, probeContainer).Command
	at := slices.Index(command, "checker=/etc/checker.yml")
	if at < 1 || command[at-1] != bootprobe.RoleConfigFilesFlag {
		t.Errorf("the probe's command = %q, want %s checker=/etc/checker.yml", command, bootprobe.RoleConfigFilesFlag)
	}

	b, err := configure(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(a.roleConfigRoles(), "checker") {
		t.Errorf("after a second runtime without the checker was built, the first runtime's roles = %v, want the checker among them", a.roleConfigRoles())
	}
	if slices.Contains(b.roleConfigRoles(), "checker") {
		t.Errorf("a runtime whose deployment defines no role has the roles %v, want no checker", b.roleConfigRoles())
	}
}
