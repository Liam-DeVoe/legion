//go:build linux

package storetest

import (
	"errors"
	"fmt"
	"io/fs"
	"math/bits"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The rig reads a thread's clocks at both ends of every gap, and while the collector is held off
// an allocation there would be a first touch of a page the process never used: on a loaded
// machine, milliseconds of the thread's CPU in the kernel that the gap reads as the code's
// (runWithoutCollector). So a read allocates nothing, on a thread the rig holds (its clock open
// once, xidWatchThreadClock) or not. The reader before this rule, which read the file through
// os.ReadFile and split it with bytes.Fields, allocates on every read and fails here.
func TestReadingAThreadsClocksAllocatesNothing(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if readXidThreadClock().thread == 0 {
		// A kernel that shows no thread's clocks judges no gap (XidNoGapJudgment), so this stops.
		RequireXidGapJudgment(t)
		t.Fatal("this thread's scheduler clocks read nothing, yet the rig judges gaps")
	}
	if allocs := testing.AllocsPerRun(100, func() { readXidThreadClock() }); allocs != 0 {
		t.Fatalf("a read of a thread's clocks the rig does not hold allocates %v times, want none", allocs)
	}
	stop, ok := xidWatchThreadClock()
	if !ok {
		t.Fatal("open this thread's clock to hold it")
	}
	defer stop()
	if readXidThreadClock().thread == 0 {
		t.Fatal("a held thread's clock read nothing off its own descriptor")
	}
	if allocs := testing.AllocsPerRun(100, func() { readXidThreadClock() }); allocs != 0 {
		t.Fatalf("a read of a held thread's clocks allocates %v times, want none", allocs)
	}
}

// On a machine NUMA balancing runs on, a held thread allocates on its own node, as by default,
// but has no page moved for it while it is held, and gets back the policy it had when it is let
// go (xidHoldThread). On a machine NUMA balancing leaves alone the hold has nothing to hold, so it
// answers held, even in a container whose seccomp profile refuses the memory-policy calls (EPERM,
// as a runtime's default profile answers without CAP_SYS_NICE). The test asserts that answer
// first, on every machine. Under such a profile that fails a hold that makes one of the calls and
// heeds its answer, as this hold heeds each answer; a call whose answer the hold ignored would not
// show. Where NUMA balancing runs and the hold answers not held, the test asks the kernel itself
// (xidMempolicyRefusal): where it refuses the hold's calls, the rig holds no thread and judges no
// gap, and the test stops as the rig's self-tests do (RequireXidGapJudgment); where it permits
// them, the hold is at fault, and the test fails in every build.
//
// The policy itself is compared in subtests. A machine that refuses the read of a thread's
// policy, with NUMA balancing off, has no policy to compare and nothing the hold could change, so
// they skip there, saying so (threadMempolicy). Where NUMA balancing runs, the held policy must be
// MPOL_LOCAL as this kernel reports it, read back off the test's own thread (xidLocalPolicy).
func TestHoldingAThreadKeepsNUMABalancingOffItUntilItIsLetGo(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	xidRequireBalancingDetector(t)
	release, held := xidHoldThread()
	release()
	if !held && !xidNUMABalancing() {
		t.Fatal("the hold answered not held on a machine NUMA balancing leaves alone, where it has nothing to hold and makes no memory-policy call")
	}
	if !held {
		refused, answer := xidMempolicyRefusal()
		if !refused {
			t.Fatalf("the rig could not hold this thread out of NUMA balancing, though the kernel refuses neither of the hold's calls (%s): the hold is at fault", answer)
		}
		// The kernel refused a memory-policy call: the rig holds no thread, and judges no gap.
		t.Logf("the kernel refused the thread hold's memory-policy calls (%s), so the rig holds no thread out of NUMA balancing", answer)
		RequireXidGapJudgment(t)
		t.Fatal("the kernel refused the thread hold's memory-policy calls, yet the rig judges gaps")
	}
	t.Run("its memory policy while it is held and once it is let go", func(t *testing.T) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		before := threadMempolicy(t)
		release, held := xidHoldThread()
		if !held {
			t.Fatal("the rig could not hold this thread out of NUMA balancing")
		}
		during := threadMempolicy(t)
		release()
		if after := threadMempolicy(t); after != before {
			t.Errorf("a thread let go has memory policy %s, want the %s it had", after, before)
		}
		want := before
		if xidNUMABalancing() {
			want = xidLocalPolicy(t)
		}
		if during != want {
			t.Errorf("a held thread's memory policy is %s, want %s", during, want)
		}
	})
	// The let-go puts back a policy that is not the default by its own call (xidHoldThread's
	// release), which runs only where NUMA balancing runs and the kernel permits the calls: a thread
	// that starts at the default never reaches it. So here the thread starts at MPOL_PREFERRED, on a
	// node the kernel takes for it (xidPreferredNode), and gets the policy it had back at the end.
	t.Run("a policy that is not the default is put back when it is let go", func(t *testing.T) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		allowed, errno := xidGetPolicy(nil, xidMempolicyMemsAllowed)
		if (errno == unix.EPERM || errno == unix.ENOSYS) && !xidNUMABalancing() {
			t.Skipf("NUMA balancing moves no page on this machine, so the hold makes no memory-policy call and changes nothing, and the nodes this thread may use cannot be read here to choose one (get_mempolicy MPOL_F_MEMS_ALLOWED: %v)", errno)
		}
		if errno != 0 {
			t.Fatalf("read the nodes this thread may use (get_mempolicy MPOL_F_MEMS_ALLOWED): %v", errno)
		}
		text, err := os.ReadFile(xidHasMemoryPath)
		hasMemory, absent, err := xidNodesWithMemory(allowed.nodes, text, err)
		if err != nil {
			t.Fatalf("read the nodes with memory: %v", err)
		}
		if absent {
			t.Logf("%s could not be read, as it does not exist, so the node comes from the nodes this thread may use alone, %s", xidHasMemoryPath, xidNodesText(allowed.nodes))
		}
		node, ok := xidPreferredNode(allowed.nodes, hasMemory)
		if !ok {
			t.Skipf("no node this thread may use has memory, so the kernel takes no node to prefer: the thread may use nodes %s (get_mempolicy MPOL_F_MEMS_ALLOWED), and nodes %s have memory (%s)", xidNodesText(allowed.nodes), xidNodesText(hasMemory), xidHasMemoryPath)
		}
		before := threadMempolicy(t)
		preferred := xidPolicy{mode: xidMempolicyPreferred}
		preferred.nodes[node/64] |= 1 << (node % 64)
		errno = preferred.set()
		if (errno == unix.EPERM || errno == unix.ENOSYS) && !xidNUMABalancing() {
			t.Skipf("NUMA balancing moves no page on this machine, so the hold makes no memory-policy call and changes nothing, and this thread's memory policy cannot be set here to compare (set_mempolicy: %v)", errno)
		}
		if errno != 0 {
			t.Fatalf("set this thread's memory policy to MPOL_PREFERRED on node %d: %v", node, errno)
		}
		defer func() {
			if errno := before.set(); errno != 0 {
				t.Errorf("put back this thread's memory policy, %s: %v", before, errno)
			}
		}()
		release, held := xidHoldThread()
		if !held {
			t.Fatal("the rig could not hold this thread out of NUMA balancing")
		}
		release()
		if after := threadMempolicy(t); after != preferred {
			t.Errorf("a thread let go has memory policy %s, want the %s it had", after, preferred)
		}
	})
}

