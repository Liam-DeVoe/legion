//go:build unix

package main

import (
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// inode is the inode of the entry that info describes.
func inode(info fs.FileInfo) uint64 {
	return uint64(info.Sys().(*syscall.Stat_t).Ino)
}

// writable answers nil when this process may write in the directory dir, and the system's refusal
// when it may not.
func writable(dir string) error {
	if err := unix.Access(dir, unix.W_OK); err != nil {
		return &fs.PathError{Op: "write", Path: dir, Err: err}
	}
	return nil
}

// sameOwner gives the directory path the owner and the group of old, when they are others, through
// fsys.
func sameOwner(fsys fileSystem, path string, old fs.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	have, want := info.Sys().(*syscall.Stat_t), old.Sys().(*syscall.Stat_t)
	if have.Uid == want.Uid && have.Gid == want.Gid {
		return nil
	}
	return fsys.lchown(path, int(want.Uid), int(want.Gid))
}
