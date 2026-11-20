package jjmerge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Command is one git or jj child of the merge.
type Command struct {
	// Dir is the directory the command runs in, the workspace's root: jj reads the configuration
	// of the repository its working directory sits in (docs/testing.md), and prints paths
	// relative to it.
	Dir  string
	Args []string
	// Env is added to the caller's environment for this command alone. The caller's own
	// environment reaches every command unchanged: a pane's JJ_CONFIG is what writes the role's
	// trailers into the merge's description.
	Env   []string
	Stdin []byte
}

// Runner runs one command of the merge and answers what it printed on standard output. A command
// that runs and exits with a status other than 0 answers an *ExitError, with what it printed on
// standard output as well. A test gives the merge a Runner that fails one named step.
type Runner interface {
	Run(ctx context.Context, c Command) ([]byte, error)
}

// ExitError is a command that ran and exited with a status other than 0, or that a signal ended.
type ExitError struct {
	Args []string
	// Code is the exit status, or -1 where a signal ended the command.
	Code int
	// Signal names the signal that ended the command, in the words of Go's process state
	// ("signal: terminated"). It is empty where the command exited.
	Signal string
	Stderr string
}

func (e *ExitError) Error() string {
	text := fmt.Sprintf("%s exited %d", strings.Join(e.Args, " "), e.Code)
	if e.Signal != "" {
		text = fmt.Sprintf("%s ended with %s", strings.Join(e.Args, " "), e.Signal)
	}
	if said := strings.TrimSpace(e.Stderr); said != "" {
		text += ": " + said
	}
	return text
}

// Exec is the Runner of `legion merge`: each command is a child of this process, the git and jj
// first on its PATH.
type Exec struct{}

func (Exec) Run(ctx context.Context, c Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		failed := &ExitError{Args: c.Args, Code: exit.ExitCode(), Stderr: stderr.String()}
		// A signal leaves no exit status, and a child it ends often writes nothing on stderr, so
		// the signal is what says why the command failed.
		if failed.Code == -1 {
			failed.Signal = exit.ProcessState.String()
		}
		return stdout.Bytes(), failed
	}
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", strings.Join(c.Args, " "), err)
	}
	return stdout.Bytes(), nil
}