// The restore subtest starts the thread at MPOL_PREFERRED on a node the kernel takes for it: one
// the thread may use that has memory, since set_mempolicy answers EINVAL to a preferred node
// outside the thread's cpuset or without memory (mpol_set_nodemask). The nodes with memory are
// read off /sys/devices/system/node/has_memory; where that file does not exist, as under gVisor,
// the node comes from the nodes the thread may use alone (xidNodesWithMemory, xidPreferredNode).
func TestTheRestoreSubtestPrefersANodeTheThreadMayUseThatHasMemory(t *testing.T) {
	nodes := func(list ...int) (mask [16]uint64) {
		for _, node := range list {
			mask[node/64] |= 1 << (node % 64)
		}
		return mask
	}
	absent := &fs.PathError{Op: "open", Path: xidHasMemoryPath, Err: unix.ENOENT}
	for _, c := range []struct {
		name      string
		allowed   [16]uint64
		hasMemory string
		readErr   error
		node      int
		ok        bool
	}{
		{"the thread may use node 1 alone", nodes(1), "0-1\n", nil, 1, true},
		{"node 1 alone has memory", nodes(0, 1), "1\n", nil, 1, true},
		{"no node the thread may use has memory", nodes(0), "1\n", nil, 0, false},
		{"no has_memory file", nodes(0), "", absent, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			hasMemory, _, err := xidNodesWithMemory(c.allowed, []byte(c.hasMemory), c.readErr)
			if err != nil {
				t.Fatal(err)
			}
			if node, ok := xidPreferredNode(c.allowed, hasMemory); node != c.node || ok != c.ok {
				t.Errorf("the choice is node %d (found %v), want node %d (found %v)", node, ok, c.node, c.ok)
			}
		})
	}
	if _, _, err := xidNodesWithMemory(nodes(0), []byte("0-x\n"), nil); err == nil {
		t.Error("a has_memory file that is not a list of nodes was read as one")
	}
}

