package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/architecture/notes"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// stagingInfix is the part of the name of the directory that a run of print builds beside its spec
// directory: "." and the spec directory's name, stagingInfix, and the inode of the spec directory
// before the run (stagingPath).
const stagingInfix = ".spec-check-print-"

// stagingPath is the directory that a run of print builds beside dir, whose inode is ino before the
// run: one run at a time holds the lock of dir, so one name serves each run.
func stagingPath(dir string, ino uint64) string {
	return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+stagingInfix+strconv.FormatUint(ino, 10))
}

// fileSystem is each call by which print changes the file system or makes a change durable: print
// passes osFileSystem, and a test passes one that fails a call, or stops the run after a call.
type fileSystem interface {
	// mkdir makes the directory path with the mode perm.
	mkdir(path string, perm fs.FileMode) error
	// create makes the new file path, mode 0644, open to write, and refuses when path exists.
	create(path string) (*os.File, error)
	// write writes content to f.
	write(f *os.File, content []byte) error
	// sync puts f, a file or a directory, on the disk.
	sync(f *os.File) error
	// chmod gives path the mode mode.
	chmod(path string, mode fs.FileMode) error
	// lchown gives path the owner uid and the group gid, and does not follow a symbolic link.
	lchown(path string, uid, gid int) error
	// link makes newname a second name of the file oldname.
	link(oldname, newname string) error
	// lock takes the exclusive flock of the open directory f without waiting (tryLock).
	lock(f *os.File) error
	// exchange puts the directory a in the place of the directory b, and b in the place of a,
	// with one call.
	exchange(a, b string) error
	// moveNoReplace renames from to to, and refuses when an entry has the name to.
	moveNoReplace(from, to string) error
	// remove removes the file or the empty directory path.
	remove(path string) error
}

// osFileSystem is the file system of the process.
type osFileSystem struct{}

func (osFileSystem) mkdir(path string, perm fs.FileMode) error { return os.Mkdir(path, perm) }

