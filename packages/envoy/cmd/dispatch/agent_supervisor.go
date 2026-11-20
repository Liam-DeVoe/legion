package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sjawhar/envoy/going/agentwire"
)

// The supervisor's bounds (section 5 of the background-runs design).
const (
	// agentMemoryEvery is how often the supervisor reads the agent's resident memory.
	agentMemoryEvery = 50 * time.Millisecond
	// agentRenewalsMissed is how long the agent may go without a renew, three of the renews the
	// server answers every 10 s, before the supervisor kills it as stuck as a whole: a SIGSTOP,
	// a deadlock, a runtime that no longer schedules.
	agentRenewalsMissed = 30 * time.Second
	// agentRestartFirst is the wait before the agent starts again after an exit, and
	// agentRestartMost the longest: each quick exit doubles the wait before it.
	agentRestartFirst = time.Second
	agentRestartMost  = 30 * time.Second
	// agentQuickExit is the run below which an exit is quick: an agent that exits within a minute
	// of its start.
	agentQuickExit = time.Minute
	// agentKillWindow is the hour within which two kills for memory no run alone caused halve the
	// agent's slots, and the hour they stay halved for.
	agentKillWindow = time.Hour
	// agentMaxProcs is the most CPUs the agent's runtime schedules on (GOMAXPROCS).
	agentMaxProcs = 4
	// agentNice is the agent's lower priority, beside the server's 0.
	agentNice = 10
	// agentExitGrace is how long the server's close waits for the agent, whose standard input it
	// closed, before it kills it.
	agentExitGrace = 2 * time.Second
	// agentExitedTimeout bounds the database work of an exit: freeing the runs the agent held.
	agentExitedTimeout = 10 * time.Second
)

// errAgentStopping is a start refused because the server is stopping its agent.
var errAgentStopping = errors.New("the server is stopping its agent")

// agentEnd is the server's end of agent/v1 that the supervisor drives (api.AgentListener).
type agentEnd interface {
	Path() string
	Secret() []byte
	SetCloseStandardInput(closeIn func())
	LastRenewal() time.Time
	Health() (agentwire.Health, bool)
	SetRunSlots(slots int)
	AgentExited(ctx context.Context, stopping bool) (bool, error)
	Stop()
	Close(by time.Time)
}

// agentSupervisor runs the command bar's agent process (envoy-dispatch agent) for the server: it
// starts the agent at boot with an environment of its own, writes the secret of agent/v1 to its
// standard input and keeps that open, since the agent exits when it closes; it starts the agent
// again after each exit, 1 s after, then twice as long after each quick exit, up to 30 s. It reads
// the agent's resident memory every 50 ms and kills it with SIGKILL above its ceiling, and kills
// an agent that renewed nothing for 30 s. Each exit frees the runs the agent held
// (agentEnd.AgentExited); two kills for memory within an hour that no run alone caused halve the
// agent's run slots, to 1 at the least, for an hour. It is the process's agentShutdown: Stop
// closes the agent's standard input after the HTTP drain, and Close waits for the agent to exit.
type agentSupervisor struct {
	end     agentEnd
	command []string
	env     []string
	limits  agentMemory
	// after is how the supervisor waits before a restart, time.After; a test records the waits.
	after func(time.Duration) <-chan time.Time
	// renewalsMissed is agentRenewalsMissed; a test shortens it.
	renewalsMissed time.Duration

	// memoryKills counts the agent's kills for memory since the server started.
	memoryKills atomic.Int64

	mu sync.Mutex
	// stopping is closed when the server stops its agent (closeStandardInput).
	stopping chan struct{}
	// stdin, pid and started are the running agent's standard input, process id and start; a nil
	// stdin and a pid of 0 while none runs.
	stdin   io.Closer
	pid     int
	started time.Time
	// slots is the agent's run slots now, limits.Slots but for the hour after a halving, which
	// ends at slotsUntil and restore gives back.
	slots      int
	slotsUntil time.Time
	restore    *time.Timer
	// unexplained is the latest kill for memory that no run alone caused.
	unexplained time.Time

	// done is closed once the supervisor starts no agent any more and the last one has exited.
	done chan struct{}
}