// The memory policy modes and the flag the NUMA tests use beside the rig's (linux/mempolicy.h).
const (
	xidMempolicyPreferred   = 1
	xidMempolicyMemsAllowed = 1 << 2
)

// xidHasMemoryPath lists the NUMA nodes with memory.
const xidHasMemoryPath = "/sys/devices/system/node/has_memory"

// xidPolicy is a memory policy as get_mempolicy reports it: its mode and its nodes.
type xidPolicy struct {
	mode  int
	nodes [16]uint64
}

// set makes p the calling thread's memory policy (set_mempolicy).
func (p xidPolicy) set() unix.Errno {
	if p.mode == xidMempolicyDefault {
		_, _, errno := unix.Syscall(unix.SYS_SET_MEMPOLICY, xidMempolicyDefault, 0, 0)
		return errno
	}
	_, _, errno := unix.Syscall(unix.SYS_SET_MEMPOLICY, uintptr(p.mode), uintptr(unsafe.Pointer(&p.nodes[0])), uintptr(len(p.nodes)*64))
	return errno
}

func (p xidPolicy) String() string {
	return fmt.Sprintf("mode %d with nodes %s", p.mode, xidNodesText(p.nodes))
}

// xidGetPolicy is the memory policy get_mempolicy reports with flags: the calling thread's, the
// mapping address lies in with MPOL_F_ADDR, or the nodes the thread may use, in nodes, with
// MPOL_F_MEMS_ALLOWED.
func xidGetPolicy(address unsafe.Pointer, flags uintptr) (p xidPolicy, errno unix.Errno) {
	_, _, errno = unix.Syscall6(unix.SYS_GET_MEMPOLICY, uintptr(unsafe.Pointer(&p.mode)),
		uintptr(unsafe.Pointer(&p.nodes[0])), uintptr(len(p.nodes)*64), uintptr(address), flags, 0)
	return p, errno
}

