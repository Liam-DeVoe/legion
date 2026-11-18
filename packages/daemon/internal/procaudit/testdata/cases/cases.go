// Package cases is the audit's own fixture: each test of it is one case the audit must decide.
package cases

import "os"

// procRoot is the /proc scan lists: the machine's unless a test sets another.
var procRoot = "/proc"

// stand says whether this process stands in for the one serve runs in.
var stand = func() bool { return false }

// scan lists procRoot.
func scan() int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return -1
	}
	return len(entries)
}

// serve scans only where stand says so.
//
// host-proc gate stand: a test passes it only where it sets stand.
func serve() int {
	if !stand() {
		return 0
	}
	return scan()
}