// startAgentSupervisor starts the agent process, command run with env, for end, and supervises it
// until the server stops it.
func startAgentSupervisor(end agentEnd, command, env []string, limits agentMemory) *agentSupervisor {
	s := newAgentSupervisor(end, command, env, limits)
	go s.run()
	return s
}

// newAgentSupervisor is a supervisor that has started nothing yet.
func newAgentSupervisor(end agentEnd, command, env []string, limits agentMemory) *agentSupervisor {
	s := &agentSupervisor{end: end, command: command, env: env, limits: limits, after: time.After,
		renewalsMissed: agentRenewalsMissed, stopping: make(chan struct{}), slots: limits.Slots, done: make(chan struct{})}
	end.SetRunSlots(limits.Slots)
	end.SetCloseStandardInput(s.closeStandardInput)
	return s
}

// agentEnvironment is the agent's whole environment: the socket it calls the server at, its CPUs
// and its memory limit, and the chat model's settings as the server read them - nothing of the
// server's own environment, so the agent holds no secret of Dispatch.
func agentEnvironment(chat answererConfig, socket string, limits agentMemory) []string {
	env := []string{
		"DISPATCH_AGENT_SOCKET=" + socket,
		"GOMAXPROCS=" + strconv.Itoa(min(agentMaxProcs, runtime.GOMAXPROCS(0))),
		"GOMEMLIMIT=" + strconv.FormatInt(limits.AgentLimit, 10),
	}
	set := func(name, value string) {
		if value != "" {
			env = append(env, name+"="+value)
		}
	}
	set("DISPATCH_ANSWERER", chat.Place)
	switch chat.Place {
	case placeAnswererAzure:
		set("DISPATCH_AZURE_CHAT_ENDPOINT", chat.Endpoint)
		set("DISPATCH_AZURE_CHAT_DEPLOYMENT", chat.Model)
		set("DISPATCH_AZURE_CHAT_API_VERSION", chat.APIVersion)
		set("DISPATCH_AZURE_CHAT_SCOPE", chat.Scope)
		set("DISPATCH_AZURE_CHAT_TOKENS_PER_MINUTE", strconv.Itoa(chat.TokensPerMinute))
		set("DISPATCH_AZURE_TENANT_ID", chat.TenantID)
		set("DISPATCH_AZURE_CLIENT_ID", chat.ClientID)
		set("DISPATCH_AZURE_FEDERATED_TOKEN_FILE", chat.FederatedToken)
	case placeAnswererEndpoint:
		set("DISPATCH_CHAT_ENDPOINT", chat.Endpoint)
		set("DISPATCH_CHAT_MODEL", chat.Model)
		set("DISPATCH_CHAT_TOKEN_FILE", chat.TokenFile)
	}
	return env
}

// nextAgentRestart is the wait before the agent starts again, after the wait before its last start
// (0 for none) and a run of ran: agentRestartFirst after the first exit and after a run of
// agentQuickExit or more, and otherwise twice the last wait, at most agentRestartMost.
func nextAgentRestart(previous, ran time.Duration) time.Duration {
	if previous == 0 || ran >= agentQuickExit {
		return agentRestartFirst
	}
	return min(2*previous, agentRestartMost)
}

// run starts the agent, watches it until it exits, and starts it again, until the server stops it.
func (s *agentSupervisor) run() {
	defer close(s.done)
	var wait time.Duration
	for {
		started := time.Now()
		cmd, err := s.start()
		if errors.Is(err, errAgentStopping) {
			return
		}
		var ran time.Duration
		if err != nil {
			slog.Error("dispatch: start the agent process", "error", err)
		} else {
			killedForMemory := s.watch(cmd, started)
			ran = time.Since(started)
			s.exited(killedForMemory)
		}
		select {
		case <-s.stopping:
			return
		default:
		}
		wait = nextAgentRestart(wait, ran)
		slog.Warn("dispatch: the agent process exited; it starts again", "after", wait, "ran", ran.Round(time.Millisecond), "exit", exitOf(cmd))
		select {
		case <-s.after(wait):
		case <-s.stopping:
			return
		}
	}
}

