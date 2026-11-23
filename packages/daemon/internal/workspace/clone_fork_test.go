package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

// The one-shot credential's helper runs at once however many goroutines of the process start
// commands beside its write. The daemon starts git, gh and tmux from many goroutines, and git runs
// the helper by its path as soon as a clone or fetch asks for the credential. A child forked while
// the helper was open for writing holds it open until its own exec, and the kernel refuses to run
// a file some process holds open for writing (ETXTBSY, "text file busy"). Eight goroutines start
// true without a pause while this test makes the credential again and again and runs each helper
// as git does, with `get`, as soon as it is made.
func TestTheProvisioningHelperRunsWhileTheProcessForks(t *testing.T) {
	testexec.ForkBeside(t, 8)
	parent := t.TempDir()
	const runs = 400
	busy := 0
	var first error
	for range runs {
		credential, err := newProvisioningCredential(parent, testInstallationToken)
		if err != nil {
			t.Fatal(err)
		}
		helper := exec.Command(filepath.Join(credential.dir, "helper"), "get")
		helper.Env = append([]string{"PATH=" + os.Getenv("PATH")}, credential.env...)
		answer, err := helper.Output()
		switch {
		case errors.Is(err, syscall.ETXTBSY):
			busy++
			if first == nil {
				first = err
			}
		case err != nil:
			t.Fatalf("run the provisioning helper: %v", err)
		case !strings.Contains(string(answer), testInstallationToken):
			t.Fatalf("the provisioning helper answered %q, want the token it was made with", answer)
		}
		if err := credential.remove(); err != nil {
			t.Fatal(err)
		}
	}
	if busy != 0 {
		t.Errorf("%d of %d provisioning helpers were refused as busy while the process forked; the first: %v", busy, runs, first)
	}
}
