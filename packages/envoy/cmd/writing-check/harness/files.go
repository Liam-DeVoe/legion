package harness

import (
	"errors"
	"fmt"
	"io"
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
// whole that holds such an input; a file it writes in a directory it reads whole; and two files it
// writes that are one file, or one inside a directory it replaces whole: the write would empty or
// replace what the command reads, or write into it, or the later of two writes would replace the
// earlier. A path that does not exist yet is compared by where a write to it lands (realPath), so
// two flags that name one new file are refused before either is written. A file one flag both
// reads and writes, such as the output a run continues, is that flag's own. A command calls it
// before it writes anything, and refuses with exit 2.
func NoOutputIsAnInput(writes, reads []File) error {
	written, read := locate(writes), locate(reads)
	for i, w := range writes {
		for j, r := range reads {
			switch {
			case w.Flag == r.Flag:
			case w.Tree && written[i].holds(read[j]):
				return fmt.Errorf("%s, which this command replaces whole, holds %s: give another %s", w, r, w.Flag)
			case r.Tree && read[j].holds(written[i]):
				return fmt.Errorf("%s lies in %s, which this command reads whole: give another %s", w, r, w.Flag)
			case !written[i].same(read[j]):
			case w.Name == "":
				return fmt.Errorf("%s names %s: give another %s", w.Flag, r, w.Flag)
			default:
				return fmt.Errorf("%s is %s: give another %s", w, r, w.Flag)
			}
		}
	}
	for i, w := range writes {
		for j := i + 1; j < len(writes); j++ {
			switch v := writes[j]; {
			case w.Tree && written[i].holds(written[j]):
				return fmt.Errorf("%s, which this command replaces whole, holds %s, which it also writes: give another %s", w, v, v.Flag)
			case v.Tree && written[j].holds(written[i]):
				return fmt.Errorf("%s, which this command replaces whole, holds %s, which it also writes: give another %s", v, w, v.Flag)
			case !written[i].same(written[j]):
			case v.Name == "":
				return fmt.Errorf("%s names %s, which this command also writes: give another %s", v.Flag, w, v.Flag)
			default:
				return fmt.Errorf("%s is %s, which this command also writes: give another %s", v, w, v.Flag)
			}
		}
	}
	return nil
}

// located is a file looked up once (locate): its path, its os.Stat (nil where it does not exist
// or cannot be looked up) and its realPath.
type located struct {
	path string
	info os.FileInfo
	real string
}

func locate(files []File) []located {
	found := make([]located, len(files))
	for i, f := range files {
		found[i] = located{path: f.Path, real: realPath(f.Path)}
		if info, err := os.Stat(f.Path); err == nil {
			found[i].info = info
		}
	}
	return found
}

// same is whether a and b are one file: one that exists, by any two names (os.SameFile), or one
// path that may not exist yet.
func (a located) same(b located) bool {
	return a.info != nil && b.info != nil && os.SameFile(a.info, b.info) || a.real == b.real
}

// holds is whether dir, a directory a command replaces or reads whole, is f or lies above it,
// whether or not either exists.
func (dir located) holds(f located) bool {
	rel, err := filepath.Rel(dir.real, f.real)
	return err == nil && filepath.IsLocal(rel) || underDir(f.path, dir.info)
}

// realPath is where a write to path lands: path made absolute, its longest leading part that
// exists with its links resolved, and the rest as written. Two names of one file that does not
// exist yet, as through a link to its directory, give one realPath.
func realPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	for dir, rest := abs, ""; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		rest, dir = filepath.Join(filepath.Base(dir), rest), parent
	}
}

// underDir is whether path is the directory dir, of the os.Stat given (nil where it does not
// exist), or lies under it.
func underDir(path string, dir os.FileInfo) bool {
	if dir == nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for d := abs; ; d = filepath.Dir(d) {
		if info, err := os.Stat(d); err == nil && os.SameFile(info, dir) {
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

// ReplaceFile writes data to path whole, of mode perm (WriteBeside).
func ReplaceFile(path string, data []byte, perm os.FileMode) error {
	return WriteBeside(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteBeside writes path whole, of mode perm, by write: to a file CreateBeside makes, closed and
// renamed over path once write has written it. A failed write, close or rename removes that file
// and leaves path as it was.
func WriteBeside(path string, perm os.FileMode, write func(io.Writer) error) error {
	f, err := CreateBeside(path, perm)
	if err != nil {
		return err
	}
	err = write(f)
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
