package launcher

// A Postgres server that is starting up or shutting down (SQLSTATE 57P03: a restart, a failover, a
// maintenance window) or that holds all the connections it can take (53300) answers every new
// connection with a refusal of its own and serves none: it does not answer the start's question,
// so a resume whose store answers that way is a store fault the daemon waits out a minute at a
// time, as the daemon's own store reads those classes (sqlstate.NotJudged). The control is a
// password the server refuses (28P01), a configuration fault the daemon charges, which also shows
// that the stand-in's answer reaches the launcher as the server's own (testdb.Refusing).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

func TestAnOhMyPiResumeWhoseStoreCannotServeAConnectionNowIsAStoreFault(t *testing.T) {
	shortenResumeLookup(t)
	sessionFile := filepath.Join(t.TempDir(), "sessions", "2026-10-08T12-00-00-000Z_0009.jsonl")
	for name, tc := range map[string]struct {
		code, message string
		storeFault    bool
	}{
		"the server is starting up (57P03)":                {code: "57P03", message: "the database system is starting up", storeFault: true},
		"the server is shutting down (57P03)":              {code: "57P03", message: "the database system is shutting down", storeFault: true},
		"the server takes no more clients (53300)":         {code: "53300", message: "sorry, too many clients already", storeFault: true},
		"control: the server refuses the password (28P01)": {code: "28P01", message: `password authentication failed for user "legion"`},
	} {
		t.Run(name, func(t *testing.T) {
			urlFile := filepath.Join(t.TempDir(), "dsn")
			if err := replaceURLFile(urlFile, "postgres://legion@"+testdb.Refusing(t, tc.code, tc.message)+"/none?sslmode=disable&connect_timeout=1"); err != nil {
				t.Fatal(err)
			}
			g := newRig(t)
			marker := filepath.Join(t.TempDir(), "marker")
			command := g.childStart(3, marker, nil)
			command.Env = append(command.Env, ompsessions.StorageVariable+"="+ompsessions.SQLStorage, ompsessions.DSNFileVariable+"="+urlFile)
			command.ResumeFile = sessionFile
			got := g.start(command)
			want := shimwire.SessionRefusal("")
			if tc.storeFault {
				want = shimwire.SessionStoreUnreachable
			}
			if got.OK || got.Session != want || !strings.Contains(got.Error, tc.message) {
				t.Fatalf("start = %#v, want a refusal naming %q with session refusal %q", got, tc.message, want)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused start started its child")
			}
		})
	}
}

// A store that takes a connection and ends it while it is being opened, before any answer of its
// own, by a reset or by a close (an end of file), as a server that crashes or restarts, or a TCP
// balancer in front of a store that is down, ends it, does not answer the start either: under a
// URL file the launcher's own connection saw the end (ompsessions.ConnectURL), so an Oh My Pi
// resume's lookup and a fresh Claude Code start's keeper are refused as a store fault the daemon
// waits out. The controls are a server that answers 57P03, a store fault by the server's own
// answer, and one that refuses the password (28P01), a configuration fault the daemon charges.
func TestAStartWhoseStoreEndsTheConnectionWhileItOpensIsAStoreFault(t *testing.T) {
	shortenResumeLookup(t)
	for name, tc := range map[string]struct {
		store      func(t *testing.T) string
		storeFault bool
	}{
		"the store resets the connection after its startup":       {store: func(t *testing.T) string { return testdb.Ending(t, true) }, storeFault: true},
		"the store closes the connection after its startup (EOF)": {store: func(t *testing.T) string { return testdb.Ending(t, false) }, storeFault: true},
		"control: the server is starting up (57P03)": {storeFault: true, store: func(t *testing.T) string {
			return testdb.Refusing(t, "57P03", "the database system is starting up")
		}},
		"control: the server refuses the password (28P01)": {store: func(t *testing.T) string {
			return testdb.Refusing(t, "28P01", `password authentication failed for user "legion"`)
		}},
	} {
		want := shimwire.SessionRefusal("")
		if tc.storeFault {
			want = shimwire.SessionStoreUnreachable
		}
		t.Run(name+", an Oh My Pi resume's lookup", func(t *testing.T) {
			urlFile := filepath.Join(t.TempDir(), "dsn")
			if err := replaceURLFile(urlFile, "postgres://legion@"+tc.store(t)+"/none?sslmode=disable&connect_timeout=1"); err != nil {
				t.Fatal(err)
			}
			g := newRig(t)
			marker := filepath.Join(t.TempDir(), "marker")
			command := g.childStart(3, marker, nil)
			command.Env = append(command.Env, ompsessions.StorageVariable+"="+ompsessions.SQLStorage, ompsessions.DSNFileVariable+"="+urlFile)
			command.ResumeFile = filepath.Join(t.TempDir(), "sessions", "2026-10-08T12-00-00-000Z_0011.jsonl")
			if got := g.start(command); got.OK || got.Session != want {
				t.Fatalf("start = %#v, want it refused with session refusal %q", got, want)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused start started its child")
			}
		})
		t.Run(name+", a fresh Claude Code start's keeper", func(t *testing.T) {
			_, dsnFile := testdb.DSNFile(t, "legion_launcher_store_ends_open_test")
			c := claudeRigOn(t, newSessionRig(t, Config{}), dsnFile)
			if err := replaceURLFile(c.dsnFile, "postgres://legion@"+tc.store(t)+"/none?sslmode=disable&connect_timeout=1"); err != nil {
				t.Fatal(err)
			}
			if got := c.start(c.command(1, seeing(c.transcript, filepath.Join(t.TempDir(), "seen")), false)); got.OK || got.Session != want {
				t.Fatalf("start = %#v, want it refused with session refusal %q", got, want)
			}
		})
	}
}
