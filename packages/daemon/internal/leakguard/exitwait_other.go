//go:build !linux

package leakguard

// exitedNoReap is false on a system with no waitid that leaves a process unreaped (darwin's has
// no WNOWAIT, and a wait that reaped the child would take its status from the exec.Cmd that
// started it). It waits only until read is closed, the reader having read to the pipe's end, so a
// read there ends with the pipe or the caller's context, as a read before this change did.
func exitedNoReap(_ int, read <-chan struct{}) bool {
	<-read
	return false
}
