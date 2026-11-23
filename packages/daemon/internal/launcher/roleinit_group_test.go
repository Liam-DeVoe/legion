//go:build linux

package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The two re-entrant helpers of this file: the stage, which builds a role layout in a mount
// namespace of its own and runs one generation over it through role-init, as runRoleDrain does,
// and the stand-in shim that role-init starts, which does what its case names and prints
// `group <key>=<value>` lines. The stand-in's own children (a member that stops itself, the
// session and the group leader of the orphaned-by-exit control) are this binary again, with
// roleGroupShimEnv naming what each one is.
const (
	roleGroupStageEnv = "LEGION_ROLE_INIT_TEST_GROUP_STAGE"
	roleGroupShimEnv  = "LEGION_ROLE_INIT_TEST_GROUP_SHIM"
	// roleGroupControlEnv names the file the orphaned-by-exit control's group leader writes the
	// pid of its stopped member to, in the role's own /tmp.
	roleGroupControlEnv = "LEGION_ROLE_INIT_TEST_GROUP_CONTROL"
)

func init() {
	helperChildVariables = append(helperChildVariables, roleGroupStageEnv, roleGroupShimEnv)
}

// roleGroupBound bounds every wait the stage and the stand-in make for a process of the role to
// start, stop, end or be reaped: far past what a loaded machine takes for any of them, short of
// the stage's own hang guard (roleGroupReport).
const roleGroupBound = 30 * time.Second

// roleGroupPlan is what the stage builds and runs: a role layout under Dir, which it covers with a
// memory filesystem of its own, and one generation whose stand-in shim runs Case. Stop says the
// stage stops the generation as the launcher does (signalChild: SIGTERM to role-init alone), once
// the stand-in says it is ready, and gives it Grace before it kills role-init as endGeneration
// does at the end of the stop grace.
type roleGroupPlan struct {
	Dir   string
	Case  string
	Stop  bool
	Grace time.Duration
}

// A role's shim runs in role-init's own process group, so an orphan the role leaves in that group
// (a background sleep whose shell exits) is reparented to role-init and reaped by it inside the
// group: under the cluster's gVisor a member reaped by a parent in another group lowers the count
// that keeps the group from reading as orphaned, and reparenting never raises it again
// (runRoleShim). The stand-in reports, from inside the role's namespaces, its own process group
// and role-init's, and the orphan's parent and group while it still runs, then whether it was
// reaped once it ended (gone from /proc, not a zombie). Before the shim shared role-init's group
// (Setpgid in runRoleShim) the shim led a group of its own, and the orphan, in that group, had
// role-init for its parent from outside it: both comparisons below fail there.
func TestARolesOrphanIsReapedByRoleInitInsideItsOwnProcessGroup(t *testing.T) {
	requireRoleNamespaces(t)
	report, output := roleGroupReport(t, roleGroupPlan{Dir: stageMountPoint(t), Case: "orphan"})
	if report["status"] != "0" {
		t.Fatalf("the role's status = %q, want the stand-in shim's own exit 0\n%s", report["status"], output)
	}
	if report["init-group"] != "1" {
		t.Fatalf("role-init's process group reads %q inside the role, want 1, role-init's own: the stand-in read the wrong process\n%s", report["init-group"], output)
	}
	if report["shim-group"] != report["init-group"] {
		t.Errorf("the shim's process group is %q and role-init's is %q: the shim leads a group of its own, whose orphans role-init reaps from outside it\n%s",
			report["shim-group"], report["init-group"], output)
	}
	if report["orphan-parent"] != "1" {
		t.Errorf("the orphan's parent is %q, want role-init, the init of the role's PID namespace\n%s", report["orphan-parent"], output)
	}
	if report["orphan-group"] != report["init-group"] {
		t.Errorf("the orphan is in process group %q and its parent role-init in %q: role-init reaps it from another group\n%s",
			report["orphan-group"], report["init-group"], output)
	}
	if report["orphan"] != "reaped" {
		t.Errorf("the orphan is %q once it ended, want reaped by role-init\n%s", report["orphan"], output)
	}
}

