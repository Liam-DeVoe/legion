//go:build !linux

package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// openJJDir opens the directory at path, the workspace's .jj (openJJ), on a kernel other than
// Linux, where no issue pod runs: with O_DIRECTORY|O_NOFOLLOW, which such a kernel answers ELOOP
// for a link and ENOTDIR for a file. The Lstat only says which the refusal was.
func openJJDir(path string) (*os.File, error) {
	jj, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		if info, statErr := os.Lstat(path); errors.Is(err, unix.ELOOP) || (statErr == nil && info.Mode()&fs.ModeSymlink != 0) {
			return nil, fmt.Errorf("%w: %s: %w", errLinkedJJ, path, err)
		}
	}
	if err != nil {
		return nil, err
	}
	return jj, nil
}