// xidLocalPolicy is MPOL_LOCAL as this kernel reports it, read off the calling goroutine's own
// thread: it sets MPOL_LOCAL, reads back the mode and the nodes get_mempolicy gives, and puts back
// the policy the thread had. A held thread's policy, and a held mapping's, must read the same.
// Linux 3.8 to 5.13 store MPOL_LOCAL as MPOL_PREFERRED and report it as that mode with no node
// (mm/mempolicy.c: mpol_new at v3.8, get_policy_nodemask at v5.13); from 5.14 it is a mode of its
// own (mpol_ops[MPOL_LOCAL] at v5.14); gVisor reports mode 4. So the test hard-codes neither. It
// is read only where NUMA balancing runs, which Linux offers from 3.14 (kernel.numa_balancing,
// kernel/sysctl.c at v3.14; v3.13 has no such setting), and every such kernel takes MPOL_LOCAL, a
// mode since 3.8 (include/uapi/linux/mempolicy.h at v3.8). So any answer but a refusal fails the
// test, EINVAL too; a refusal (EPERM, ENOSYS) stops it as the rig's self-tests do
// (RequireXidGapJudgment).
func xidLocalPolicy(t *testing.T) xidPolicy {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	failed := func(call string, errno unix.Errno) {
		t.Helper()
		if errno == unix.EPERM || errno == unix.ENOSYS {
			t.Logf("the kernel refused the test's own %s (%v), so the rig judges no gap", call, errno)
			RequireXidGapJudgment(t)
		}
		t.Fatalf("%s: %v", call, errno)
	}
	had, errno := xidGetPolicy(nil, 0)
	if errno != 0 {
		failed("read of this thread's memory policy, to put it back", errno)
	}
	if errno := (xidPolicy{mode: xidMempolicyLocal}).set(); errno != 0 {
		failed("set_mempolicy MPOL_LOCAL", errno)
	}
	local, readErrno := xidGetPolicy(nil, 0)
	putErrno := had.set()
	if readErrno != 0 {
		failed("read back of MPOL_LOCAL", readErrno)
	}
	if putErrno != 0 {
		failed(fmt.Sprintf("set_mempolicy putting back %s", had), putErrno)
	}
	return local
}

// xidPreferredNode is the lowest node both in allowed, the nodes the thread may use, and in
// hasMemory, the nodes with memory: set_mempolicy takes a preferred node only from both. found is
// false where no node is in both.
func xidPreferredNode(allowed, hasMemory [16]uint64) (node int, found bool) {
	for word := range allowed {
		if both := allowed[word] & hasMemory[word]; both != 0 {
			return word*64 + bits.TrailingZeros64(both), true
		}
	}
	return 0, false
}

// xidNodesWithMemory is the set of nodes with memory, from what reading xidHasMemoryPath gave: its
// text and its error. Where the file does not exist (gVisor has no /sys/devices/system/node), it
// is allowed, the nodes the thread may use, and absent says so. A file that is there but cannot be
// read, or is not a list of nodes, is an error.
func xidNodesWithMemory(allowed [16]uint64, text []byte, readErr error) (hasMemory [16]uint64, absent bool, err error) {
	if errors.Is(readErr, fs.ErrNotExist) {
		return allowed, true, nil
	}
	if readErr != nil {
		return hasMemory, false, readErr
	}
	for _, part := range strings.Split(strings.TrimSpace(string(text)), ",") {
		if part == "" {
			continue
		}
		first, last, isRange := strings.Cut(part, "-")
		from, err := strconv.Atoi(first)
		to := from
		if err == nil && isRange {
			to, err = strconv.Atoi(last)
		}
		if err != nil || from < 0 || to < from || to >= len(hasMemory)*64 {
			return hasMemory, false, fmt.Errorf("read %s, %q, as a list of nodes", xidHasMemoryPath, text)
		}
		for node := from; node <= to; node++ {
			hasMemory[node/64] |= 1 << (node % 64)
		}
	}
	return hasMemory, false, nil
}

// xidNodesText names the nodes of a mask, as "{0,2}".
func xidNodesText(mask [16]uint64) string {
	var named []string
	for node := range len(mask) * 64 {
		if mask[node/64]&(1<<(node%64)) != 0 {
			named = append(named, strconv.Itoa(node))
		}
	}
	return "{" + strings.Join(named, ",") + "}"
}

