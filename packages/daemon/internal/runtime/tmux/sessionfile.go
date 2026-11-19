package tmux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// sessionFileOf is the file a resume continues the claim's recorded session from, and the
// sessions directories it searched for it: the recorded path itself when it is there, and otherwise
// the same session found by its file name in a working directory's folder, first of the recorded
// path's own sessions directory, then of the one paneSessions resolves, where the pane's harness
// keeps its sessions (Launcher.SessionsDir), resolved only when the recorded path is not there;
// nil is none. Oh My Pi keeps a session as
// `<sessions>/<folder named for the working directory>/<timestamp>_<session id>.jsonl` (Claude Code
// as `<projects>/<folder>/<session id>.jsonl`), so a session carried onto this host from where it
// was recorded — a pod, whose path names the image's home and profile — sits in a folder of the
// pane's own sessions directory, named for the pod's working directory or the pane's: each search
// looks one level below a sessions directory for a file of the recorded name, or, for Oh My Pi's
// name, of any timestamp with the recorded session id. Of several in one directory, the one written
// last is taken. found is "" when no directory holds the session; err is a failure to look, never
// the session's absence: a folder that cannot be looked in is runtime.ErrSessionStore, a store
// that did not answer, which the claim's machine waits out a minute at a time with the session
// kept, and a pane sessions directory that cannot be resolved is the launch's own failure. A folder
// another user owns and denies this daemon's user (othersFolder) is passed over at every level:
// the recorded path's own, a sessions directory, a folder of one and a session in it. It is no part
// of this daemon's session store, so a session under it is searched for elsewhere, as a session
// that is not there is: a takeover whose pod recorded its session under the image's home, which
// is another user's folder on this host, finds the copy carried into the pane's sessions directory.
func sessionFileOf(recorded string, paneSessions func() (string, error)) (found string, searched []string, err error) {
	if _, err := os.Stat(recorded); err == nil {
		return recorded, nil, nil
	} else if !errors.Is(err, os.ErrNotExist) && !othersFolder(recorded, err) {
		return "", nil, fmt.Errorf("%w: %v", runtime.ErrSessionStore, err)
	}
	searched = []string{filepath.Dir(filepath.Dir(recorded))}
	if paneSessions != nil {
		pane, err := paneSessions()
		if err != nil {
			return "", nil, err
		}
		if pane != "" && pane != searched[0] {
			searched = append(searched, pane)
		}
	}
	name := filepath.Base(recorded)
	for _, sessions := range searched {
		if found, err = newestSession(sessions, name); err != nil || found != "" {
			return found, searched, err
		}
	}
	return "", searched, nil
}

// newestSession is the regular file written last among those one folder below sessions named name
// or, for Oh My Pi's `<timestamp>_<session id>.jsonl`, of any timestamp with name's session id; ""
// for none. On equal times a file of the recorded name is taken before one of the session id alone.
// A directory or name holding a glob character is searched nowhere: it would match other sessions.
// A sessions directory that is not there holds no session, and nor does an entry of it that is no
// folder, or a folder another user owns and denies this daemon's user: it is no part of this
// daemon's session store, as a sessions directory such as /tmp holds folders of other users. A
// sessions directory that is there and cannot be read, a folder of this daemon's own user that
// cannot be read, a folder that fails otherwise, or a session in one that cannot be looked at is
// runtime.ErrSessionStore, since the session may be in it: calling it missing would start a fresh
// session over the saved one.
func newestSession(sessions, name string) (string, error) {
	if strings.ContainsAny(sessions+name, `*?[\`) {
		return "", nil
	}
	ofID := ""
	if stem, ok := strings.CutSuffix(name, ".jsonl"); ok {
		if _, id, ok := strings.Cut(stem, "_"); ok && id != "" {
			ofID = "_" + id + ".jsonl"
		}
	}
	folders, err := sessionEntries(sessions)
	if err != nil {
		return "", err
	}
	var named, identified []string
	for _, folder := range folders {
		dir := filepath.Join(sessions, folder.Name())
		entries, err := sessionEntries(dir)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			switch {
			case entry.Name() == name:
				named = append(named, filepath.Join(dir, entry.Name()))
			case ofID != "" && strings.HasSuffix(entry.Name(), ofID):
				identified = append(identified, filepath.Join(dir, entry.Name()))
			}
		}
	}
	newest, newestAt := "", time.Time{}
	for _, candidate := range append(named, identified...) {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) || othersFolder(candidate, err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("%w: %v", runtime.ErrSessionStore, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(newestAt) {
			newest, newestAt = candidate, info.ModTime()
		}
	}
	return newest, nil
}

// sessionEntries is what dir, a sessions directory or a folder of one, holds: nothing when it is
// not there or is no directory, or when it is under a folder another user owns that denies this
// daemon's user (othersFolder), and runtime.ErrSessionStore when it is there and cannot be read
// otherwise.
func sessionEntries(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || othersFolder(dir, err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", runtime.ErrSessionStore, err)
	}
	return entries, nil
}

// othersFolder is whether err, the refusal to read or look at path, is this daemon's user denied
// by a folder that another user owns: path itself, or the nearest folder above it that can be
// looked at, when the denial is of a folder on the way to it. A folder whose owner cannot be read
// is not taken for another's.
func othersFolder(path string, err error) bool {
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		return false
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		info, statErr := os.Stat(dir)
		if statErr == nil {
			owner, ok := info.Sys().(*syscall.Stat_t)
			return ok && int(owner.Uid) != os.Geteuid()
		}
		if !errors.Is(statErr, syscall.EACCES) && !errors.Is(statErr, syscall.EPERM) || dir == filepath.Dir(dir) {
			return false
		}
	}
}

// searchedIn is the sessions directories a search for a session found nothing in (sessionFileOf),
// as a refusal names them.
func searchedIn(searched []string) string {
	if len(searched) == 2 {
		return searched[0] + " or of the pane's sessions directory " + searched[1]
	}
	return strings.Join(searched, ", ")
}