// The role's process group never reads as orphaned while role-init lives, read through the signal
// the kernel sends a group that becomes orphaned with a stopped member: a member of the shim's
// group stops itself, an orphan of the group is reaped by role-init, and the stopped member must
// still be stopped, with no SIGHUP to it or to the stand-in. With the shim in a group of its own
// (Setpgid in runRoleShim) this row fails on the cluster's gVisor, which is the fault itself: in a
// builds Job on gVisor the member ended by hangup and the stand-in got SIGHUP. On Linux it passes
// either way, since Linux counts the parents the members have now, so there it guards the group's
// state. The positive control in the same role shows the probe sees a hangup where one comes: a
// group whose leader's exit really orphans it with a member stopped (a leader in a session of its
// own, whose member then has role-init, in another session, for its parent), whose member must be
// ended by the kernel's SIGHUP.
func TestARolesProcessGroupIsNotHungUpWhileRoleInitLives(t *testing.T) {
	requireRoleNamespaces(t)
	report, output := roleGroupReport(t, roleGroupPlan{Dir: stageMountPoint(t), Case: "hup"})
	if report["status"] != "0" {
		t.Fatalf("the role's status = %q, want the stand-in shim's own exit 0\n%s", report["status"], output)
	}
	if report["control-hup"] != "yes" {
		t.Fatalf("the orphaned-by-exit control's stopped member was %q, want ended by the kernel's SIGHUP: the probe cannot see a hangup\n%s", report["control-hup"], output)
	}
	if report["orphan"] != "reaped" {
		t.Fatalf("the role group's orphan is %q, want reaped by role-init: the case never reached what it checks\n%s", report["orphan"], output)
	}
	if report["member"] != "stopped" || report["shim-hup"] != "no" {
		t.Errorf("the role group's stopped member is %q and the shim got SIGHUP: %q, want it still stopped and no SIGHUP to the shim\n%s",
			report["member"], report["shim-hup"], output)
	}
}

// A normal stop of a role is what it was with the shim in a group of its own: the launcher sends
// SIGTERM to role-init alone (signalChild), role-init forwards one SIGTERM to every process of the
// role, the shim has the stop grace for its session save and exits with its own status, and the
// git-like descendant it leaves gets its SIGTERM before the namespace's SIGKILL. The stand-in
// counts the SIGTERMs it receives: one, as before, though it now shares role-init's group. It
// saves for 300 ms after the signal, and the stage gives role-init a stop grace far past that
// before it would kill it, so a save that is cut short or a role that needs the kill fails here.
// The drain after a shim that exits on its own is TestARoleWhoseShimExitsSignalsItsNamespaceBeforeItKills's.
func TestARoleStopKeepsItsGraceAndItsSaveWithTheShimInRoleInitsGroup(t *testing.T) {
	requireRoleNamespaces(t)
	report, output := roleGroupReport(t, roleGroupPlan{Dir: stageMountPoint(t), Case: "stop", Stop: true, Grace: roleGroupBound})
	if report["killed"] != "no" {
		t.Fatalf("role-init was killed at the end of the stop grace (%s), want it gone before\n%s", roleGroupBound, output)
	}
	if report["status"] != "0" {
		t.Errorf("the role's status = %q, want the stand-in shim's own exit 0 after its save\n%s", report["status"], output)
	}
	if report["sigterms"] != "1" {
		t.Errorf("the stand-in shim received %q SIGTERMs, want the one role-init forwards\n%s", report["sigterms"], output)
	}
	if report["saved"] != "present" {
		t.Errorf("the stand-in shim's session save is %q, want it written inside the stop grace\n%s", report["saved"], output)
	}
	if report["descendant"] != "absent" {
		t.Errorf("the file the role's descendant removes on SIGTERM is %q, want it gone: the descendant was killed with no signal before it\n%s",
			report["descendant"], output)
	}
}

// roleGroupReport runs the stage of plan in user, mount and PID namespaces of its own, and answers
// every `group <key>=<value>` line it and its role printed, with its whole output for a failure's
// message. Its deadline is a hang guard alone. The stage is the init of its PID namespace, so the
// guard's kill of the stage ends role-init and every process the role started with it: a role that
// hangs leaves nothing running behind the failed test.
func roleGroupReport(t *testing.T, plan roleGroupPlan) (map[string]string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	guard := testwait.Bounded(t, 3*time.Minute, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	stage := exec.CommandContext(ctx, self, "-test.run=^TestRoleGroupStageHelper$")
	stage.WaitDelay = time.Second
	stage.Env = append(os.Environ(), roleGroupStageEnv+"="+string(encoded))
	stage.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	output, err := stage.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the stage had not ended %s after it started, the hang guard: role-init or the stand-in shim never finished\n%s", guard, output)
	}
	if err != nil {
		t.Fatalf("the stage: %v\n%s", err, output)
	}
	report := map[string]string{}
	for line := range strings.Lines(string(output)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "group ")
		if !ok {
			continue
		}
		if key, value, ok := strings.Cut(rest, "="); ok {
			report[key] = value
		}
	}
	return report, string(output)
}