// xidNUMABalancingFromFiles reads whether the kernel balances memory across NUMA nodes apart from
// the rig's detector (xidNUMABalancing): kernel.numa_balancing set to anything but 0, and more than
// one node directory under /sys/devices/system/node, counted rather than read off the online list
// as the detector reads it. A variable, so that a forced run can read balancing as on.
var xidNUMABalancingFromFiles = func() bool {
	setting, err := os.ReadFile("/proc/sys/kernel/numa_balancing")
	if err != nil || strings.TrimSpace(string(setting)) == "0" {
		return false
	}
	entries, err := os.ReadDir("/sys/devices/system/node")
	if err != nil {
		return false
	}
	nodes := 0
	for _, entry := range entries {
		if number, ok := strings.CutPrefix(entry.Name(), "node"); ok {
			if _, err := strconv.Atoi(number); err == nil {
				nodes++
			}
		}
	}
	return nodes > 1
}

// xidRequireBalancingDetector fails the calling test where the rig's detector of NUMA balancing
// and the test's own reading of the same files disagree: each NUMA test rests on the detector.
func xidRequireBalancingDetector(t *testing.T) {
	t.Helper()
	if detected, read := xidNUMABalancing(), xidNUMABalancingFromFiles(); detected != read {
		t.Fatalf("the rig's detector reads NUMA balancing as %v, and the test's own reading of /proc/sys/kernel/numa_balancing and /sys/devices/system/node reads it as %v", detected, read)
	}
}

// xidMempolicyRefusal asks the kernel whether it refuses the thread hold's memory-policy calls,
// get_mempolicy then set_mempolicy, the latter putting back the mode the former read, so the
// calling thread's policy is left as it was. A refusal is EPERM, as a seccomp profile answers
// without CAP_SYS_NICE, or ENOSYS, as a kernel built without NUMA answers; any other answer is
// not one. answer names what the kernel answered.
func xidMempolicyRefusal() (refused bool, answer string) {
	isRefusal := func(errno unix.Errno) bool { return errno == unix.EPERM || errno == unix.ENOSYS }
	var mode int
	var nodes [16]uint64
	_, _, errno := unix.Syscall6(unix.SYS_GET_MEMPOLICY, uintptr(unsafe.Pointer(&mode)),
		uintptr(unsafe.Pointer(&nodes[0])), uintptr(len(nodes)*64), 0, 0, 0)
	if errno != 0 {
		return isRefusal(errno), fmt.Sprintf("get_mempolicy answered %v", errno)
	}
	if mode == xidMempolicyDefault {
		_, _, errno = unix.Syscall(unix.SYS_SET_MEMPOLICY, xidMempolicyDefault, 0, 0)
	} else {
		_, _, errno = unix.Syscall(unix.SYS_SET_MEMPOLICY, uintptr(mode), uintptr(unsafe.Pointer(&nodes[0])), uintptr(len(nodes)*64))
	}
	if errno != 0 {
		return isRefusal(errno), fmt.Sprintf("get_mempolicy succeeded, and set_mempolicy of the mode it read answered %v", errno)
	}
	return false, "get_mempolicy and set_mempolicy both succeeded"
}

// threadMempolicy is the calling thread's memory policy. A kernel that refuses the read - a
// seccomp profile that refuses the memory-policy calls answers EPERM, and a kernel built without
// NUMA answers ENOSYS - on a machine NUMA balancing leaves alone skips the test, saying why: the
// hold makes no such call there and changes nothing, so there is no policy to compare. With NUMA
// balancing on, the hold makes the same call, and the test that held the thread has stopped on
// its refusal already, so a refusal here fails.
func threadMempolicy(t *testing.T) xidPolicy {
	t.Helper()
	policy, errno := xidGetPolicy(nil, 0)
	if (errno == unix.EPERM || errno == unix.ENOSYS) && !xidNUMABalancing() {
		t.Skipf("NUMA balancing moves no page on this machine, so the hold makes no memory-policy call and changes nothing, and this thread's memory policy cannot be read here to compare (get_mempolicy: %v)", errno)
	}
	if errno != 0 {
		t.Fatalf("read this thread's memory policy: %v", errno)
	}
	return policy
}