// exitOf names how cmd's process ended, for the log.
func exitOf(cmd *exec.Cmd) string {
	if cmd == nil || cmd.ProcessState == nil {
		return "not started"
	}
	return cmd.ProcessState.String()
}

// start starts one agent: its command with the agent's environment, its output into the server's
// log, the secret on its standard input, which stays open, and its priority lowered.
func (s *agentSupervisor) start() (*exec.Cmd, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopping:
		return nil, errAgentStopping
	default:
	}
	cmd := exec.Command(s.command[0], s.command[1:]...)
	cmd.Env = s.env
	// A writer of its own rather than the server's *os.File, so the agent holds a pipe of the
	// server's and not the server's own standard error, which outlives no reader of the server.
	output := struct{ io.Writer }{os.Stderr}
	cmd.Stdout, cmd.Stderr = output, output
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if _, err := stdin.Write(s.end.Secret()); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("give the agent its secret: %w", err)
	}
	s.stdin, s.pid, s.started = stdin, cmd.Process.Pid, time.Now()
	lowerPriority(cmd.Process.Pid)
	slog.Info("dispatch: started the agent process", "pid", cmd.Process.Pid, "ceiling", s.limits.Ceiling,
		"gomemlimit", s.limits.AgentLimit, "slots", s.slots)
	return cmd, nil
}

// watch waits for the agent cmd started at started to exit, reading its resident memory every
// agentMemoryEvery: above its ceiling it is killed with SIGKILL, and so is an agent whose latest
// renew, or whose start where it has renewed nothing, is renewalsMissed old. It reports whether the
// agent was killed for memory.
func (s *agentSupervisor) watch(cmd *exec.Cmd, started time.Time) bool {
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	pid := cmd.Process.Pid
	ticker := time.NewTicker(agentMemoryEvery)
	defer ticker.Stop()
	killed, killedForMemory, lowered := false, false, false
	for {
		select {
		case <-exited:
			return killedForMemory
		case <-ticker.C:
		}
		if killed {
			continue
		}
		// The threads its runtime made since the start are lowered too.
		if !lowered && time.Since(started) >= time.Second {
			lowerPriority(pid)
			lowered = true
		}
		if memory, err := residentMemory(pid); err == nil && memory > s.limits.Ceiling {
			_ = cmd.Process.Kill()
			killed, killedForMemory = true, true
			slog.Warn("dispatch: agent killed for memory", "pid", pid, "memory", memory, "ceiling", s.limits.Ceiling)
			s.memoryKills.Add(1)
			continue
		}
		renewed := s.end.LastRenewal()
		if renewed.Before(started) {
			renewed = started
		}
		if since := time.Since(renewed); since > s.renewalsMissed {
			_ = cmd.Process.Kill()
			killed = true
			slog.Warn("dispatch: agent killed: it renewed nothing for 30s", "pid", pid, "since_renewal", since.Round(time.Millisecond))
		}
	}
}

// exited reports the agent's exit to the server's end, which frees the runs it held, and counts
// a kill for memory that no run alone caused (noteUnexplainedKill).
func (s *agentSupervisor) exited(killedForMemory bool) {
	s.mu.Lock()
	s.stdin, s.pid = nil, 0
	var stopping bool
	select {
	case <-s.stopping:
		stopping = true
	default:
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), agentExitedTimeout)
	defer cancel()
	alone, err := s.end.AgentExited(ctx, stopping)
	if err != nil {
		slog.Error("dispatch: free the runs of the agent process that exited; their leases run out instead", "error", err)
		return
	}
	if killedForMemory && !alone {
		s.noteUnexplainedKill(time.Now())
	}
}

// noteUnexplainedKill counts a kill for memory at now that no run alone caused: one within
// agentKillWindow of the one before halves the agent's run slots, to 1 at the least, until
// agentKillWindow after it.
func (s *agentSupervisor) noteUnexplainedKill(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.unexplained
	s.unexplained = now
	if previous.IsZero() || now.Sub(previous) > agentKillWindow {
		return
	}
	s.slots = max(1, s.slots/2)
	s.slotsUntil = now.Add(agentKillWindow)
	s.end.SetRunSlots(s.slots)
	if s.restore != nil {
		s.restore.Stop()
	}
	s.restore = time.AfterFunc(agentKillWindow, s.restoreSlots)
	slog.Warn("dispatch: the agent was killed for memory twice within an hour with no run alone to blame; its slots are halved for an hour",
		"slots", s.slots)
}

