//go:build linux

package storetest

import "os"

// FORCED_NUMA_BALANCING=1 makes the rig read NUMA balancing as on, for a forced run on a machine
// it leaves alone (packages/envoy/forced/storetest-numa.sh): with the memory-policy calls refused
// too, that is a machine whose kernel balances memory and whose container cannot keep it off.
func init() {
	if os.Getenv("FORCED_NUMA_BALANCING") == "1" {
		xidNUMABalancing = func() bool { return true }
	}
}
