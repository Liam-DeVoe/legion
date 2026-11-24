//go:build linux && (amd64 || arm64)

// nomempolicy runs a command under a seccomp filter that answers EPERM to get_mempolicy,
// set_mempolicy and mbind and allows every other system call: what a container runtime's default
// seccomp profile does to a container whose bounding set lacks CAP_SYS_NICE. It is the forcing of
// storetest-numa.sh beside it, for a machine where the memory-policy calls are allowed.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	prSetNoNewPrivs   = 38
	prSetSeccomp      = 22
	seccompModeFilter = 2
	seccompRetAllow   = 0x7fff0000
	seccompRetErrno   = 0x00050000
	seccompRetKill    = 0x00000000
	// The audit architectures of linux/audit.h, which seccomp_data.arch holds.
	auditArchX8664   = 0xc000003e
	auditArchAArch64 = 0xc00000b7
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nomempolicy COMMAND [ARG...]")
		os.Exit(64)
	}
	arch := uint32(auditArchX8664)
	if runtime.GOARCH == "arm64" {
		arch = auditArchAArch64
	}
	runtime.LockOSThread()
	// seccomp_data is {int nr; u32 arch; ...}: a call of another architecture is killed, since
	// its numbers are not the ones below.
	filter := []syscall.SockFilter{
		{Code: syscall.BPF_LD | syscall.BPF_W | syscall.BPF_ABS, K: 4},
		{Code: syscall.BPF_JMP | syscall.BPF_JEQ | syscall.BPF_K, Jt: 1, K: arch},
		{Code: syscall.BPF_RET | syscall.BPF_K, K: seccompRetKill},
		{Code: syscall.BPF_LD | syscall.BPF_W | syscall.BPF_ABS, K: 0},
		{Code: syscall.BPF_JMP | syscall.BPF_JEQ | syscall.BPF_K, Jt: 3, K: syscall.SYS_GET_MEMPOLICY},
		{Code: syscall.BPF_JMP | syscall.BPF_JEQ | syscall.BPF_K, Jt: 2, K: syscall.SYS_SET_MEMPOLICY},
		{Code: syscall.BPF_JMP | syscall.BPF_JEQ | syscall.BPF_K, Jt: 1, K: syscall.SYS_MBIND},
		{Code: syscall.BPF_RET | syscall.BPF_K, K: seccompRetAllow},
		{Code: syscall.BPF_RET | syscall.BPF_K, K: seccompRetErrno | uint32(syscall.EPERM)},
	}
	program := syscall.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0); errno != 0 {
		fmt.Fprintln(os.Stderr, "nomempolicy: PR_SET_NO_NEW_PRIVS:", errno)
		os.Exit(70)
	}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilter, uintptr(unsafe.Pointer(&program))); errno != 0 {
		fmt.Fprintln(os.Stderr, "nomempolicy: PR_SET_SECCOMP:", errno)
		os.Exit(70)
	}
	// The filter is in place: show that it refuses the call, so a run in which it did not cannot
	// pass for a forced one.
	var mode int
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_GET_MEMPOLICY, uintptr(unsafe.Pointer(&mode)), 0, 0, 0, 0, 0); errno != syscall.EPERM {
		fmt.Fprintf(os.Stderr, "nomempolicy: get_mempolicy under the filter answered %v, want EPERM\n", errno)
		os.Exit(70)
	}
	fmt.Fprintln(os.Stderr, "nomempolicy: get_mempolicy, set_mempolicy and mbind now answer EPERM")
	path, err := exec.LookPath(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "nomempolicy:", err)
		os.Exit(127)
	}
	err = syscall.Exec(path, os.Args[1:], os.Environ())
	fmt.Fprintln(os.Stderr, "nomempolicy: exec:", err)
	os.Exit(126)
}
