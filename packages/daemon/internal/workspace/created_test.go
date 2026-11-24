package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The in-use record is the launcher's fence against a bundle restored over a workspace a role
// has worked: MarkInUse writes it once, InUse reads it, and a record a role could plant in its
// place — a link — counts as in use without being followed. A .jj that cannot take the record
// is MarkInUse's error, which is what keeps a role from starting there.
func TestMarkInUseIsReadByInUseAndWritesOnce(t *testing.T) {
	fresh := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".jj"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	dir := fresh(t)
	if inUse, err := InUse(dir); inUse || err != nil {
		t.Fatalf("InUse of a fresh .jj = %t, %v; want false and no error", inUse, err)
	}
	if err := MarkInUse(dir); err != nil {
		t.Fatalf("MarkInUse = %v", err)
	}
	if inUse, err := InUse(dir); !inUse || err != nil {
		t.Fatalf("InUse after MarkInUse = %t, %v; want true", inUse, err)
	}
	if err := MarkInUse(dir); err != nil {
		t.Fatalf("a second MarkInUse = %v, want nil: the record is already there", err)
	}

	linked := fresh(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Symlink(target, filepath.Join(linked, ".jj", inUseFile)); err != nil {
		t.Fatal(err)
	}
	if inUse, err := InUse(linked); !inUse || err != nil {
		t.Fatalf("InUse of a link at the record's path = %t, %v; want true", inUse, err)
	}
	if err := MarkInUse(linked); err != nil {
		t.Fatalf("MarkInUse over a link at the record's path = %v, want nil", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the link's target %s was made (%v): the record's path was followed", target, err)
	}

	t.Run("a .jj that is no directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".jj"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MarkInUse(dir); !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("MarkInUse = %v, want ENOTDIR", err)
		}
		if inUse, err := InUse(dir); err == nil {
			t.Fatalf("InUse = %t and no error, want an error", inUse)
		}
	})

	t.Run("a .jj that refuses a create", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("as root a 0555 directory does not refuse the create")
		}
		dir := fresh(t)
		jj := filepath.Join(dir, ".jj")
		if err := os.Chmod(jj, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(jj, 0o755) })
		if err := MarkInUse(dir); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("MarkInUse = %v, want a permission error", err)
		}
		if inUse, err := InUse(dir); inUse || err != nil {
			t.Fatalf("InUse = %t, %v; want false and no error: only MarkInUse's error keeps the role from starting", inUse, err)
		}
	})
}