// On a machine NUMA balancing runs on, the window's memory is held out of it (xidHoldMemory): every
// private writable mapping of the process - here, the one a fresh heap object lies in, and the one
// this goroutine's stack lies in - has a policy of its own, MPOL_LOCAL, so no thread's fault moves
// a page of it and the balancing scan passes over it. With the hold planted to do nothing, the
// mappings keep the default policy and this test fails. On a machine NUMA balancing leaves alone
// there is nothing to hold: the hold answers held there, which under a seccomp profile that
// refuses mbind fails a hold that makes the call and heeds its answer, and the test then skips.
// Where NUMA balancing runs and the hold answers not held, the test asks the kernel itself: where
// it refuses mbind, the rig holds nothing and judges no gap, and the test stops as the rig's
// self-tests do (RequireXidGapJudgment); an unreadable /proc/self/maps, or an mbind the kernel
// permits, fails it by name. The mappings' policy must be MPOL_LOCAL as this kernel reports it
// (xidLocalPolicy).
func TestHoldingTheMemoryKeepsNUMABalancingOffItsMappings(t *testing.T) {
	xidRequireBalancingDetector(t)
	object := new([4096]byte)
	object[0] = 1
	var onStack byte
	held := xidHoldMemory()
	if !xidNUMABalancing() {
		if !held {
			t.Fatal("the memory hold answered not held on a machine NUMA balancing leaves alone, where it makes no mbind")
		}
		t.Skip("NUMA balancing moves no page on this machine, so there is no memory to hold")
	}
	if !held {
		if _, err := os.ReadFile("/proc/self/maps"); err != nil {
			t.Fatalf("the memory hold answered not held, and /proc/self/maps, which it reads the mappings off, cannot be read: %v", err)
		}
		page := uintptr(unsafe.Pointer(object)) &^ uintptr(os.Getpagesize()-1)
		_, _, errno := unix.Syscall6(unix.SYS_MBIND, page, uintptr(os.Getpagesize()), xidMempolicyLocal, 0, 0, 0)
		if errno != unix.EPERM && errno != unix.ENOSYS {
			t.Fatalf("the memory hold answered not held, though the kernel does not refuse mbind (mbind MPOL_LOCAL of a heap page answered %v, errno %d): the hold is at fault", errno, uintptr(errno))
		}
		t.Logf("the kernel refused mbind MPOL_LOCAL (%v), so the rig holds no memory out of NUMA balancing", errno)
		RequireXidGapJudgment(t)
		t.Fatal("the kernel refused the memory hold's mbind, yet the rig judges gaps")
	}
	want := xidLocalPolicy(t)
	for _, address := range []struct {
		name string
		at   unsafe.Pointer
	}{{"a heap object", unsafe.Pointer(object)}, {"this goroutine's stack", unsafe.Pointer(&onStack)}} {
		if got := mappingMempolicy(t, address.at); got != want {
			t.Errorf("the mapping of %s has memory policy %s after the hold, want %s, MPOL_LOCAL as this kernel reports it", address.name, got, want)
		}
	}
}

// mappingMempolicy is the memory policy of the mapping address lies in (MPOL_F_ADDR). A kernel
// that refuses the read (EPERM, ENOSYS) refuses the thread hold's get_mempolicy too, so the rig
// judges no gap, and the test stops as the rig's self-tests do (RequireXidGapJudgment).
func mappingMempolicy(t *testing.T, address unsafe.Pointer) xidPolicy {
	t.Helper()
	const byAddress = 1 << 1
	policy, errno := xidGetPolicy(address, byAddress)
	if errno == unix.EPERM || errno == unix.ENOSYS {
		t.Logf("the kernel refused get_mempolicy of the mapping at %p (%v), as it refuses the thread hold's, so the rig judges no gap", address, errno)
		RequireXidGapJudgment(t)
	}
	if errno != 0 {
		t.Fatalf("read the memory policy at %p: %v", address, errno)
	}
	return policy
}

