// Package fleetalert writes the daemon's error lines for the cases no agent can hear (R6): the
// project's controller is the one out of reach, Envoy lists nobody, the daemon cannot read Envoy's
// session list at all, or a claim's newer process is still locked out by an older one that will not
// stop (R3).
//
// Every notice of R5 goes to a session over Envoy, so none of these cases can reach a person that
// way. The line goes outside Envoy instead: the deployment repository's `legion/` program holds a
// log-match alert policy over the daemons' own containers which matches Prefix in the entry's
// `jsonPayload.msg` and opens an incident in Slack's #fleet-alerts and under the infrastructure
// lead's Envoy role, as `legion/sessions_readonly_alert.go` does for a statement refused in a
// read-only transaction. Prefix is therefore a contract between the two repositories: this
// package's test holds it, and the policy's filter names it.
//
// A line is written once per case and claim until that case ends, so a case that holds for hours
// opens one incident rather than one a minute. The caller says when a case ends (Close), which
// is the moment the next Open writes again.
package fleetalert

import (
	"log/slog"
	"sync"
)

// Prefix is the fixed text every line starts with, and the whole of what the alert policy
// matches. It changes in neither repository alone.
const Prefix = "LEGION FLEET ALERT"

// Case is one of the cases a person is told of: Word is what the line's `case` field holds and
// the policy's documentation names, and Sentence is what the line says after Prefix.
type Case struct {
	Word     string
	Sentence string
}

var (
	// ControllerUnreachable is the controller still out of reach after the daemon's one relaunch
	// of it, a relaunched controller that has not reached ready with its Envoy role 20 minutes
	// after that relaunch included.
	ControllerUnreachable = Case{
		Word:     "controller_unreachable",
		Sentence: "the project's controller is out of reach of Envoy and the daemon's relaunch of it did not bring it back; nobody else holds that role, so a person must start it",
	}
	// ControllerRelaunchRefused is the controller out of reach with the daemon's relaunch of it
	// refused by the guard, or ended by a process that outlived its stop.
	ControllerRelaunchRefused = Case{
		Word:     "controller_relaunch_refused",
		Sentence: "the project's controller is out of reach of Envoy and the daemon cannot relaunch it; a person must look at its process",
	}
	// NoLiveSession is Envoy's whole session list, Legion's sessions and every other, holding no
	// live session for 20 minutes.
	NoLiveSession = Case{
		Word:     "envoy_no_live_session",
		Sentence: "Envoy has listed no live session for 20 minutes, so no agent of the fleet can hear anything",
	}
	// SessionListUnreadable is the daemon's reads of Envoy's session list failing for 20 minutes.
	SessionListUnreadable = Case{
		Word:     "envoy_session_list_unreadable",
		Sentence: "the daemon has not read Envoy's session list for 20 minutes, so it cannot tell which roles are out of reach",
	}
	// NewerProcessRefused is a claim's newer process still refused by the worker stream 5 minutes
	// after its first hello, because an older connection will not close (R3).
	NewerProcessRefused = Case{
		Word:     "newer_process_refused",
		Sentence: "a claim's current process is still locked out of its worker stream by an older process that will not stop; a person must end that process on its node",
	}
)

// Cases is every case this package names, for a reader that lists them (the policy's
// documentation, and this package's own test).
func Cases() []Case {
	return []Case{ControllerUnreachable, ControllerRelaunchRefused, NoLiveSession, SessionListUnreadable, NewerProcessRefused}
}

// Writer writes one line per open case of one project.
type Writer struct {
	log     *slog.Logger
	project string

	mu   sync.Mutex
	open map[openCase]struct{}
}

// openCase keys a case that has been written and not ended: one case may be open for several
// claims at once, each with a line of its own.
type openCase struct {
	word  string
	claim string
}

// New is a Writer that logs through log for project.
func New(log *slog.Logger, project string) *Writer {
	return &Writer{log: log, project: project, open: map[openCase]struct{}{}}
}

// Open writes c's line, naming claim where there is one and whatever else the caller gives as
// slog key-value pairs, and returns whether it wrote: a case already open writes nothing until
// Close ends it.
func (w *Writer) Open(c Case, claim string, attrs ...any) bool {
	w.mu.Lock()
	key := openCase{word: c.Word, claim: claim}
	if _, already := w.open[key]; already {
		w.mu.Unlock()
		return false
	}
	w.open[key] = struct{}{}
	w.mu.Unlock()

	args := []any{"case", c.Word, "project", w.project}
	if claim != "" {
		args = append(args, "claim", claim)
	}
	w.log.Error(Prefix+": "+c.Sentence, append(args, attrs...)...)
	return true
}

// Close ends c for claim, so the next Open of it writes again. Closing a case that is not open
// changes nothing.
func (w *Writer) Close(c Case, claim string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.open, openCase{word: c.Word, claim: claim})
}

// IsOpen is whether c's line for claim has been written and not ended, for a caller that reads
// its own state rather than keeping a second copy of it.
func (w *Writer) IsOpen(c Case, claim string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, open := w.open[openCase{word: c.Word, claim: claim}]
	return open
}
