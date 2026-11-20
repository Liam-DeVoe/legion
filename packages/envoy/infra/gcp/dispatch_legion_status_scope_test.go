package gcp_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/gcp"
)

// legionStatusScope true renders DISPATCH_LEGION_STATUS_SCOPE=on on the app's container in each
// mode that runs Dispatch: a tailnet Dispatch with browser sign-in and without it, and the
// sandbox. It comes right after DISPATCH_SERVER_URL, and right after DISPATCH_PARENT_STATUS_RULE
// when parentStatusRule is set too. Unset, the app carries no such variable, so a deployment that
// leaves it unset renders the pod it always had: each committed golden of the app's pod renders
// byte for byte (TestDispatchPodWithoutOutsideInputsIsUnchanged). Only envoy-dispatch reads the
// setting, so the listener's and the front door's pods are the defaults' own.
func TestTheLegionStatusScopeInputRendersTheSetting(t *testing.T) {
	for _, row := range []struct {
		name string
		args func() *gcp.DispatchArgs
		// defaults is that args are the defaults' own, whose listener and front door pods have
		// committed goldens.
		defaults bool
	}{
		{"tailnet with sign-in", dispatchArgs, true},
		{"tailnet without sign-in", dispatchTailnetWithoutSignInArgs, false},
		{"sandbox", dispatchSandboxArgs, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			unset, err := runDispatch(t, row.args(), true)
			require.NoError(t, err)
			require.NotContains(t, appEnv(t, unset), "DISPATCH_LEGION_STATUS_SCOPE")

			args := row.args()
			args.LegionStatusScope = true
			m, err := runDispatch(t, args, true)
			require.NoError(t, err)
			require.Equal(t, "on", appEnv(t, m)["DISPATCH_LEGION_STATUS_SCOPE"])
			requireRightAfter(t, m, "DISPATCH_SERVER_URL", "DISPATCH_LEGION_STATUS_SCOPE")
			if row.defaults {
				requirePodGolden(t, m, "example-dispatch-listener", "dispatch-listener-pod.golden")
				requirePodGolden(t, m, "example-dispatch-tailnet", "dispatch-tailnet-pod.golden")
			}

			both := row.args()
			both.ParentStatusRule = true
			both.LegionStatusScope = true
			m, err = runDispatch(t, both, true)
			require.NoError(t, err)
			requireRightAfter(t, m, "DISPATCH_PARENT_STATUS_RULE", "DISPATCH_LEGION_STATUS_SCOPE")
		})
	}
}
