package workspace

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// openJJDir opens the directory at path, the workspace's .jj (openJJ), on Linux. The entry is
// opened itself with O_PATH|O_NOFOLLOW, which reads through no link, and its kind is read off that
// descriptor with fstat; only then is the directory opened for reading, through that same
// descriptor (openat of "."). An open of the path with O_DIRECTORY|O_NOFOLLOW would refuse a link
// on a Linux kernel, but gVisor, which issue pods run under, follows a link to a directory there.
func openJJDir(path string) (*os.File, error) {
	entry, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(entry)
	var stat unix.Stat_t
	if err := unix.Fstat(entry, &stat); err != nil {
		return nil, &fs.PathError{Op: "fstat", Path: path, Err: err}
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
	case unix.S_IFLNK:
		return nil, fmt.Errorf("%w: %s", errLinkedJJ, path)
	default:
		return nil, &fs.PathError{Op: "open", Path: path, Err: unix.ENOTDIR}
	}
	fd, err := unix.Openat(entry, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
