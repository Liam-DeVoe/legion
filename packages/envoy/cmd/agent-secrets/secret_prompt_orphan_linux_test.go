//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// An orphaned prompt with a value deadline ends by that deadline whatever its check of whether its
// group is orphaned does. Once no ancestor of the prompt holds its group, that check reads the stat
// of every process of the machine (processGroupOrphaned), and a read there can block, as one of a
// FIFO bound over a stat does; the check runs beside the wait (promptTerminal.groupOrphaned), so
// the deadline still ends the prompt, naming the check that had not ended. Where the check
// answers, the prompt refuses as an orphan at once, before the deadline. A prompt held inside the
// check ends neither way, and this test's wait (promptJobWait) fails it.
func TestPromptJobOrphanedPromptEndsByItsValueDeadlineWhateverItsCheckDoes(t *testing.T) {
	refused := "RETURNED " + errNoForeground.Error()
	unfinished := "RETURNED read the value at the terminal: " + errOrphanCheckUnfinished.Error()
	s := newPromptShell(t)
	// Without job control a background command's standard input is /dev/null, so the subshell
	// names the terminal for it; the subshell's exit orphans the prompt's group.
	command := "AGENT_SECRETS_JOB_REFERENCE=" + s.foregroundState() + " AGENT_SECRETS_JOB_DEADLINE=5s AGENT_SECRETS_JOB_HELPER=1 " +
		shellWord(os.Args[0]) + " -test.run='^TestPromptJobHelper$' </dev/tty"
	s.reset()
	s.send("(" + command + " &)\r")
	s.wait("HELPER_PID=")
	s.wait("\r\nHELPER_READY\r\n") // the whole pid line has come
	start := strings.Index(s.out.String(), "HELPER_PID=") + len("HELPER_PID=")
	pid, err := strconv.Atoi(strings.Fields(s.out.String()[start:])[0])
	if err != nil {
		t.Fatal(err)
	}
	s.pid = pid
	switch s.waitFirst(refused, unfinished) {
	case refused:
		t.Log("the check answered: the orphaned prompt refused before its deadline")
	case unfinished:
		t.Log("the check had not answered: the orphaned prompt ended at its value deadline, naming the check")
	}
}
