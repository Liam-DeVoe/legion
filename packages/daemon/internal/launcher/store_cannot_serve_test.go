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
