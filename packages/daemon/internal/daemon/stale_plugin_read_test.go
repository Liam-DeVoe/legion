package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// stalePassReadFailed is the line the stale-plugin pass writes for a claim whose read of whether
// its launch installs a plugin failed (refreshStalePlugins).
const stalePassReadFailed = "supervise: the stale-plugin pass could not read whether this claim's launch installs a plugin, so it decides nothing for the claim; the next sweep reads it again"

// staleProbers are n stale idle probers, each of an issue of its own, with a recorded session and
// a locator: claims the stale-plugin pass reads the plan of under Kubernetes, since prober is none
// of Legion's roles.
func staleProbers(t *testing.T, n int) []supervise.Claim {
	t.Helper()
	project, err := claim.ProjectToken("legion")
	if err != nil {
		t.Fatal(err)
	}
	claims := make([]supervise.Claim, 0, n)
	for i := range n {
		issue := fmt.Sprintf("LEGION-%d", 300+i)
		token, err := claim.NewToken(project, issue, "prober")
		if err != nil {
			t.Fatal(err)
		}
		claims = append(claims, supervise.Claim{
			Token: token, Project: project, Tree: issue, Issue: issue, Role: "prober", Generation: 1,
			State: supervise.StateIdle, Session: "ses_" + issue, PluginContract: api.DaemonAPIVersion - 1, Locator: pluginTestLocator(token),
		})
	}
	return claims
}

// stalePassSupervision is a supervision over a bare supervisor (newPluginSupervisor) of a
// Kubernetes daemon that cannot relaunch onto a plugin, logging to logs, whose read of a claim's
// harness is read (supervision.backends).
func stalePassSupervision(t *testing.T, logs *syncBuffer, read roleBackend) *supervision {
	t.Helper()
	sup, _ := newPluginSupervisor(t, api.DaemonAPIVersion, false)
	return &supervision{
		cfg: config.Config{Runtime: config.Runtime{Name: "kubernetes"}}, log: jsonLogger(logs), supervisor: sup,
		plan: plan{pluginRelaunchable: false}, refreshGate: supervise.NewRefreshGate(0), backends: read,
	}
}

// A read the stale-plugin pass makes that fails while the daemon runs is the store's fault, and
// one line names it for each claim. A read that fails because the pass's context ended, the
// daemon stopping, says nothing of the store, and the pass writes no line for it: a stop over a
// fleet would otherwise write one line for each stale claim, each saying that a sweep that never
// comes reads it again. The stop comes during the first read, so the pass reads a claim's plan
// and the read fails for the stop, which is the failure the pass must not log.
func TestTheStalePluginPassLogsAFailedReadOnlyWhileTheDaemonRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		stopping bool
		lines    int
	}{
		{"while the daemon runs", false, 2},
		{"once the daemon is stopping", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuffer{}
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			reads := 0
			read := func(ctx context.Context, issue string, role claim.Role) (runtime.Backend, error) {
				reads++
				if tc.stopping {
					stop()
				}
				if err := ctx.Err(); err != nil {
					return "", fmt.Errorf("the role %s of the plan of %s: %w", role, issue, err)
				}
				return "", errors.New("the role " + string(role) + " of the plan of " + issue + ": connection refused")
			}
			s := stalePassSupervision(t, logs, read)
			s.refreshStalePlugins(ctx, staleProbers(t, 2), "sweep")
			if reads == 0 {
				t.Fatalf("the pass read no claim's plan, so it judged no failed read")
			}
			if lines := loggedLines(t, logs.String(), stalePassReadFailed); len(lines) != tc.lines {
				t.Fatalf("the pass wrote %d lines of a failed read, want %d; it logged:\n%s", len(lines), tc.lines, logs.String())
			}
		})
	}
}
