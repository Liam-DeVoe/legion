package textcheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// lockWaitEvent is a comparison of pg_stat_activity's wait_event_type with Lock, or of its
// wait_event with one of the lock types a lock wait reports (pg_locks' locktype values), in either
// case and across lines.
var lockWaitEvent = regexp.MustCompile(`(?i)wait_event(_type)?\s*(=|<>|!=)\s*'(lock|relation|extend|frozenid|page|tuple|transactionid|virtualxid|spectoken|object|userlock|advisory|applytransaction)'`)

// No Go file of this module reads whether a backend waits on a lock from pg_stat_activity's
// wait_event_type or wait_event. A waiting backend's lock request stays queued in pg_locks for the
// whole wait, but its wait event reads Lock only while it sleeps, and it leaves that sleep each
// time it is woken: by its own deadlock check, by any signal it takes. A test that sampled the
// wait event once read 0 of the daemon's backends waiting while its one connection waited
// (TestReadyzIsFalseUntilTheDaemonServes, CI run 45206462019). The read is pg_locks':
// `pid in (select pid from pg_locks where not granted)`, with `and locktype = '...'` for a lock of
// one type, or pg_blocking_pids, which reads the same queue.
func TestNoTestReadsALockWaitFromItsWaitEvent(t *testing.T) {
	read, offences, err := lockWaitEventReads("../..")
	if err != nil {
		t.Fatalf("read the module's Go files: %v", err)
	}
	if read == 0 {
		t.Fatal("read no Go file under ../..: the check would pass having seen nothing")
	}
	if len(offences) > 0 {
		t.Fatalf("%d reads of a lock wait from a wait event, which reads no Lock while the waiter is woken:\n\t%s\n"+
			"read the wait from pg_locks: pid in (select pid from pg_locks where not granted)",
			len(offences), strings.Join(offences, "\n\t"))
	}
}

// lockWaitEventReads walks the Go files under root, passing over node_modules, vendor, testdata,
// dot directories and any directory that holds a go.mod of its own (another module, which that
// module's check reads), and answers how many it read and each comparison lockWaitEvent finds, as
// root-relative path and line.
func lockWaitEventReads(root string) (int, []string, error) {
	read := 0
	var offences []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			if name == "node_modules" || name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		read++
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, at := range lockWaitEvent.FindAllIndex(raw, -1) {
			offences = append(offences, fmt.Sprintf("%s:%d", filepath.ToSlash(relative), 1+strings.Count(string(raw[:at[0]]), "\n")))
		}
		return nil
	})
	return read, offences, err
}
