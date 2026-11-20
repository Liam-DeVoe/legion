package launcher

import (
	"fmt"
	"slices"
	"sync"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// indexNotInstalled begins the cause of every slice of the leak check's index the launcher
// refuses, as leakguard.InstallIndex begins its own errors, which the launcher answers whole.
const indexNotInstalled = "the leak check index could not be installed: "

// indexInstall is the launcher process's one writer of the leak check's index of sandbox lines,
// shared by every role manager of the container as checkInstall is, the controller's single role
// included. The daemon reads the index the builder publishes (leakguard.IndexSource) and pushes
// each new one in launcher_leak_check_index slices on every connection whose hello says the
// launcher takes it (shimwire.LauncherHello.LeakCheckIndex). This rebuilds a sequence in memory,
// checks the whole body's digest, and installs it in dir by a rename (leakguard.InstallIndex),
// where the check in every role reads it, at its home's leakguard.IndexDir.
//
// Every slice of every role's connection is taken under mu, the install included.
// leakguard.InstallIndex keeps the newer of two indexes only of calls that do not overlap: two at
// once can both read the held file before either renames, and the older can rename last. So of
// two connections that push an older and a newer index at once the folder ends with the newer,
// and of two that push the same index the second finds it installed and is answered Installed at
// once.
type indexInstall struct {
	// dir is the folder the index is written in (Config.LeakCheckIndexDir), never "".
	dir string

	mu sync.Mutex
	// sequence is the id of the slice sequence held, with the count and the digest its slices
	// agree on, and slices what has arrived of it. A sequence refused whole keeps its id with no
	// slices, and refused is why, the answer to every later slice of the same push; refused is ""
	// while a sequence is being assembled.
	sequence string
	count    int
	digest   string
	slices   [][]byte
	refused  string
	// installed is the digest of the last index whose push ended Installed: this process put it in
	// the folder, or found there one built at its time or later, which leakguard.InstallIndex keeps.
	installed string
}

// newIndexInstall is the install of a launcher whose configuration names dir as its index folder,
// and nil for "": such a launcher writes no index, and its hello says nothing of one. It writes the
// folder's pushed marker (leakguard.MarkIndexPushed) before it answers, and the launcher builds it
// before it dials the daemon (newManagers), so the marker is there before any role starts: a check
// that finds it never downloads the index, which the daemon pushes instead. A marker that cannot
// be written is the launcher's start refused, naming why.
func newIndexInstall(dir string) (*indexInstall, error) {
	if dir == "" {
		return nil, nil
	}
	if err := leakguard.MarkIndexPushed(dir); err != nil {
		return nil, fmt.Errorf("launcher: %w", err)
	}
	return &indexInstall{dir: dir}, nil
}

// takes is what a hello says of the index: whether this launcher writes one.
func (c *indexInstall) takes() bool { return c != nil }

// receive takes one slice of an index. A slice of the digest installed last is answered Installed
// at once: that push is over, and the daemon sends no more of it. The launcher holds one sequence
// at a time, and a push sends its slices in order from the first, so a first slice of another
// sequence begins that one in place of the one held, whose push is over or lost, and a later slice
// of a sequence not held is refused, since its earlier slices are gone. Nothing is written until
// the sequence is whole and rebuilds the digest every slice declares. The slice that makes it
// whole installs it and is answered Installed, the folder keeping a newer index against it
// included; the last slice of a sequence that still misses one is refused. A sequence refused
// whole drops its slices, and its refusal answers every later slice of the same push, as another
// role's connection pushing it at once sends; a push of it begun again at its first slice is taken
// afresh. Every refusal is one line, why the index could not be installed, which the daemon logs.
func (c *indexInstall) receive(frame shimwire.LauncherLeakCheckIndex) shimwire.LauncherLeakCheckIndexResult {
	refused := func(cause string) shimwire.LauncherLeakCheckIndexResult {
		return shimwire.LauncherLeakCheckIndexResult{ID: frame.ID, Index: frame.Index, Error: cause}
	}
	if c == nil {
		return refused(indexNotInstalled + "this launcher has no folder to write it in (--leak-check-index-dir)")
	}
	if err := frame.Validate(); err != nil {
		return refused(indexNotInstalled + err.Error())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if frame.SHA256 == c.installed {
		return shimwire.LauncherLeakCheckIndexResult{ID: frame.ID, Index: frame.Index, OK: true, Installed: true}
	}
	if frame.ID == c.sequence && c.refused != "" && frame.Index != 0 {
		return refused(c.refused)
	}
	if frame.ID != c.sequence || c.refused != "" {
		if frame.Index != 0 {
			holds := c.sequence
			if holds == "" {
				holds = "no sequence"
			}
			return refused(fmt.Sprintf("%sslice %d of %d of %s arrived while the launcher holds none of its earlier slices (it holds %s); a push of it begins again at slice 0",
				indexNotInstalled, frame.Index, frame.Count, frame.ID, holds))
		}
		c.sequence, c.count, c.digest, c.slices, c.refused = frame.ID, frame.Count, frame.SHA256, make([][]byte, frame.Count), ""
	}
	if frame.Count != c.count || frame.SHA256 != c.digest {
		return refused(fmt.Sprintf("%sslice %d of %d of sha256 %s does not match the %d slices of sha256 %s its sequence %s holds",
			indexNotInstalled, frame.Index, frame.Count, frame.SHA256, c.count, c.digest, c.sequence))
	}
	c.slices[frame.Index] = frame.Payload
	body := joined(c.slices)
	if body == nil {
		if frame.Index < frame.Count-1 {
			return shimwire.LauncherLeakCheckIndexResult{ID: frame.ID, Index: frame.Index, OK: true}
		}
		missing := slices.IndexFunc(c.slices, func(slice []byte) bool { return slice == nil })
		return refused(c.end(fmt.Sprintf("%sslice %d of %d never arrived before the last slice of %s",
			indexNotInstalled, missing, frame.Count, frame.ID)))
	}
	if got := shimwire.BundleDigest(body); got != frame.SHA256 {
		return refused(c.end(fmt.Sprintf("%sits slices rebuild a body of sha256 %s, not the %s they declare",
			indexNotInstalled, got, frame.SHA256)))
	}
	if _, err := leakguard.InstallIndex(c.dir, body); err != nil {
		return refused(c.end(err.Error()))
	}
	c.sequence, c.count, c.digest, c.slices, c.installed = "", 0, "", nil, frame.SHA256
	return shimwire.LauncherLeakCheckIndexResult{ID: frame.ID, Index: frame.Index, OK: true, Installed: true}
}

// end ends the sequence held, refused for cause: its slices are dropped, and cause, which it
// answers, answers every later slice of the same push (receive).
func (c *indexInstall) end(cause string) string {
	c.slices, c.refused = nil, cause
	return cause
}