// TestRoleGroupStageHelper is the stage: it builds a role layout of its own on a memory filesystem
// under the plan's directory, runs one generation over it whose shim is the stand-in, stops it
// where the plan says so, and reports the shim's status and the files the stand-in leaves in the
// role's own /tmp once the role's namespace is gone.
func TestRoleGroupStageHelper(t *testing.T) {
	encoded := os.Getenv(roleGroupStageEnv)
	if encoded == "" {
		return
	}
	var plan roleGroupPlan
	err := json.Unmarshal([]byte(encoded), &plan)
	if err == nil {
		err = runRoleGroupStage(plan)
	}
	if err != nil {
		fmt.Println("stage error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runRoleGroupStage(plan roleGroupPlan) error {
	if err := privateMounts(); err != nil {
		return err
	}
	// The stage is the init of a PID namespace of its own (roleGroupReport), so it mounts that
	// namespace's /proc: /proc/self, which starts role-init and the stand-in, names this process
	// only there.
	if err := mountProc("/proc"); err != nil {
		return fmt.Errorf("mount the stage's own /proc: %w", err)
	}
	if err := mountTmpfs(plan.Dir, "size=16m"); err != nil {
		return err
	}
	roleInitLaunch = roleInitHelperLaunch(stageSelf)
	const role = "group"
	cfg := RoleInitConfig{
		RolesDir: filepath.Join(plan.Dir, "roles"), Role: role, Generation: 1,
		Home: filepath.Join(plan.Dir, "home"), HomeMounts: filepath.Join(plan.Dir, "home-mounts"),
		FixedRoot: filepath.Join(plan.Dir, "fixed"), Argv: []string{stageSelf, "-test.run=^TestRoleGroupShimHelper$"},
	}
	dirs := []string{cfg.Home, cfg.HomeMounts}
	for _, name := range []string{RoleGHDir, RolePrivateDir, RoleStateDir} {
		dirs = append(dirs, filepath.Join(cfg.RolesDir, role, name), filepath.Join(cfg.FixedRoot, name))
	}
	for _, path := range dirs {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	tmp := filepath.Join(cfg.RolesDir, RoleHomesDir, role, "tmp")
	process, err := newRoleProcess(cfg, append(os.Environ(), roleGroupShimEnv+"="+plan.Case), os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if err := process.start(); err != nil {
		return err
	}
	ended := make(chan struct{})
	var raw []byte
	go func() {
		raw, _ = io.ReadAll(process.status)
		close(ended)
	}()
	if plan.Stop {
		if !awaitPath(filepath.Join(tmp, "ready"), ended) {
			return errors.New("the stand-in shim never said it was ready to be stopped")
		}
		// The launcher's stop of a generation in an issue pod: SIGTERM to role-init alone, then
		// SIGKILL at the end of the stop grace (endGeneration, signalChild).
		if err := process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("stop role-init: %w", err)
		}
		killed := "no"
		select {
		case <-ended:
		case <-time.After(plan.Grace):
			killed = "yes"
			_ = process.cmd.Process.Kill()
			<-ended
		}
		fmt.Printf("group killed=%s\n", killed)
	}
	<-ended
	_ = process.status.Close()
	if err := process.cmd.Wait(); err != nil {
		fmt.Printf("group role-init=%v\n", err)
	}
	if status := parseStatus(raw); status == nil {
		fmt.Println("group status=none")
	} else {
		fmt.Printf("group status=%d\n", status.ExitStatus())
	}
	fmt.Printf("group saved=%s\n", presence(filepath.Join(tmp, "saved")))
	fmt.Printf("group descendant=%s\n", presence(filepath.Join(tmp, "descendant")))
	return nil
}

// awaitPath waits for path to exist, giving up at roleGroupBound or once ended is closed.
func awaitPath(path string, ended <-chan struct{}) bool {
	deadline := time.After(roleGroupBound)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		select {
		case <-ended:
			return false
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestRoleGroupShimHelper is the stand-in shim role-init starts, and its own children, by
// roleGroupShimEnv.
func TestRoleGroupShimHelper(t *testing.T) {
	var code int
	switch mode := os.Getenv(roleGroupShimEnv); mode {
	case "":
		return
	case "orphan":
		code = groupShimOrphan()
	case "hup":
		code = groupShimHup()
	case "stop":
		code = groupShimStop()
	case "session":
		code = groupControlSession()
	case "leader":
		code = groupControlLeader()
	default:
		fmt.Printf("group error=no stand-in mode %q\n", mode)
		code = 2
	}
	os.Exit(code)
}

// groupShimOrphan reports the stand-in's process group and role-init's, leaves an orphan in its
// group, and reports the orphan's parent and group while it runs and whether role-init reaped it.
func groupShimOrphan() int {
	fmt.Printf("group shim-group=%d\n", syscall.Getpgrp())
	initGroup, err := syscall.Getpgid(1)
	if err != nil {
		fmt.Printf("group error=read role-init's group: %v\n", err)
		return 1
	}
	fmt.Printf("group init-group=%d\n", initGroup)
	orphan, err := leaveOrphan("1")
	if err != nil {
		fmt.Printf("group error=%v\n", err)
		return 1
	}
	parent, group, _, err := procStat(orphan)
	if err != nil {
		fmt.Printf("group error=read the orphan: %v\n", err)
		return 1
	}
	fmt.Printf("group orphan-parent=%d\n", parent)
	fmt.Printf("group orphan-group=%d\n", group)
	fmt.Printf("group orphan=%s\n", awaitReaped(orphan))
	return 0
}

// groupShimHup runs the orphaned-by-exit control, then stops a member of its own group, leaves an
// orphan in the group for role-init to reap, and reports whether the member is still stopped
// afterwards and whether the stand-in itself received SIGHUP.
func groupShimHup() int {
	hups := make(chan os.Signal, 4)
	signal.Notify(hups, syscall.SIGHUP)
	control, err := orphanedByExit()
	if err != nil {
		fmt.Printf("group error=the control: %v\n", err)
		return 1
	}
	fmt.Printf("group control-hup=%s\n", control)

	member := exec.Command("/bin/sh", "-c", "kill -STOP $$; exec /bin/sleep 600")
	if err := member.Start(); err != nil {
		fmt.Printf("group error=start the member: %v\n", err)
		return 1
	}
	pid := member.Process.Pid
	defer func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
	}()
	var status syscall.WaitStatus
	if got, err := syscall.Wait4(pid, &status, syscall.WUNTRACED, nil); err != nil || got != pid || !status.Stopped() {
		fmt.Printf("group error=the member did not stop: %v %v\n", err, status)
		return 1
	}
	orphan, err := leaveOrphan("0.2")
	if err != nil {
		fmt.Printf("group error=%v\n", err)
		return 1
	}
	fmt.Printf("group orphan=%s\n", awaitReaped(orphan))
	// Long enough for a SIGHUP and SIGCONT the reap brought to have arrived.
	time.Sleep(500 * time.Millisecond)
	switch got, err := syscall.Wait4(pid, &status, syscall.WNOHANG|syscall.WUNTRACED|syscall.WCONTINUED, nil); {
	case err != nil:
		fmt.Printf("group member=%v\n", err)
	case got == 0:
		fmt.Println("group member=stopped")
	case status.Signaled():
		fmt.Printf("group member=ended by %v\n", status.Signal())
	case status.Continued():
		fmt.Println("group member=continued")
	default:
		fmt.Printf("group member=%v\n", status)
	}
	select {
	case <-hups:
		fmt.Println("group shim-hup=yes")
	default:
		fmt.Println("group shim-hup=no")
	}
	return 0
}

// groupShimStop is a shim that a stop reaches: it leaves a descendant that removes a file of its
// own when it is signalled, says it is ready once that descendant's trap is in place, and on its
// first SIGTERM saves for 300 ms (writes /tmp/saved), counts every SIGTERM it got by then and a
// little after, and exits 0.
func groupShimStop() int {
	terms := make(chan os.Signal, 8)
	signal.Notify(terms, syscall.SIGTERM)
	descendant := exec.Command("/bin/sh", "-c", `/bin/sleep 600 &
sleeper=$!
trap 'kill $sleeper 2>/dev/null; rm -f /tmp/descendant; exit 0' TERM
printf alive > /tmp/descendant
wait $sleeper`)
	if err := descendant.Start(); err != nil {
		fmt.Printf("group error=start the descendant: %v\n", err)
		return 1
	}
	never := make(chan struct{})
	if !awaitPath("/tmp/descendant", never) {
		fmt.Println("group error=the descendant never set its trap")
		return 1
	}
	if err := os.WriteFile("/tmp/ready", nil, 0o600); err != nil {
		fmt.Printf("group error=%v\n", err)
		return 1
	}
	select {
	case <-terms:
	case <-time.After(roleGroupBound):
		fmt.Println("group error=no SIGTERM came")
		return 1
	}
	count := 1
	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile("/tmp/saved", []byte("saved"), 0o600); err != nil {
		fmt.Printf("group error=%v\n", err)
		return 1
	}
	for late := time.After(200 * time.Millisecond); ; {
		select {
		case <-terms:
			count++
			continue
		case <-late:
		}
		break
	}
	fmt.Printf("group sigterms=%d\n", count)
	return 0
}

// orphanedByExit is the positive control: a process in a session of its own starts a group leader,
// whose member stops itself; the leader then exits, so the member's parent becomes role-init, in
// another session, and the leader's exit orphans the group with a stopped member, which every
// kernel answers with SIGHUP and SIGCONT to the group. It answers "yes" when the member then ends
// (its SIGHUP's default action) and "no" when it is still stopped at the bound.
func orphanedByExit() (string, error) {
	file := "/tmp/control-member"
	session := exec.Command(stageSelf, "-test.run=^TestRoleGroupShimHelper$")
	session.Env = append(os.Environ(), roleGroupShimEnv+"=session", roleGroupControlEnv+"="+file)
	session.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if out, err := session.CombinedOutput(); err != nil {
		return "", fmt.Errorf("the session: %v: %s", err, out)
	}
	spelled, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	member, err := strconv.Atoi(strings.TrimSpace(string(spelled)))
	if err != nil {
		return "", fmt.Errorf("the leader wrote %q, not its member's pid", spelled)
	}
	deadline := time.Now().Add(roleGroupBound)
	for {
		_, _, state, err := procStat(member)
		switch {
		case errors.Is(err, os.ErrNotExist) || state == "Z":
			return "yes", nil
		case err != nil:
			return "", err
		case time.Now().After(deadline):
			_ = syscall.Kill(member, syscall.SIGKILL)
			return "no, still " + state, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// groupControlSession is the control's session process: it starts the group leader and waits
// for it.
func groupControlSession() int {
	leader := exec.Command(stageSelf, "-test.run=^TestRoleGroupShimHelper$")
	leader.Env = append(os.Environ(), roleGroupShimEnv+"=leader")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	leader.Stdout, leader.Stderr = os.Stdout, os.Stderr
	if err := leader.Run(); err != nil {
		fmt.Println("session:", err)
		return 1
	}
	return 0
}

// groupControlLeader is the control's group leader: it starts a member that stops itself, waits
// until it has, writes its pid to the control's file and exits, which orphans the group.
func groupControlLeader() int {
	member := exec.Command("/bin/sh", "-c", "kill -STOP $$; exec /bin/sleep 600")
	if err := member.Start(); err != nil {
		fmt.Println("leader:", err)
		return 1
	}
	var status syscall.WaitStatus
	if got, err := syscall.Wait4(member.Process.Pid, &status, syscall.WUNTRACED, nil); err != nil || got != member.Process.Pid || !status.Stopped() {
		fmt.Println("leader: the member did not stop:", err, status)
		return 1
	}
	if err := os.WriteFile(os.Getenv(roleGroupControlEnv), []byte(strconv.Itoa(member.Process.Pid)), 0o600); err != nil {
		fmt.Println("leader:", err)
		return 1
	}
	return 0
}

// leaveOrphan starts a shell that starts `sleep <seconds>` in the background and exits, so the
// sleep is orphaned while it runs, and answers the sleep's pid once the shell is gone.
func leaveOrphan(seconds string) (int, error) {
	var out bytes.Buffer
	shell := exec.Command("/bin/sh", "-c", "/bin/sleep "+seconds+" </dev/null >/dev/null 2>&1 & echo $!")
	shell.Stdout = &out
	if err := shell.Run(); err != nil {
		return 0, fmt.Errorf("the shell that leaves the orphan: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		return 0, fmt.Errorf("the shell printed %q, not the orphan's pid", out.String())
	}
	return pid, nil
}

// awaitReaped waits for pid to leave /proc: "reaped" once it has, "zombie" when it is still there,
// exited and unreaped, at roleGroupBound, and "running" when it has not ended by then.
func awaitReaped(pid int) string {
	deadline := time.Now().Add(roleGroupBound)
	for {
		_, _, state, err := procStat(pid)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "reaped"
		case err != nil:
			return err.Error()
		case time.Now().After(deadline) && state == "Z":
			return "zombie"
		case time.Now().After(deadline):
			return "running"
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// procStat reads pid's parent, process group and state from the /proc the caller sees.
func procStat(pid int) (parent, group int, state string, err error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, "", err
	}
	end := bytes.LastIndexByte(stat, ')')
	fields := strings.Fields(string(stat[end+1:]))
	if end < 0 || len(fields) < 3 {
		return 0, 0, "", fmt.Errorf("an unreadable stat for %d: %q", pid, stat)
	}
	parent, _ = strconv.Atoi(fields[1])
	group, _ = strconv.Atoi(fields[2])
	return parent, group, fields[0], nil
}
