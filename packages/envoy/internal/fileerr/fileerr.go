// Package fileerr is why a file a setting names could not be read, in fixed words: the class of
// the filesystem error, never its text. An *fs.PathError quotes the path, and the path a setting
// holds can be a credential pasted where the path goes, so a refusal names the setting and this
// class, never the path.
package fileerr

import (
	"errors"
	"io/fs"
	"syscall"
)

// Class is err's class, err being an error reading a file: "it does not exist", "permission is
// denied", "it is a directory", or "it could not be read".
func Class(err error) string {
	var pathErr *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "it does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "permission is denied"
	case errors.As(err, &pathErr) && errors.Is(pathErr.Err, syscall.EISDIR):
		return "it is a directory"
	}
	return "it could not be read"
}
