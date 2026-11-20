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
// <stateDir>/leak-check, for the guard of a Kubernetes daemon's own gh tool (LeakCheck), and
// answers the path of the installed check. Such a daemon's environment holds none of the three
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

// LeakCheck is the shared public leak check a Kubernetes daemon's gh tool runs before every call
// (Host.LeakCheck): the copy the daemon installs at its pin (InstallLeakCheck), which an answer's
// call may come before. Until the install lands, Check refuses every call with the setup failure
// naming the pin and why the install's last try failed; from then on it answers the installed
// check, to the next call of an answer already open too. The daemon records into it while the
// answers' calls read it, so it is safe for concurrent use.
type LeakCheck struct {
	tag string

	mu        sync.Mutex
	installed string
	failure   error
}

// NewLeakCheck is the check at tag, not yet installed.
func NewLeakCheck(tag string) *LeakCheck { return &LeakCheck{tag: tag} }

// Installed records check, the path InstallLeakCheck answered, as the check every call runs.
func (c *LeakCheck) Installed(check string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.installed, c.failure = check, nil
}

// Failed records err as why the latest try of the install failed, which every refusal names
// until a try lands.
func (c *LeakCheck) Failed(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = err
}

// Check is the installed check a call runs, or, before the install lands, the
// *leakguard.SetupFailure that refuses the call: the check at the pin is not installed yet, with
// the install's last failure underneath.
func (c *LeakCheck) Check() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.installed != "" {
		return c.installed, nil
	}
	return "", &leakguard.SetupFailure{
		What: leakguard.WhatGhCall,
		Why: fmt.Sprintf("the shared public leak check at %s, the pin %s names, is not installed yet, and the daemon tries the install again until it is",
			c.tag, leakguard.TagVariable),
		Err: c.failure,
	}
}

// MachineLeakCheck is the shared public leak check of the machine a tmux daemon runs on, resolved
// in environ, the daemon's own, the way the daemon resolves a pane's for each launch
// (runtime/tmux's resolveLeakCheck): the guard's own resolution (leakguard's Resolve),
// PUBLIC_LEAK_CHECK, then public-leak-check.sh on PATH, then the pointer file under the
// configuration home. Its error names all three.
func MachineLeakCheck(environ []string) (string, error) {
	return leakguard.New(leakguard.Options{Env: environ}).Resolve()
}