func (osFileSystem) create(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

func (osFileSystem) write(f *os.File, content []byte) error {
	_, err := f.Write(content)
	return err
}

func (osFileSystem) sync(f *os.File) error                     { return f.Sync() }
func (osFileSystem) chmod(path string, mode fs.FileMode) error { return os.Chmod(path, mode) }
func (osFileSystem) lchown(path string, uid, gid int) error    { return os.Lchown(path, uid, gid) }
func (osFileSystem) link(oldname, newname string) error        { return os.Link(oldname, newname) }
func (osFileSystem) lock(f *os.File) error                     { return tryLock(f) }
func (osFileSystem) remove(path string) error                  { return os.Remove(path) }

// writeDir changes the spec files of dir, files, to after in one step, so that a reader, and a
// kill, sees the spec files as they were or as the run leaves them. It builds a new directory
// beside dir (stagingPath) that holds each spec file of after: a file that changes written and
// synced, and each other as a hard link to it, the same file, or as a copy where the system
// refuses the link (linkOrCopy). The new directory takes the mode and the owner of dir. Then
// writeDir locks the new directory, so that a run that opens dir after the exchange waits for this
// one, exchanges the two directories with one call (exchange), syncs their parent, and finishes
// the old directory (finishOld): it moves each entry that is no spec file, a file of another name,
// a symbolic link or a subdirectory, which print neither reads nor changes, into dir, the same
// entry, and removes the rest. A step that fails before the exchange removes the new directory and
// leaves dir as it was; a system that cannot exchange two directories refuses the exchange, so
// print refuses with dir as it was, and names the way out where it knows one (exchangeRefusal). A
// run that is killed leaves the new or the old directory beside dir, which the next run finishes
// (cleanLeftovers). A directory that the process may not write is refused, as the files of such a
// directory were. warnings is a line for each step after the exchange that did not finish, or for
// an entry that another program changed while the run built the new directory, which writeDir
// keeps beside dir. Each change that it makes in the file system goes through fsys.
func writeDir(fsys fileSystem, dir string, lock *specDirLock, files, after map[string][]byte) (warnings []string, err error) {
	var changed []string
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if old, held := files[name]; !held || !bytes.Equal(old, after[name]) {
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := writable(dir); err != nil {
		return nil, err
	}
	old, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	staging := stagingPath(dir, inode(old))
	if err := fsys.mkdir(staging, 0o700); err != nil {
		return nil, err
	}
	exchanged := false
	defer func() {
		if !exchanged {
			removeNew(fsys, staging)
		}
	}()
	for _, name := range slices.Sorted(maps.Keys(after)) {
		to := filepath.Join(staging, name)
		if slices.Contains(changed, name) {
			err = writeSynced(fsys, to, after[name], 0o644)
		} else {
			err = linkOrCopy(fsys, filepath.Join(dir, name), to, after[name])
		}
		if err != nil {
			return nil, err
		}
	}
	if err := fsys.chmod(staging, old.Mode()&(fs.ModePerm|fs.ModeSetgid|fs.ModeSticky)); err != nil {
		return nil, err
	}
	if err := sameOwner(fsys, staging, old); err != nil {
		return nil, err
	}
	built, err := os.Open(staging)
	if err != nil {
		return nil, err
	}
	lock.hold(built)
	if err := fsys.lock(built); err != nil {
		return nil, err
	}
	if err := fsys.sync(built); err != nil {
		return nil, err
	}
	if err := fsys.exchange(staging, dir); err != nil {
		return nil, exchangeRefusal(err)
	}
	exchanged = true
	if err := syncDir(fsys, filepath.Dir(dir)); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: the system did not sync the directory that holds it, so a power loss can undo the run: %s", dir, err))
	}
	return append(warnings, finishOld(fsys, staging, dir, files)...), nil
}

// exchangeRefusal is the error of writeDir when the system refuses the exchange with err. For the
// two refusals that a writer meets, it names the cause and the way out: EXDEV, which overlayfs
// answers for a directory that comes from the layers of a container image, and EBUSY, which the
// system answers for a directory that is a mount point. A copy of the directory in its place, made
// in the container, and a mount of the checkout, or of the directory above the mount point, each
// let print run there (measured in a container; the pull request says how).
func exchangeRefusal(err error) error {
	var wayOut string
	switch {
	case errors.Is(err, syscall.EXDEV):
		wayOut = ". The file system refuses so to move a directory that comes from the layers of a container image (overlayfs): put a copy of the directory in its place in the container (cp -a <dir> <dir>.copy && rm -rf <dir> && mv <dir>.copy <dir>), or mount the checkout into the container, then run print again"
	case errors.Is(err, syscall.EBUSY):
		wayOut = ". The system refuses so to move a directory that is a mount point: mount a directory above it instead, such as the checkout (docker run -v <checkout>:/work, not -v <checkout>/.dispatch/spec:/work/spec), then run print again"
	}
	return fmt.Errorf("print changes its files in one step, by an exchange of the directory with a new directory that it builds beside it, and the system refused the exchange: %w%s", err, wayOut)
}

// removeNew removes staging, a new directory that the run built and did not exchange, which holds
// only files that the run made: each file, then staging. What it cannot remove, the next run
// removes (cleanLeftovers).
func removeNew(fsys fileSystem, staging string) {
	list, _ := os.ReadDir(staging)
	for _, entry := range list {
		_ = fsys.remove(filepath.Join(staging, entry.Name()))
	}
	_ = fsys.remove(staging)
}

// linkOrCopy puts at to the spec file from, which the run does not change and whose text is
// content: a hard link to it, the same file, or, where the system refuses the link (another user's
// file under fs.protected_hardlinks, a file on another file system, a file at its bound of links,
// a file system with no hard links), a copy of content with the mode of from, which the process
// owns.
func linkOrCopy(fsys fileSystem, from, to string, content []byte) error {
	refused := fsys.link(from, to)
	if refused == nil {
		return nil
	}
	info, err := os.Lstat(from)
	if err == nil {
		err = writeSynced(fsys, to, content, info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))
	}
	if err != nil {
		return fmt.Errorf("%w, and print could not copy the file either: %w", refused, err)
	}
	return nil
}

// writeSynced writes content to a new file at path, made by fsys, syncs it and gives it the mode
// mode.
func writeSynced(fsys fileSystem, path string, content []byte, mode fs.FileMode) error {
	f, err := fsys.create(path)
	if err != nil {
		return err
	}
	err = fsys.write(f, content)
	if err == nil {
		err = fsys.sync(f)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = fsys.chmod(path, mode)
	}
	return err
}