// restoreSlots gives the agent its whole slots back once agentKillWindow has passed since they
// were last halved.
func (s *agentSupervisor) restoreSlots() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.slotsUntil) {
		return
	}
	s.slots = s.limits.Slots
	s.end.SetRunSlots(s.slots)
	slog.Info("dispatch: the agent's slots are whole again", "slots", s.slots)
}

// closeStandardInput is the stop of the agent, which the server's end calls at its Stop: no agent
// starts from now on, and the running one's standard input closes, so it exits.
func (s *agentSupervisor) closeStandardInput() {
	s.mu.Lock()
	select {
	case <-s.stopping:
		s.mu.Unlock()
		return
	default:
	}
	close(s.stopping)
	stdin, pid := s.stdin, s.pid
	s.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
		slog.Info("dispatch: closed the agent's standard input", "pid", pid)
	}
}

// Stop is the first half of the agent's shutdown, after the HTTP drain: the server's end stops,
// which closes the agent's standard input (closeStandardInput).
func (s *agentSupervisor) Stop() {
	s.end.Stop()
}

// Close is the second half: the server's end closes once the calls in flight have ended or by
// passes, and the agent, whose standard input Stop closed, is waited for agentExitGrace, then
// killed.
func (s *agentSupervisor) Close(by time.Time) {
	s.end.Close(by)
	select {
	case <-s.done:
	case <-time.After(agentExitGrace):
		s.mu.Lock()
		pid := s.pid
		s.mu.Unlock()
		if pid != 0 {
			slog.Warn("dispatch: the agent did not exit when its standard input closed; it is killed", "pid", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		select {
		case <-s.done:
		case <-time.After(agentExitGrace):
			slog.Warn("dispatch: the agent's exit was not recorded before the shutdown went on")
		}
	}
	s.mu.Lock()
	if s.restore != nil {
		s.restore.Stop()
	}
	s.mu.Unlock()
}

// running is when the running agent started, and false while none runs.
func (s *agentSupervisor) running() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started, s.pid != 0
}

// runSlots is the agent's run slots now.
func (s *agentSupervisor) runSlots() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slots
}

// lowerPriorityRefused is set once the kernel refused to lower the agent's priority, which is
// logged once for the process rather than at each start.
var lowerPriorityRefused atomic.Bool

// lowerPriority sets the nice value of each thread of process pid to agentNice: Linux keeps a
// nice value for each thread, and a thread takes its maker's, so the agent's runtime makes its
// later threads at the lower priority.
func lowerPriority(pid int) {
	tasks, _ := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	ids := []int{pid}
	for _, task := range tasks {
		if id, err := strconv.Atoi(task.Name()); err == nil && id != pid {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, id, agentNice); err != nil && !errors.Is(err, syscall.ESRCH) {
			if !lowerPriorityRefused.Swap(true) {
				slog.Warn("dispatch: lower the agent's priority", "pid", pid, "thread", id, "error", err)
			}
			return
		}
	}
}

// residentMemory is process pid's resident memory in bytes, its VmRSS in /proc/<pid>/status.
func residentMemory(pid int) (int64, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	return vmRSS(data)
}

// vmRSS reads the VmRSS line of a /proc/<pid>/status, which counts kibibytes.
func vmRSS(status []byte) (int64, error) {
	scanner := bufio.NewScanner(bytes.NewReader(status))
	for scanner.Scan() {
		value, found := bytes.CutPrefix(scanner.Bytes(), []byte("VmRSS:"))
		if !found {
			continue
		}
		fields := bytes.Fields(value)
		if len(fields) != 2 || string(fields[1]) != "kB" {
			return 0, fmt.Errorf("VmRSS reads %q, not a count of kB", value)
		}
		kibibytes, err := strconv.ParseInt(string(fields[0]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("VmRSS reads %q, not a count of kB", value)
		}
		return kibibytes << 10, nil
	}
	return 0, errors.New("the status holds no VmRSS line")
}
