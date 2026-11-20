package jjmerge

import (
	"context"
	"errors"
	"testing"
)

// A child that a signal ends is named with the signal, in the words of Go's process state, and
// not as an exit status of -1 with nothing after it.
func TestExecNamesTheSignalThatEndedACommand(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), Command{Dir: t.TempDir(), Args: []string{"sh", "-c", "kill -TERM $$"}})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Signal != "signal: terminated" {
		t.Fatalf("Exec.Run of a command a signal ends = %#v, want an *ExitError whose Signal is %q", err, "signal: terminated")
	}
	if got := err.Error(); got != "sh -c kill -TERM $$ ended with signal: terminated" {
		t.Errorf("the error reads %q, want it to name the signal", got)
	}
}
