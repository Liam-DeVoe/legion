package launcher

// A child under a URL file reports a session database it could not open itself, on its standard
// error, and that report ties its exit to the store where the launcher's own looks missed the
// fault: a store that refused or ended the child's connection before any answer of its own. A
// server's own answer in that report carries no SQLSTATE, so it decides nothing.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// The lines are the pinned Oh My Pi's own, as it wrote them for each store (impl-4's
// omp-report-probe.log), with only the URL file's path shortened.
func TestAChildsOwnReportOfAStoreItCouldNotOpenIsReadByWhereItsFailureCameFrom(t *testing.T) {
	const prefix = "Error: OMP_SESSION_SQL_DSN_FILE names /tmp/dsn, but the session database could not be opened: "
	for name, tc := range map[string]struct {
		written []string
		fault   bool
	}{
		"the store refused the connection":                              {written: []string{prefix + "Failed to connect (ERR_POSTGRES_CONNECTION_REFUSED)\n"}, fault: true},
		"the store ended the connection while it opened":                {written: []string{prefix + "Connection closed before the connection was established (ERR_POSTGRES_CONNECTION_FAILED)\n"}, fault: true},
		"the report in pieces, and coloured":                            {written: []string{"Still starting after 10s\n\x1b[31m" + prefix[:20], prefix[20:] + "Failed to connect (ERR_POSTG", "RES_CONNECTION_REFUSED)\x1b[39m\n"}, fault: true},
		"control: a server's own answer, the store starting up (57P03)": {written: []string{prefix + "the database system is starting up (ERR_POSTGRES_SERVER_ERROR)\n"}},
		"control: a server's own answer, a password it refuses (28P01)": {written: []string{prefix + "password authentication failed for user \"legion\" (ERR_POSTGRES_SERVER_ERROR)\n"}},
		"control: a report with no end of line":                         {written: []string{prefix + "Failed to connect (ERR_POSTGRES_CONNECTION_REFUSED)"}},
		"control: a line too long to be a report":                       {written: []string{strings.Repeat("x", reportLine) + prefix + "Failed to connect (ERR_POSTGRES_CONNECTION_REFUSED)\n"}},
		"control: no report":                                            {written: []string{"Still starting after 10s — phase: resolveSessionStorage\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			report := &childReport{}
			for _, w := range tc.written {
				if n, err := report.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(w))
				}
			}
			if got := report.storeFault(); (got != nil) != tc.fault {
				t.Fatalf("the report read as %v; want a store fault %t", got, tc.fault)
			}
		})
	}
}

// The pinned Oh My Pi's own report decides its exit where the launcher saw nothing: the store
// answers the launcher's look at the child's start, then refuses every dial, so the pinned Oh My
// Pi's first open is refused, and the stop grace leaves no time to ask the store after the exit.
// The control is a server that refuses the password (28P01): Oh My Pi reports that the server
// answered, which decides nothing, and the exit is charged.
func TestAPinnedOhMyPiThatReportsItsStoreRefusedItExitsAsAStoreFault(t *testing.T) {
	ompBinary := testbin.OMP(t)
	home := t.TempDir()
	testbin.OMPHome(t, ompBinary, home)
	testbin.WriteOMPProfile(t, home, "providers:\n  operator:\n    baseUrl: http://127.0.0.1:9\n    auth: apiKey\n    api: anthropic-messages\n"+
		"    apiKey: unused\n    models:\n      - id: store-fault-test\n        name: Store fault test\n", "modelRoles:\n  default: operator/store-fault-test\n")
	for name, tc := range map[string]struct {
		refusing bool
		want     shimwire.SessionRefusal
	}{
		"the store answers the launcher's look, then refuses Oh My Pi's open": {refusing: true, want: shimwire.SessionStoreUnreachable},
		"control: the server refuses Oh My Pi's password (28P01)":             {},
	} {
		t.Run(name, func(t *testing.T) {
			lookEvery(t, time.Hour)
			url := "postgres://legion@" + testdb.Refusing(t, "28P01", `password authentication failed for user "legion"`) + "/none?sslmode=disable"
			var store *testdb.Flapping
			if tc.refusing {
				store = testdb.NewFlapping(t, testdb.Database(t, "legion_launcher_child_report_test"), false)
				url = store.URL
			}
			urlFile := filepath.Join(t.TempDir(), "dsn")
			if err := replaceURLFile(urlFile, url); err != nil {
				t.Fatal(err)
			}
			// A stop grace this short leaves the launcher no time to ask the store after the exit.
			g := newSessionRig(t, Config{StopGrace: time.Nanosecond})
			marker := filepath.Join(t.TempDir(), "child")
			env := append(testbin.OMPEnviron(home), ompsessions.StorageVariable+"="+ompsessions.SQLStorage,
				ompsessions.DSNFileVariable+"="+urlFile, "LEGION_WORKSPACE="+filepath.Join(t.TempDir(), "workspace"))
			argv := []string{"/bin/sh", "-c", `while [ ! -e "$1" ]; do sleep 0.02; done; exec "$2" --mode rpc`, "sh", marker + ".go", ompBinary}
			if got := g.start(shimwire.LauncherStart{ID: "start-1", Generation: 1, Argv: argv, Env: env}); !got.OK {
				t.Fatalf("start = %#v, want the child started", got)
			}
			if tc.refusing {
				awaitAtLeast(t, "connections passed", &store.Passed, 1)
				store.Refuse()
			}
			markFile(t, marker+".go")
			exit := g.awaitExitWithin(1, 2*time.Minute)
			if exit.Code != 1 || exit.Session != tc.want {
				t.Fatalf("the child's exit = %+v, want status 1 and session refusal %q\nlauncher log:\n%s", exit, tc.want, g.stderr.String())
			}
			if !strings.Contains(g.stderr.String(), openFailed) {
				t.Fatalf("Oh My Pi wrote no report of the store it could not open; launcher log:\n%s", g.stderr.String())
			}
		})
	}
}
