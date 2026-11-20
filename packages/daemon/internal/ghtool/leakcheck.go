package ghtool

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/procgroup"
)

// LeakCheckSource reads the shared public leak check at a tag, packed as the daemon serves it to a
// pod's launcher: the Kubernetes runtime's reader (runtime/sandbox's LeakCheckReader, a
// *leakguard.Source, which keeps one copy per tag), whose every error is one cause sentence
// (leakguard.Cause) wrapping what is underneath it (leakguard.Source.Get).
type LeakCheckSource interface {
	Get(ctx context.Context, tag string) (leakguard.Served, error)
}

// leakCheckDir is the directory under the daemon's state directory InstallLeakCheck installs the
// check into, beside the answers' own directories (Host.StateDir).
const leakCheckDir = "leak-check"

// InstallLeakCheck installs the shared public leak check source serves at tag into
// <stateDir>/leak-check, for the guard of a Kubernetes daemon's own gh tool (LeakCheck.Install),
// and answers the path of the installed check. Such a daemon's environment holds none of the three
// places the guard resolves a check from (leakguard's Resolve): the worker image carries the pin,
// LEGION_LEAK_CHECK_TAG, and not the check, which every pod's launcher installs for its own pod
// from what the daemon serves it. So the daemon installs its own copy the same way, with the
// launcher's own installer (leakguard.InstallServed: the unpack into a staging directory, the tag
// recorded beside the check, one `--help` run, and the rename over what the directory held), and
// reads it back as a role's shim reads its pod's install (leakguard.Installed), so the path it
// answers is a check of tag. The daemon reaps no child it did not start, so that run starts with
// procgroup.NoReaper. log takes the one line of an install that replaced what the directory held:
// the install of an earlier boot, at its tag.
//
// An error is the read's own (source's), or an *InstallError: the install's cause sentence, or the
// setup failure a role's shim would refuse to start with where the install does not read back as
// tag's.
func InstallLeakCheck(ctx context.Context, source LeakCheckSource, tag, stateDir string, log *slog.Logger) (string, error) {
	served, err := source.Get(ctx, tag)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir, leakCheckDir)
	if err := leakguard.InstallServed(ctx, leakguard.ServedInstall{
		Dir: dir, Tag: tag, Body: served.Body, Start: procgroup.NoReaper,
		Log: func(format string, args ...any) { log.Info("answers: " + fmt.Sprintf(format, args...)) },
	}); err != nil {
		return "", &InstallError{sentence: leakguard.Cause(err.Error(), nil), err: err}
	}
	check, err := leakguard.Installed([]string{leakguard.TagVariable + "=" + tag}, dir)
	if err != nil {
		return "", &InstallError{sentence: err.Error(), err: err}
	}
	return check, nil
}

// InstallError is an InstallLeakCheck failure after source served the check: the install of what
// it served (leakguard.InstallServed) or the read back of what was installed
// (leakguard.Installed). Installing again does not change it, since source keeps a read that
// succeeded (leakguard.Source) and so serves the next install the same body. Its text is the
// failure's sentence, and it wraps the failure.
type InstallError struct {
	sentence string
	err      error
}

func (e *InstallError) Error() string { return e.sentence }

func (e *InstallError) Unwrap() error { return e.err }

// LeakChecker is the shared public leak check a call's guard runs (Host.LeakCheck), asked at every
// call: Check answers the path of the check the guard is handed (leakguard.Options.Check), so the
// guard never resolves one of its own, or the *leakguard.SetupFailure that refuses the call, gh
// never started. Each runtime has one (internal/daemon's answersTools): a Kubernetes daemon's
// *LeakCheck, the copy it installs at its pin, and a tmux daemon's MachineLeakCheck, the
// machine's own.
type LeakChecker interface {
	Check() (string, error)
}

// LeakCheck is the shared public leak check a Kubernetes daemon's gh tool runs before every call
// (Host.LeakCheck): the copy the daemon installs at its pin (Install), which an answer's call may
// come before. Until a try of the install lands, Check refuses every call with the setup failure
// naming the pin and why the last try failed; from then on it answers the installed check, to the
// next call of an answer already open too. Once the daemon stops trying (Stop), the refusal says
// so. The daemon installs into it while the answers' calls read it, so it is safe for concurrent
// use.
type LeakCheck struct {
	tag string

	mu        sync.Mutex
	installed string
	failure   error
	stopped   bool
}

// NewLeakCheck is the check at tag, not yet installed.
func NewLeakCheck(tag string) *LeakCheck { return &LeakCheck{tag: tag} }

// Install makes one try of the install at the pin: the check source serves at it, installed
// under stateDir (InstallLeakCheck). A try that lands makes the installed check the one every
// call runs, and logs the tag and the path to log; one that fails makes its error why every call
// is refused until a try lands, and returns it.
func (c *LeakCheck) Install(ctx context.Context, source LeakCheckSource, stateDir string, log *slog.Logger) error {
	check, err := InstallLeakCheck(ctx, source, c.tag, stateDir, log)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.failure = err
		return err
	}
	c.installed, c.failure = check, nil
	log.Info("answers: installed the shared public leak check the gh tool's leak guard runs", "tag", c.tag, "check", check)
	return nil
}

// Stop records that the daemon tries the install no more until its next boot, over cause, a
// failure no wait changes, which every refusal names from then on.
func (c *LeakCheck) Stop(cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure, c.stopped = cause, true
}

// Check is the installed check a call runs, or, before an install lands, the
// *leakguard.SetupFailure that refuses the call: the check at the pin is not installed yet, or
// could not be installed once the daemon stopped trying (Stop), with the last failure underneath.
func (c *LeakCheck) Check() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.installed != "" {
		return c.installed, nil
	}
	state := "is not installed yet, and the daemon tries the install again until it is"
	if c.stopped {
		state = "could not be installed, and the daemon tries the install again only at its next boot"
	}
	return "", &leakguard.SetupFailure{
		What: leakguard.WhatGhCall,
		Why:  fmt.Sprintf("the shared public leak check at %s, the pin %s names, %s", c.tag, leakguard.TagVariable, state),
		Err:  c.failure,
	}
}

// MachineLeakCheck is the shared public leak check of the machine a tmux daemon runs on
// (Host.LeakCheck), resolved at each call in Environ, the daemon's own environment
// (Host.Environ), the way the tmux runtime resolves a pane's for each launch (runtime/tmux's
// resolveLeakCheck): leakguard's Resolve, PUBLIC_LEAK_CHECK, then public-leak-check.sh on PATH,
// then the pointer file under the configuration home. It resolves at each call because an
// operator's machine installs, moves and republishes the check while the daemon runs, and in the
// daemon's environment rather than the guard children's (Answer.guardEnv), whose PATH has gh's
// directory in front and which carries no PUBLIC_LEAK_CHECK. Nil Environ is this process's own,
// read at each call.
type MachineLeakCheck struct {
	Environ []string
}

// Check is the machine's check, or, on a machine that carries none, the setup failure the guard
// itself refuses such a call with (leakguard.WhyNoCheck), naming all three places.
func (m MachineLeakCheck) Check() (string, error) {
	check, err := leakguard.New(leakguard.Options{Env: m.Environ}).Resolve()
	if err != nil {
		return "", &leakguard.SetupFailure{What: leakguard.WhatGhCall, Why: leakguard.WhyNoCheck, Err: err}
	}
	return check, nil
}