// A /proc/self/maps address range is read as the addresses mbind takes, each parsed at the
// platform's integer size (xidMappingRange, xidMappingRangeAt). An address that does not fit is
// refused, not cut short: given a 32-bit platform's size, the range just above 4 GiB is refused,
// where parsing at 64 bits and converting, as before, would read it on a 32-bit build as the range
// at address 0. At 64 bits it is read whole, and only a range past 64 bits is refused. The module
// cannot be built for a 32-bit platform, so the 32-bit rows give the parser that size directly.
func TestAMappingRangeIsReadAtThePlatformsIntegerSize(t *testing.T) {
	for _, test := range []struct {
		field      string
		bits       int
		start, end uint64
	}{
		{field: "7f00-7f80", bits: 32, start: 0x7f00, end: 0x7f80},
		{field: "ffff0000-ffff1000", bits: 32, start: 0xffff0000, end: 0xffff1000},
		{field: "100000000-100001000", bits: 32},
		{field: "ffff0000-100000000", bits: 32},
		{field: "100000000-100001000", bits: 64, start: 0x100000000, end: 0x100001000},
		{field: "10000000000000000-10000000000001000", bits: 64},
		{field: "7f80-7f00", bits: 64},
		{field: "7f00", bits: 64},
		{field: "-7f00", bits: 64},
	} {
		start, end, ok := xidMappingRangeAt([]byte(test.field), test.bits)
		want := test.end != 0
		if ok != want || uint64(start) != test.start || uint64(end) != test.end {
			t.Errorf("xidMappingRangeAt(%q, %d) = %#x, %#x, %t, want %#x, %#x, %t",
				test.field, test.bits, start, end, ok, test.start, test.end, want)
		}
	}
	start, end, ok := xidMappingRange([]byte("100000000-100001000"))
	if fits := strconv.IntSize == 64; ok != fits || (fits && (start != 0x100000000 || end != 0x100001000)) {
		t.Errorf("at strconv.IntSize %d, xidMappingRange of the range above 4 GiB = %#x, %#x, %t", strconv.IntSize, start, end, ok)
	}
}

// The rig reads a schedstat line's fields in place (xidFields): the first three decimal fields,
// and no reading from a line that does not hold them.
func TestASchedstatLineIsReadInPlace(t *testing.T) {
	for _, test := range []struct {
		line string
		want []int64
	}{
		{"123 456 7\n", []int64{123, 456, 7}},
		{"0 0 1\n", []int64{0, 0, 1}},
		{"18446744073 9 12", []int64{18446744073, 9, 12}},
		{"123 456\n", nil},
		{"123  456 7\n", nil},
		{"", nil},
	} {
		var fields [3]int64
		ok := xidFields([]byte(test.line), fields[:])
		if test.want == nil {
			if ok {
				t.Errorf("xidFields(%q) read %v, want no reading", test.line, fields)
			}
			continue
		}
		if !ok || fields != [3]int64(test.want) {
			t.Errorf("xidFields(%q) = %v, %t, want %v", test.line, fields, ok, test.want)
		}
	}
}

// The window runs after a collection, with the collector already off (runWithoutCollector): with
// it off the heap only grows, so without that collection every allocation of the window would be
// the first touch of a page. The test reads the collector's cycle count inside the operation.
func TestTheWindowRunsAfterACollection(t *testing.T) {
	cycles := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
	metrics.Read(cycles)
	before := cycles[0].Value.Uint64()
	var during uint64
	var percent int
	runWithoutCollector(func() {
		metrics.Read(cycles)
		during = cycles[0].Value.Uint64()
		percent = debug.SetGCPercent(-1)
	})
	if during <= before {
		t.Errorf("the operation ran after %d collections, the same as before it: no collection ran first", during)
	}
	if percent != -1 {
		t.Errorf("the operation ran with GOGC %d, want the collector off", percent)
	}
}
