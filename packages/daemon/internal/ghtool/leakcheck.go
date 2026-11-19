package ghtool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/procgroup"
)

// LeakCheckSource reads the shared public leak check at a tag, packed as the daemon serves it to a
// pod's launcher: the Kubernetes runtime's reader (runtime/sandbox's LeakCheckReader, a
// *leakguard.Source, which keeps one copy per tag), whose every error is one cause sentence
// (leakguard.Cause).
type LeakCheckSource interface {
	Get(ctx context.Context, tag string) (leakguard.Served, error)
}

// leakCheckDir is the directory under the daemon's state directory InstallLeakCheck installs the
// check into, beside the answers' own directories (Host.StateDir).
const leakCheckDir = "leak-check"

// InstallLeakCheck installs the shared public leak check source serves at tag into
// <stateDir>/leak-check, for the guard of a Kubernetes daemon's own gh tool (Host.LeakCheck), and
// answers the path of the installed check. Such a daemon's environment holds none of the three
// places the guard resolves a check from (leakguard's Resolve): the worker image carries the pin,
// LEGION_LEAK_CHECK_TAG, and not the check, which every pod's launcher installs for its own pod
// from what the daemon serves it. So the daemon installs its own copy the same way, with the
// launcher's own installer (leakguard.InstallServed: the unpack into a staging directory, the tag
// recorded beside the check, one `--help` run, and the rename over what the directory held), and
// reads it back as a role's shim reads its pod's install (leakguard.Installed), so the path it
// answers is a check of tag. The daemon reaps no child it did not start, so that run starts with
// procgroup.NoReaper. log takes the one line of an install that replaced what the directory held:
// the install of an earlier boot, at its tag.
//
// An error is the read's own cause sentence, the install's, or the setup failure a role's shim
// would refuse to start with where the install does not read back as tag's.
func InstallLeakCheck(ctx context.Context, source LeakCheckSource, tag, stateDir string, log *slog.Logger) (string, error) {
	served, err := source.Get(ctx, tag)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir, leakCheckDir)
	if err := leakguard.InstallServed(ctx, leakguard.ServedInstall{
		Dir: dir, Tag: tag, Body: served.Body, Start: procgroup.NoReaper,
		Log: func(format string, args ...any) { log.Info("answers: " + fmt.Sprintf(format, args...)) },
	}); err != nil {
		return "", errors.New(leakguard.Cause(err.Error(), nil))
	}
	return leakguard.Installed([]string{leakguard.TagVariable + "=" + tag}, dir)
}

// MachineLeakCheck is the shared public leak check of the machine a tmux daemon runs on, resolved
// in environ, the daemon's own, the way the daemon resolves a pane's for each launch
// (runtime/tmux's resolveLeakCheck): the guard's own resolution (leakguard's Resolve),
// PUBLIC_LEAK_CHECK, then public-leak-check.sh on PATH, then the pointer file under the
// configuration home. Its error names all three.
func MachineLeakCheck(environ []string) (string, error) {
	return leakguard.New(leakguard.Options{Env: environ}).Resolve()
}
