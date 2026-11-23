package memoryguard

import (
	"path/filepath"
	"time"

	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/termsafe"
)

// The guard's log lines. Each starts with linePrefix and carries `key=value` fields: sizes in
// bytes, times in RFC 3339 UTC, and a process's name (processName) last and quoted, so a reader
// takes everything from `executable="` to the line's last quote whatever the name holds. No line
// prints a process's arguments. A process the guard cannot read has its unreadLine once, settings
// the launcher refuses their refusedLine, and a process the kill failed on its killFailedLine
// once, so a kill that fails is never retried unseen at every reading. A stop that repeats has its
// stoppedLine at most once in each stoppedEvery, and the line written counts in left_out the
// stopped lines left out since the one before it (stopRise).
const (
	linePrefix     = "launcher: memory guard: "
	startedLine    = linePrefix + "started act_percent=%d floor_percent=%d wait=%s interval=%s limit=%d reading=%d"
	settingsLine   = linePrefix + "settings act_percent=%d floor_percent=%d wait=%s interval=%s"
	noSettingsLine = linePrefix + "off: the daemon's acknowledgement carries no settings"
	refusedLine    = linePrefix + "settings refused: %v; nothing changes"
	endedLine      = linePrefix + `ended pid=%d role=%s size=%d reading=%d counter=%d shmem=%d limit=%d executable="%s"`
	stoppedLine    = linePrefix + "stopped for this rise reason=%s reading=%d endable=%d protected=%d unread=%d unheld=%d limit=%d left_out=%d"
	hourLine       = linePrefix + "hour start=%s end=%s reading_high=%d reading_high_at=%s reading_low=%d counter_high=%d counter_low=%d" +
		` largest_pid=%d largest_role=%s largest_size=%d largest_executable="%s"`
	offLine        = linePrefix + "off reason=%s"
	unreadLine     = linePrefix + "unread pid=%d reason=%v"
	killFailedLine = linePrefix + "kill failed pid=%d reason=%v"
)

// The two reasons a rise is given up on.
const (
	reasonNotFallen    = "not fallen"
	reasonNothingToEnd = "nothing to end"
)

// The guest watch's lines (Guest). Each starts with guestPrefix and carries `key=value` fields as
// the guard's do, each figure the watch cannot read written `-`; a process the guest kernel killed
// has the guard's own endedLine, so a reader finds a process stopped for memory by one line,
// whichever stopped it. The start line names the cgroup's version, its directory, v2's
// memory.oom.group and the starting score; a watch with no readable kill counter has its
// guestKillsUnnamedLine once, and one with no readable /proc its guestOffLine once. A watch that
// reads the kill counter and does not wake on the file-modified event of memory.events (Guest's
// wake) has its guestWakeOffLine once, with the reason: its cgroup is v1, whose memory.oom_control
// raises no such event, or the watch of the event could not start, or it ended, with the error
// that ended it; the watch then reads the counter at its scans alone. The kills of a burst of the
// counter's rises that no process was named for by the wait's end, since none showed the kernel's
// kill, or more showed it at one pass than kills were pending, or each that showed it was of a
// role whose session had ended (Guest's sessionEnded), and every kill of a rise where the watch
// reads no /proc, have one guestKilledUnnamedLine, which names no process: the count of those
// kills, the counter's total, and the pod's memory at the burst's first rise, the fields an
// endedLine gives it (reading, counter, shmem and limit). A process whose score write failed has
// its guestScoreFailedLine once, with the score the write would have set.
const (
	guestPrefix            = "launcher: guest memory: "
	guestStartLine         = guestPrefix + "start cgroup=%s dir=%s oom_group=%s score=%s"
	guestOffLine           = guestPrefix + "off reason=%s"
	guestKillsUnnamedLine  = guestPrefix + "kills unnamed reason=%v"
	guestWakeOffLine       = guestPrefix + "wake off reason=%s"
	guestKilledUnnamedLine = guestPrefix + "killed unnamed count=%d oom_kill=%d reading=%d counter=%d shmem=%d limit=%d"
	guestScoreFailedLine   = guestPrefix + "score failed pid=%d start_time=%d score=%d reason=%v"
	guestHourLine          = guestPrefix + "hour start=%s end=%s current=%s current_high=%s current_high_at=%s" +
		" working_set=%s working_set_high=%s working_set_high_at=%s anon=%s file=%s inactive_file=%s shmem=%s limit=%s" +
		" oom_kills=%s unnamed=%s raised=%s unraised=%s"
)

// roleName is a class's role as a line names it: `none` outside every role.
func roleName(role string) string {
	if role == "" {
		return "none"
	}
	return role
}

// stamp is t as a line writes it.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// processName is how every line and every report names p: the base name of its executable, the
// `exe` link the scan read, or its stat name (comm) where the scan could not read that link,
// escaped and cut to shimwire.MemoryKillExecutableBytes (termsafe.Escaped). It holds none of p's arguments,
// and the guard masks nothing: an argument is text the process chose, and it may hold a credential
// no masking rule names, such as a positional password, while the role that started the process
// already has its arguments (dispatch://INFRA-3557, W4).
func processName(p *Process) string {
	name := p.Name
	if p.Exe != "" {
		name = filepath.Base(p.Exe)
	}
	return termsafe.Escaped(name, shimwire.MemoryKillExecutableBytes)
}
