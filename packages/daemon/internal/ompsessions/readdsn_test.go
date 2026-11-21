package ompsessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadDSN's refusals name neither the path nor what the file holds: a made-up database URL
// pasted where the file's path goes is a file that does not exist, refused by why alone and still
// errors.Is fs.ErrNotExist, and an empty file is refused as one.
func TestReadDSNNamesNoPath(t *testing.T) {
	const pasted = "postgres:madeUpDsnPassword0123456789"
	_, err := ReadDSN(filepath.Join(t.TempDir(), pasted))
	if want := "read the database URL file: it does not exist"; err == nil || err.Error() != want {
		t.Fatalf("ReadDSN(a missing file) = %v, want %q", err, want)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDSN(a missing file) = %v, which is not fs.ErrNotExist", err)
	}
	empty := filepath.Join(t.TempDir(), pasted+"-empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadDSN(empty)
	if want := "the database URL file is empty"; err == nil || err.Error() != want {
		t.Fatalf("ReadDSN(an empty file) = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), pasted[:10]) {
		t.Fatalf("ReadDSN = %v, which names the path", err)
	}
}
