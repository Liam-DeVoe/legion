//go:build linux

package storetest

import "os"

// Scratch forcing, not for merge. FORCED_NUMA_BALANCING=1 makes the tests' own reading of NUMA
// balancing read it as on too, beside the rig's detector (forced_numa_linux_test.go).
func init() {
	if os.Getenv("FORCED_NUMA_BALANCING") == "1" {
		xidNUMABalancingFromFiles = func() bool { return true }
	}
}
