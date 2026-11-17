package daemon

import (
	"log/slog"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
)

// leakCheckNotConfigured is the boot's line for a deployment that names no shared public leak
// check (config's leak_check): what is left out, the two settings that would name one, and what
// running without one means. It is a plain Info line and not a warning: a deployment with no
// check of its own, a turnkey install among them, is one the daemon runs as designed.
const leakCheckNotConfigured = "daemon: the deployment leak check is not configured (" +
	leakguard.RepositoryVar + " and " + leakguard.CheckerVar + ", or leak_check.repository and " +
	"leak_check.checker_sha256 in legion.yaml): role pods start without it, and no write is refused for its absence"

// logLeakCheck is the boot's one line on the deployment's shared public leak check: under a
// setting that names none, leakCheckNotConfigured, once; under one that names a check, nothing,
// since the check then runs as it always has.
func logLeakCheck(log *slog.Logger, setting leakguard.Setting) {
	if setting.Configured() {
		return
	}
	log.Info(leakCheckNotConfigured)
}
