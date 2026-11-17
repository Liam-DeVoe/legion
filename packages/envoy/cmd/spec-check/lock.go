package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// errLockHeld is tryLock's answer when another open file of the directory holds its lock.
var errLockHeld = errors.New("the lock is held")

// lockPoll is how often lockSpecDir tries the lock again while another run holds it.
const lockPoll = 20 * time.Millisecond

// specDirLock is the hold of one run of print on its directory: the open directory, whose flock is
// the lock, and each directory that the run made to write into, the deepest first.
type specDirLock struct {
	dir  *os.File
	made []string
}

// lockSpecDir takes the lock of print on dir: an exclusive flock on the directory itself, which
// the kernel releases when the process ends, so a run that is killed leaves no lock. Git keeps no
// empty directory, so print writes the first spec file of a repository into a directory that it
// makes: lockSpecDir makes dir and each missing directory above it first. When another run holds
// the lock, it tries again every lockPoll until wait ends, then refuses. A run that held the lock
// before can have removed dir, and another can have made it again, so the lock counts only when
// the open directory is still the directory that dir names.
func lockSpecDir(dir string, wait time.Duration) (*specDirLock, error) {
	deadline := time.Now().Add(wait)
	for {
		made, err := makeDirs(dir)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(dir)
		if err != nil {
			removeDirs(made)
			return nil, err
		}
		for err = tryLock(f); errors.Is(err, errLockHeld) && time.Now().Before(deadline); err = tryLock(f) {
			time.Sleep(lockPoll)
		}
		if err != nil {
			f.Close()
			if errors.Is(err, errLockHeld) {
				return nil, fmt.Errorf("another run of print holds the lock of the directory, and this run waited %s for it; run print again when that run ends", wait)
			}
			removeDirs(made)
			return nil, fmt.Errorf("cannot lock the directory: %w", err)
		}
		held, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		if now, err := os.Stat(dir); err == nil && os.SameFile(held, now) {
			return &specDirLock{dir: f, made: made}, nil
		}
		f.Close()
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another run of print removed and made the directory again while this run waited %s for its lock; run print again", wait)
		}
	}
}

// release ends the hold. When the run wrote nothing (kept is false), it first removes each
// directory that it made and that is still empty, so a run that print refuses leaves no directory.
func (l *specDirLock) release(kept bool) {
	if !kept {
		removeDirs(l.made)
	}
	l.dir.Close() // closing the last descriptor of the open directory releases its flock
}

// makeDirs makes dir and each directory above it that does not exist, and answers the ones that it
// made, the deepest first. A directory that another process makes at the same time is not one of
// them.
func makeDirs(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	var made []string
	for i := len(missing) - 1; i >= 0; i-- {
		switch err := os.Mkdir(missing[i], 0o755); {
		case err == nil:
			made = append([]string{missing[i]}, made...)
		case !errors.Is(err, fs.ErrExist):
			removeDirs(made)
			return nil, err
		}
	}
	return made, nil
}

// removeDirs removes each directory of dirs, the deepest first, while each is empty.
func removeDirs(dirs []string) {
	for _, d := range dirs {
		if os.Remove(d) != nil {
			return
		}
	}
}