// A .jj that is a symbolic link is refused before a record in it is read or written through it.
// .jj is on the issue's volume, which every agent of the issue can write, and a link there
// carries the records into a directory off the workspace's disk: a record read there says nothing
// of this workspace, and one written there can be lost at a container restart while the workspace
// keeps its work, so a bundle could be restored over that work. InUse fails, which the restore
// counts as in use, and MarkInUse fails, as it does for a .jj that cannot take the record, and
// writes nothing through the link. Created fails, which the restore takes as a workspace it
// cannot tell from the one the work was saved from, and RecordCreated writes nothing through the
// link. Nothing here needs root, so nothing skips as root.
func TestALinkedJJIsRefusedBeforeTheInUseRecordIsReadOrWritten(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, ".jj")); err != nil {
		t.Fatal(err)
	}
	if inUse, err := InUse(dir); !errors.Is(err, errLinkedJJ) {
		t.Errorf("InUse of a linked .jj = %t, %v; want errLinkedJJ", inUse, err)
	}
	if err := MarkInUse(dir); !errors.Is(err, errLinkedJJ) {
		t.Errorf("MarkInUse over a linked .jj = %v, want errLinkedJJ", err)
	}
	if err := RecordCreated(dir, time.Now()); !errors.Is(err, errLinkedJJ) {
		t.Errorf("RecordCreated over a linked .jj = %v, want errLinkedJJ", err)
	}
	for _, name := range []string{inUseFile, createdFile} {
		if _, err := os.Lstat(filepath.Join(target, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was written through the link into %s (%v)", name, target, err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, createdFile), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := Created(dir); !errors.Is(err, errLinkedJJ) {
		t.Errorf("Created through a linked .jj = %s, %t, %v; want errLinkedJJ", at, ok, err)
	}
}

// A .jj swapped for a link after it was opened changes nothing either: the records are read and
// written through the directory that open reached, never by its path again. Each row opens a
// workspace's real .jj, then, in the window between that open and the record's read or write
// (the held seam), moves the real directory aside and puts a link to another directory at .jj, as
// an agent of the issue's volume could. Read by its path after that check, a record would be the
// other directory's: a marker made there, a marker missed or planted there, a creation moment
// forged there. Read through the open directory it is the workspace's own.
func TestAJJSwappedForALinkAfterItsOpenReachesNoOtherDirectory(t *testing.T) {
	saved := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	record := func(t *testing.T, jj string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(jj, createdFile), []byte(at.Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mark := func(t *testing.T, jj string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(jj, inUseFile), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	marked := func(jj string) bool {
		_, err := os.Lstat(filepath.Join(jj, inUseFile))
		return err == nil
	}
	// workspace is a workspace with a real .jj and another directory, elsewhere; swap moves the
	// real .jj to own and puts a link to elsewhere at .jj.
	workspace := func(t *testing.T) (dir, own, elsewhere string, swap func()) {
		t.Helper()
		dir, elsewhere = t.TempDir(), t.TempDir()
		jj := filepath.Join(dir, ".jj")
		own = filepath.Join(dir, ".jj-own")
		if err := os.Mkdir(jj, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir, own, elsewhere, func() {
			if err := os.Rename(jj, own); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, jj); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("MarkInUse marks the directory it opened", func(t *testing.T) {
		dir, own, elsewhere, swap := workspace(t)
		if err := markInUse(dir, swap); err != nil {
			t.Fatalf("MarkInUse = %v", err)
		}
		if marked(elsewhere) {
			t.Error("the in-use record was made in the directory the swapped link names")
		}
		if !marked(own) {
			t.Error("the workspace's own .jj holds no in-use record")
		}
	})
	t.Run("InUse reads a marked workspace as in use", func(t *testing.T) {
		dir, own, _, swap := workspace(t)
		mark(t, filepath.Join(dir, ".jj"))
		if got, err := inUse(dir, swap); !got || err != nil {
			t.Errorf("InUse of a marked workspace whose .jj was swapped for a link to an unmarked directory = %t, %v; want true", got, err)
		}
		if !marked(own) {
			t.Fatal("the case's premise: the workspace's own .jj is marked")
		}
	})
	t.Run("InUse reads no marker planted elsewhere", func(t *testing.T) {
		dir, _, elsewhere, swap := workspace(t)
		mark(t, elsewhere)
		if got, err := inUse(dir, swap); got || err != nil {
			t.Errorf("InUse of an unmarked workspace whose .jj was swapped for a link to a marked directory = %t, %v; want false", got, err)
		}
	})
	t.Run("Created reads the workspace's own moment", func(t *testing.T) {
		dir, _, elsewhere, swap := workspace(t)
		record(t, filepath.Join(dir, ".jj"), saved.Add(-time.Hour))
		record(t, elsewhere, saved.Add(time.Hour))
		at, ok, err := created(dir, swap)
		if err != nil || !ok || !at.Equal(saved.Add(-time.Hour)) {
			t.Errorf("Created of a workspace whose .jj was swapped for a link = %s, %t, %v; want its own %s", at, ok, err, saved.Add(-time.Hour))
		}
	})
	t.Run("Created names the directory it opened", func(t *testing.T) {
		dir, _, elsewhere, swap := workspace(t)
		path := filepath.Join(dir, ".jj", "legion-created")
		if err := os.Symlink(filepath.Join(elsewhere, "record"), path); err != nil {
			t.Fatal(err)
		}
		_, ok, err := created(dir, swap)
		var pathErr *fs.PathError
		if ok || !errors.As(err, &pathErr) || pathErr.Path != path {
			t.Errorf("Created after .jj was moved = %t, %v; want a refusal naming %s", ok, err, path)
		}
	})
}

// The restore record is how a launcher restarted over the workspace's disk knows what the one
// before it did: a restore begun and cut off, or ended with a report the daemon is still owed.
// RecordRestore writes it whole over the one before, ReadRestoreRecord reads back what was
// written, and RemoveRestoreRecord takes it off, so a workspace that never had one and one whose
// report reached the daemon both read as none. A record that is not one the launcher wrote is
// refused by name rather than read as a report.
func TestTheRestoreRecordIsReadAsItWasWrittenUntilItIsRemoved(t *testing.T) {
	dir := t.TempDir()
	if record, ok, err := ReadRestoreRecord(dir); ok || err != nil {
		t.Fatalf("ReadRestoreRecord of a directory with no .jj = %+v, %t, %v; want none and no error", record, ok, err)
	}
	if err := RemoveRestoreRecord(dir); err != nil {
		t.Fatalf("RemoveRestoreRecord of a directory with no .jj = %v, want nil", err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if record, ok, err := ReadRestoreRecord(dir); ok || err != nil {
		t.Fatalf("ReadRestoreRecord of a fresh .jj = %+v, %t, %v; want none and no error", record, ok, err)
	}

	begun := RestoreRecord{ID: "restore-INFRA-1117-11", Release: 11}
	ended := RestoreRecord{ID: begun.ID, Release: begun.Release, Ended: true, Error: "the released workspace's saved work was not restored (a reason)"}
	for _, want := range []RestoreRecord{begun, ended} {
		if err := RecordRestore(dir, want); err != nil {
			t.Fatalf("RecordRestore(%+v) = %v", want, err)
		}
		if got, ok, err := ReadRestoreRecord(dir); !ok || err != nil || got != want {
			t.Fatalf("ReadRestoreRecord after RecordRestore(%+v) = %+v, %t, %v", want, got, ok, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".jj", restoreTemp)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the write left its temporary file behind (%v)", err)
		}
	}
	if err := RemoveRestoreRecord(dir); err != nil {
		t.Fatalf("RemoveRestoreRecord = %v", err)
	}
	if record, ok, err := ReadRestoreRecord(dir); ok || err != nil {
		t.Fatalf("ReadRestoreRecord after RemoveRestoreRecord = %+v, %t, %v; want none", record, ok, err)
	}
	if err := RemoveRestoreRecord(dir); err != nil {
		t.Fatalf("a second RemoveRestoreRecord = %v, want nil: there is nothing to remove", err)
	}

	for name, body := range map[string]string{
		"not JSON":                   "begun\n",
		"no slice sequence":          "{}\n",
		"over the record's own size": `{"id":"x","error":"` + strings.Repeat("a", restoreRecordLimit) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, ".jj"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".jj", restoreFile)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, ok, err := ReadRestoreRecord(dir); ok || err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("ReadRestoreRecord = %+v, %t, %v; want a refusal naming %s", got, ok, err, path)
			}
		})
	}
}

// The restore record is on the issue's volume, which every agent of the issue can write, so a link
// or a directory planted at its name is never taken for the record: a link is not followed, read
// or written through, and a directory is refused by every call. A .jj that is a link is refused
// before the record is read, written or removed, as for the in-use record.
func TestARestoreRecordThatIsALinkOrADirectoryIsRefused(t *testing.T) {
	fresh := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".jj"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	record := RestoreRecord{ID: "restore-INFRA-1117-11", Release: 11, Ended: true, OK: true}

	t.Run("a link at the record's name", func(t *testing.T) {
		dir := fresh(t)
		// The link names a record a launcher could have written, so only a read that follows it
		// would answer one.
		elsewhere := fresh(t)
		if err := RecordRestore(elsewhere, record); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(elsewhere, ".jj", restoreFile)
		planted, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, ".jj", restoreFile)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := ReadRestoreRecord(dir); ok || !errors.Is(err, syscall.ELOOP) {
			t.Fatalf("ReadRestoreRecord of a link = %+v, %t, %v; want ELOOP, the link not followed", got, ok, err)
		}
		begun := RestoreRecord{ID: "restore-INFRA-1117-12", Release: 12}
		if err := RecordRestore(dir, begun); err != nil {
			t.Fatalf("RecordRestore over a link = %v", err)
		}
		if got, ok, err := ReadRestoreRecord(dir); !ok || err != nil || got != begun {
			t.Fatalf("after RecordRestore over a link ReadRestoreRecord = %+v, %t, %v; want the record written in the link's place", got, ok, err)
		}
		if err := RemoveRestoreRecord(dir); err != nil {
			t.Fatalf("RemoveRestoreRecord = %v", err)
		}
		if after, err := os.ReadFile(target); err != nil || string(after) != string(planted) {
			t.Fatalf("the link's target reads %q (%v) after the write and the remove, want it as it was, %q", after, err, planted)
		}
	})

	t.Run("a directory at the record's name", func(t *testing.T) {
		dir := fresh(t)
		path := filepath.Join(dir, ".jj", restoreFile)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := ReadRestoreRecord(dir); ok || err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("ReadRestoreRecord of a directory = %+v, %t, %v; want a refusal of what is not a regular file", got, ok, err)
		}
		if err := RecordRestore(dir, record); err == nil {
			t.Fatal("RecordRestore over a directory succeeded, want it refused")
		}
		if err := RemoveRestoreRecord(dir); err == nil {
			t.Fatal("RemoveRestoreRecord of a directory succeeded, want it refused")
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("the directory at the record's name is gone or changed (%v)", err)
		}
	})

	t.Run("a .jj that is a link", func(t *testing.T) {
		dir, target := t.TempDir(), t.TempDir()
		if err := os.Symlink(target, filepath.Join(dir, ".jj")); err != nil {
			t.Fatal(err)
		}
		if err := RecordRestore(dir, record); !errors.Is(err, errLinkedJJ) {
			t.Errorf("RecordRestore through a linked .jj = %v, want errLinkedJJ", err)
		}
		if _, err := os.Lstat(filepath.Join(target, restoreFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the record was written through the link into %s (%v)", target, err)
		}
		if err := os.WriteFile(filepath.Join(target, restoreFile), []byte(`{"id":"planted","ended":true,"ok":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := ReadRestoreRecord(dir); ok || !errors.Is(err, errLinkedJJ) {
			t.Errorf("ReadRestoreRecord through a linked .jj = %+v, %t, %v; want errLinkedJJ", got, ok, err)
		}
		if err := RemoveRestoreRecord(dir); !errors.Is(err, errLinkedJJ) {
			t.Errorf("RemoveRestoreRecord through a linked .jj = %v, want errLinkedJJ", err)
		}
		if _, err := os.Lstat(filepath.Join(target, restoreFile)); err != nil {
			t.Errorf("the record planted in %s was removed through the link (%v)", target, err)
		}
	})
}
