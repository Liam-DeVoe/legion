package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// A File is a file a command reads or writes: the flag, or the environment variable, that names
// it or names the directory that holds it, and its path.
type File struct {
	Flag, Path string
	// Name is the file's name in the flag's directory, or beside the flag's file; "" for the file
	// the flag names itself.
	Name string
	// Tree marks a directory the command writes or reads whole, with every file in it: a
	// directory it replaces, or one it reads every file of.
	Tree bool
}

func (f File) String() string {
	switch {
	case f.Name != "":
		return f.Name + " of " + f.Flag
	case f.Tree:
		return "the " + f.Flag + " directory"
	}
	return "the " + f.Flag + " file"
}

// Files is the file path, which flag names; or, given names, the files of those names in the
// directory path. An empty path names no file.
func Files(flag, path string, names ...string) []File {
	if path == "" {
		return nil
	}
	if len(names) == 0 {
		return []File{{Flag: flag, Path: path}}
	}
	files := make([]File, len(names))
	for i, name := range names {
		files[i] = File{Flag: flag, Path: filepath.Join(path, name), Name: name}
	}
	return files
}

// NoOutputIsAnInput refuses a file a command writes that is a file another of its flags names as
// an input, by one path or two (a link, or another name of one file); a directory it replaces
// whole that holds such an input; and a file it writes in a directory it reads whole: the write
// would empty or replace what the command reads, or write into it. A file one flag both reads
// and writes, such as the output a run continues, is that flag's own. A command calls it before
// it writes anything, and refuses with exit 2.
func NoOutputIsAnInput(writes, reads []File) error {
	for _, w := range writes {
		for _, r := range reads {
			switch {
			case w.Flag == r.Flag:
			case w.Tree && underDir(r.Path, w.Path):
				return fmt.Errorf("%s, which this command replaces whole, holds %s: give another %s", w, r, w.Flag)
			case r.Tree && underDir(w.Path, r.Path):
				return fmt.Errorf("%s lies in %s, which this command reads whole: give another %s", w, r, w.Flag)
			case !SameFile(w.Path, r.Path):
			case w.Name == "":
				return fmt.Errorf("%s names %s: give another %s", w.Flag, r, w.Flag)
			default:
				return fmt.Errorf("%s is %s: give another %s", w, r, w.Flag)
			}
		}
	}
	return nil
}

// SameFile is whether a and b name one file or directory that exists, by one path or two.
func SameFile(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// underDir is whether path is the directory dir or lies under it.
func underDir(path, dir string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for d := abs; ; d = filepath.Dir(d) {
		if SameFile(d, dir) {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// CreateBeside creates a file of a name of its own in path's directory, of mode perm, for a write
// that is renamed over path once it is whole (os.CreateTemp): no other run writes, renames or
// removes that file, so two runs on one path never truncate or remove each other's. Where the
// write fails, the caller removes the file by the name it answers, and no other.
func CreateBeside(path string, perm os.FileMode) (*os.File, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// ReplaceFile writes data to path whole, of mode perm: to a file CreateBeside makes, renamed over
// path. A failed write removes that file and leaves path as it was.
func ReplaceFile(path string, data []byte, perm os.FileMode) error {
	f, err := CreateBeside(path, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// LockRun takes an exclusive advisory lock (flock) on the file path, making it where it is not
// there, for a run that writes the files the lock guards under names a later run continues: a
// second run that asks for the lock while the first holds it is refused at once, and held says
// so. The lock goes when the answered file is closed or the process ends; the file stays, so no
// run removes a lock another holds.
func LockRun(path string) (lock *os.File, held bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.Is(err, syscall.EWOULDBLOCK), err
	}
	return f, false, nil
}
