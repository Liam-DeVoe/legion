package storetest

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// At the teardown's bound Run writes, for each database still being dropped, what becomes of it
// (Run says why). For a drop the server is running the line names the drop's backend, its wait and
// its statement, and the process then ends without a cancel request, so the server finishes the
// drop once what it waits on lets it: that is what removes the database. The row holds every drop
// on the server with a backend that does not read (holdBarriers), leaves a drop the way the
// process's exit does, its client's socket closed and no cancel request sent, and releases the
// hold.
func TestADropLeftAtTheBoundIsNamedAndTheServerFinishesIt(t *testing.T) {
	Open(t) // opens the admin pool and claims this binary's run, or skips the test
	ctx := context.Background()
	name := randomName(t, "dispatch_test_")
	if _, err := shared.admin.Exec(ctx, "create database "+name+" strategy file_copy"); err != nil {
		t.Fatalf("create the database: %v", err)
	}
	t.Cleanup(func() { _, _ = shared.admin.Exec(context.Background(), "drop database if exists "+name) })

	holder, release := holdBarriers(t)
	client := connect(t)
	// Sent and never read, with no deadline, so pgx sends no cancel request.
	client.PgConn().Exec(context.Background(), "drop database "+name)
	var drop backend
	eventually(t, "the drop of "+name+" waits on IPC/ProcSignalBarrier", func() string {
		read, err := readActivity([]string{name})
		if err != nil {
			return err.Error()
		}
		drop = read.drops[name]
		if drop.wait == "IPC/ProcSignalBarrier" {
			return ""
		}
		return fmt.Sprintf("the drop's backend: %+v", drop)
	})

	read, err := readActivity([]string{name})
	if err != nil {
		t.Fatalf("read the server: %v", err)
	}
	line := leftLine(name, time.Minute, read)
	for _, want := range []string{
		fmt.Sprintf("pid %d in ", drop.pid),
		"waiting on IPC/ProcSignalBarrier",
		strconv.Quote("drop database " + name),
		"left to the server",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the teardown's line for %s does not say %q: %s", name, want, line)
		}
	}
	if others := othersLine(read); !strings.Contains(others, fmt.Sprintf("pid %d in ", holder)) || !strings.Contains(others, "waiting on Client/ClientWrite") {
		t.Errorf("the teardown's line of the server's other backends does not name the holding backend %d, waiting on Client/ClientWrite: %s", holder, others)
	}

	// Leave the drop as the process's exit does: its socket closes, and no cancel request is sent.
	_ = client.PgConn().Conn().Close()
	release()
	eventually(t, "the server finishes the drop of "+name+" its client left", func() string {
		read, err := readActivity([]string{name})
		if err != nil {
			return err.Error()
		}
		if read.gone[name] {
			return ""
		}
		return fmt.Sprintf("%s is still on the server; its drop: %+v", name, read.drops[name])
	})
}

// A database a storetest binary leaves on the server is dropped by the next binary's sweep once
// the binary that made it is gone, and never while it runs: the sweep drops the database of a run
// whose lock no session holds, and keeps one whose run's lock is held.
func TestTheSweepDropsADatabaseOfAGoneBinaryAndKeepsALiveBinarys(t *testing.T) {
	Open(t) // opens the admin pool and claims this binary's run, or skips the test
	ctx := context.Background()
	goneRun, liveRun := newRun(t), newRun(t)
	gone := "dispatch_test_" + goneRun + "_" + newRun(t)
	live := "dispatch_template_" + liveRun + "_" + newRun(t)

	// The live run's lock is held by a session of the row's own, as its binary would hold it. The
	// gone run's is held too while its database is made, so no other binary's sweep can take it
	// first, and freed just before the row's sweep.
	holder := connect(t)
	for _, run := range []string{goneRun, liveRun} {
		if _, err := holder.Exec(ctx, "select pg_advisory_lock($1)", runKey(run)); err != nil {
			t.Fatalf("take the lock of run %s: %v", run, err)
		}
	}
	for _, name := range []string{gone, live} {
		if _, err := shared.admin.Exec(ctx, "create database "+name+" strategy file_copy"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _, _ = shared.admin.Exec(context.Background(), "drop database if exists "+name) })
	}
	var freed bool
	if err := holder.QueryRow(ctx, "select pg_advisory_unlock($1)", runKey(goneRun)).Scan(&freed); err != nil || !freed {
		t.Fatalf("free the lock of run %s: %v (freed %v)", goneRun, err, freed)
	}

	dropped, err := sweepOrphans(ctx, connect(t))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	read, err := readActivity([]string{gone, live})
	if err != nil {
		t.Fatalf("read the server: %v", err)
	}
	if !read.gone[gone] {
		t.Errorf("the sweep left %s, whose binary's lock no session holds (it dropped %v)", gone, dropped)
	}
	if read.gone[live] {
		t.Errorf("the sweep dropped %s, whose binary's lock a session holds (it dropped %v)", live, dropped)
	}
}

// holdBarriers holds every DROP DATABASE on the server: a backend writing a large COPY to a
// client that reads none of it waits on Client/ClientWrite and accepts no barrier (measured on
// PostgreSQL 18.6). It returns that backend's pid, once it waits so, and a func that releases it:
// closing the client's socket ends the backend. The test's cleanup releases it too.
func holdBarriers(t *testing.T) (int, func()) {
	t.Helper()
	conn := connect(t)
	var pid int
	if err := conn.QueryRow(context.Background(), "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read the holding backend's pid: %v", err)
	}
	// Sent and never read.
	conn.PgConn().Exec(context.Background(), "copy (select repeat('x', 8000) from generate_series(1, 1000000)) to stdout")
	released := false
	release := func() {
		if !released {
			released = true
			_ = conn.PgConn().Conn().Close()
		}
	}
	t.Cleanup(release)
	eventually(t, fmt.Sprintf("backend %d waits on Client/ClientWrite", pid), func() string {
		read, err := readActivity(nil)
		if err != nil {
			return err.Error()
		}
		for _, b := range read.others {
			if b.pid == pid {
				if b.wait == "Client/ClientWrite" {
					return ""
				}
				return b.String()
			}
		}
		return "not on the server"
	})
	return pid, release
}

// connect is a connection of its own to the server's postgres database, closed after t ends.
func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), databaseURL(shared.server, "postgres"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// newRun is a new random run, as claimRun draws one.
func newRun(t *testing.T) string {
	t.Helper()
	run, err := randomSuffix()
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// eventually fails t naming what when check has not answered "" within eventuallyBound; check
// answers what it sees otherwise.
func eventually(t *testing.T, what string, check func() string) {
	t.Helper()
	deadline := time.Now().Add(eventuallyBound)
	for {
		seen := check()
		if seen == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; last seen: %s", eventuallyBound, what, seen)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// eventuallyBound bounds each wait of these rows: a hang guard, so a loaded runner's slowness
// does not decide them.
const eventuallyBound = 2 * time.Minute