// syncDir syncs the directory dir, so that the names it holds are on the disk.
func syncDir(fsys fileSystem, dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = fsys.sync(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// finishOld finishes old, the directory that a run of print exchanged out of dir: it moves each
// entry that dir does not hold into dir, the same entry, and removes each spec file of old that
// dir holds in its new text, then old. files is the text of each spec file of dir when the run
// read it: a spec file of old is removed when it is a second name of the file that dir holds, or
// when its text is the text that the run read. A run that finishes a killed run (cleanLeftovers)
// passes no files, and removes each spec file of old that dir holds. An entry that it can neither
// move nor remove, which another program made or changed in old while the run built the new
// directory, it keeps, with old under another name (keep), and it answers a line that names them.
func finishOld(fsys fileSystem, old, dir string, files map[string][]byte) []string {
	list, err := os.ReadDir(old)
	if err != nil {
		return []string{fmt.Sprintf("print could not read %s, which held %s before the run, so it leaves it: %s", old, dir, err)}
	}
	var kept []string
	for _, entry := range list {
		name := entry.Name()
		path, now := filepath.Join(old, name), filepath.Join(dir, name)
		current, err := os.Lstat(now)
		if errors.Is(err, fs.ErrNotExist) {
			if fsys.moveNoReplace(path, now) != nil {
				kept = append(kept, name)
			}
			continue
		}
		if err != nil || !entry.Type().IsRegular() || !current.Mode().IsRegular() || !removable(path, current, files, name) || fsys.remove(path) != nil {
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 && fsys.remove(old) == nil {
		return nil
	}
	path, err := keep(fsys, old)
	if err != nil {
		return []string{fmt.Sprintf("print could not remove %s, which held %s before the run: %s", old, dir, err)}
	}
	return []string{fmt.Sprintf("print kept %s, which held %s before the run: another program made or changed %s there while print ran, so compare each with the entry of that name in %s",
		path, dir, strings.Join(kept, ", "), dir)}
}

// removable reports whether finishOld may remove the regular file path of an old directory, whose
// name in dir is the regular file that current describes: it is a second name of that file; or
// files is nil and name is the name of a spec file; or its text is files[name], the text that the
// run read.
func removable(path string, current fs.FileInfo, files map[string][]byte, name string) bool {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return false
	case os.SameFile(info, current):
		return true
	case files == nil:
		return specgraph.IsFile(name, notes.KindFile)
	}
	text, held := files[name]
	content, err := os.ReadFile(path)
	return held && err == nil && bytes.Equal(content, text)
}

// undoNew removes staging, a new directory that a killed run of print built beside dir and did not
// exchange: it holds only new files and second names of the spec files of dir. An entry that it
// cannot remove, it keeps, with staging under another name (keep), and answers a line that names
// it.
func undoNew(fsys fileSystem, staging, dir string) []string {
	list, err := os.ReadDir(staging)
	if err != nil {
		return []string{fmt.Sprintf("print could not read %s, so it leaves it: %s", staging, err)}
	}
	var kept []string
	for _, entry := range list {
		if !entry.Type().IsRegular() || fsys.remove(filepath.Join(staging, entry.Name())) != nil {
			kept = append(kept, entry.Name())
		}
	}
	if len(kept) == 0 && fsys.remove(staging) == nil {
		return nil
	}
	path, err := keep(fsys, staging)
	if err != nil {
		return []string{fmt.Sprintf("print could not remove %s: %s", staging, err)}
	}
	return []string{fmt.Sprintf("print kept %s, which a killed run of print left beside %s: it holds %s, which print did not make", path, dir, strings.Join(kept, ", "))}
}

// keep gives path, a directory that print leaves, a name that cleanLeftovers does not take as its
// own, and answers that name.
func keep(fsys fileSystem, path string) (string, error) {
	kept := path + "-kept-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	return kept, fsys.moveNoReplace(path, kept)
}

// cleanLeftovers finishes each directory that a killed run of print left beside dir, whose inode
// is ino, and answers a line for each. The caller holds the lock of dir, and a run holds that lock
// from before it makes its directory beside dir until it has removed it, so each such directory is
// a dead run's. A directory is print's own when its name is the name that stagingPath gives and the
// inode in that name is the inode of dir, then it is a new directory that the run did not exchange
// (undoNew), or its own inode, then it is the old directory that the run exchanged out of dir
// (finishOld). Each other entry of that form it passes over with a line that says so.
func cleanLeftovers(fsys fileSystem, dir string, ino uint64) []string {
	parent, prefix := filepath.Dir(dir), "."+filepath.Base(dir)+stagingInfix
	list, err := os.ReadDir(parent)
	if err != nil {
		return []string{fmt.Sprintf("print could not read %s, so it cannot finish what a killed run of print left there: %s", parent, err)}
	}
	var lines []string
	for _, entry := range list {
		rest, ok := strings.CutPrefix(entry.Name(), prefix)
		made, err := strconv.ParseUint(rest, 10, 64)
		if !ok || err != nil || strconv.FormatUint(made, 10) != rest {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		info, err := entry.Info()
		switch {
		case err != nil || !entry.IsDir():
			lines = append(lines, fmt.Sprintf("print passes over %s: it has the name of a directory that print builds beside %s, and it is no directory", path, dir))
		case made == ino:
			lines = append(lines, fmt.Sprintf("print removed %s, a new directory that a killed run of print built and did not put in the place of %s", path, dir))
			lines = append(lines, undoNew(fsys, path, dir)...)
		case made == inode(info):
			lines = append(lines, fmt.Sprintf("print finished %s, the directory that a killed run of print put out of the place of %s: it moved each entry that is no spec file into %s", path, dir, dir))
			lines = append(lines, finishOld(fsys, path, dir, nil)...)
		default:
			lines = append(lines, fmt.Sprintf("print passes over %s: it has the name of a directory that print builds beside %s, and no run of print on %s made it", path, dir, dir))
		}
	}
	return lines
}
