package writerhold

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// The steps of a hold, as a finding names them.
const (
	StepPreflight = "preflight"
	StepCensus    = "writer census"
	StepProbe     = "probe"
	StepRestart   = "restart"
	StepProof     = "proof"
	StepBarrier   = "barrier"
	StepReport    = "report"
	StepPostcheck = "postcheck"
)

// Refusal is a finding that stops the hold: a writer, a fault or a configuration the hold does not
// run under, named by its step and what it found. The command exits 1 on one.
type Refusal struct {
	Step string
	What string
}

func (r *Refusal) Error() string { return r.Step + ": " + r.What }

// refusef is a Refusal of step, its finding formatted.
func refusef(step, format string, args ...any) *Refusal {
	return &Refusal{Step: step, What: fmt.Sprintf(format, args...)}
}

// Unknown is a reading a step could not make whole: an answer that did not come, did not decode, or
// was not complete. It stops the hold as UNKNOWN, which is never a pass: the command prints
// UNKNOWN, names what it could not read, and exits 2.
type Unknown struct {
	Step string
	What string
	Err  error
}

func (u *Unknown) Error() string {
	if u.Err == nil {
		return "UNKNOWN " + u.Step + ": " + u.What
	}
	return "UNKNOWN " + u.Step + ": " + u.What + ": " + u.Err.Error()
}

func (u *Unknown) Unwrap() error { return u.Err }

// unknownf is an Unknown of step, caused by err, its subject formatted.
func unknownf(step string, err error, format string, args ...any) *Unknown {
	return &Unknown{Step: step, What: fmt.Sprintf(format, args...), Err: err}
}

// ExitCode writes err's line, under program, the command's whole name ("envoy-infra streams
// update"), and answers the exit code: 0 for none, 2 for an UNKNOWN reading (an *Unknown, or
// holdrecord.ErrUnknown, a row or record that does not verify), and 1 for every refusal.
// `envoy-infra hold`, `buckets` and `streams update|census` each end through it.
func ExitCode(stderr io.Writer, program string, err error) int {
	if err == nil {
		return 0
	}
	if isUnknown(err) {
		line := err.Error()
		if !strings.HasPrefix(line, "UNKNOWN") {
			line = "UNKNOWN " + line
		}
		fmt.Fprintf(stderr, "%s: %s\n", program, line)
		return 2
	}
	fmt.Fprintf(stderr, "%s: %v\n", program, err)
	return 1
}

// isUnknown reports whether err is an UNKNOWN reading: an *Unknown, or holdrecord.ErrUnknown (a
// row or record that does not verify). ExitCode answers 2 for one, and a held command's stop after
// one records `unknown: read …; unread …` (RunHeld), never holdrecord.LeftUnchanged.
func isUnknown(err error) bool {
	var unknown *Unknown
	return errors.As(err, &unknown) || errors.Is(err, holdrecord.ErrUnknown)
}
