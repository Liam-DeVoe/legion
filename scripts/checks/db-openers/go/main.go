//go:build ignore

// Db-openers is the Go half of scripts/checks/db-openers.ts, which runs it once for each Go module
// of the repository: the list of every call in the module's non-test Go files that opens or parses
// a Postgres connection string, each judged by what it passes.
//
//	go run scripts/checks/db-openers/go/*.go <module directory> [<package directory to plant in>]
//
// It reads every package of the module from source (every directory below the module's go.mod
// that holds Go files the build would compile, nested modules, testdata and vendor apart), and
// type-checks them together. The packages an opener comes from (database/sql, jackc/pgx and its
// pgconn, pgxpool and stdlib, lib/pq, and pgsetting, the one formatter) are read from the export
// data `go list -export` names; every other package outside the module is an empty one, so a
// module whose dependencies are large (an infrastructure SDK) is read without building them, and
// a call into such a package is never an opener.
//
// An opener form is a function of those packages that takes a connection string (a parameter
// named connString, dataSourceName or dsn) or a parsed configuration (pgx.ConnConfig,
// pgconn.Config, pgxpool.Config): the forms are read from the packages' own signatures, not from
// a list (forms.go). A call of a form, or of a wrapper of one, is judged by the argument in that
// place (judge.go):
//   - read by the formatter: the value pgsetting.ParseConfig or ParsePoolConfig answered, or a
//     value that a call of pgsetting.Refuse, ParseConfig or ParsePoolConfig before it, in the same
//     function, read;
//   - a constant, or a zero value: it holds no setting;
//   - the configuration another listed parse answered, or the configuration of a pool or a
//     connection that is already open, whose own opener is listed and judged;
//   - a parameter of the function the call is in: that function is then a wrapper, and each call
//     of it is listed and judged in its turn;
//   - anything else is unjudged: a field, a package variable, a value read from the environment
//     or a flag, the answer of another function. scripts/checks/db-openers.judged.ts says why each
//     such call cannot carry a misread setting, or the check fails.
//
// A form or a wrapper named as a value and not called is listed too, unjudged, since what it is
// later handed is not read. A wrapper that no file read calls (its callers are tests) is listed
// as one.
//
// It prints one JSON line for each form ({"form": …}), each call ({"call": …}), what the planted
// shapes gave when any was not as marked ({"blind": […]}), and a summary ({"summary": …}). The
// planted shapes (planted.go) are one more file of the package the second argument names, read
// with it: a line marked `planted: unjudged` must be listed unjudged, one marked `planted: judged`
// must be listed and judged, and a line marked neither must not be listed.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/checks/db-openers/go/*.go <module directory> [<package directory to plant in>]")
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	plantDir := ""
	if len(os.Args) == 3 {
		plantDir = os.Args[2]
	}
	m := load(root, os.Args[1], plantDir)
	a := newAnalysis(m)
	a.run()
	encoder := json.NewEncoder(os.Stdout)
	for _, f := range a.formList() {
		must(encoder.Encode(map[string]any{"form": f}))
	}
	planted := map[int]string{}
	for _, c := range a.calls {
		rec := a.record(c)
		if rec.File == filepath.ToSlash(plantedName) {
			// A line listed twice reads as both verdicts, which matches no mark.
			verdict := rec.Verdict
			if before, twice := planted[rec.Line]; twice {
				verdict = before + " and " + verdict
			}
			planted[rec.Line] = verdict
		}
		must(encoder.Encode(map[string]any{"call": rec}))
	}
	var blind []string
	if plantDir != "" {
		blind = plantedBlind(planted)
	}
	if len(m.files) == 0 {
		blind = append(blind, fmt.Sprintf("no Go file of the module %s was read", os.Args[1]))
	}
	if len(blind) > 0 {
		must(encoder.Encode(map[string]any{"blind": blind}))
	}
	must(encoder.Encode(map[string]any{"summary": a.summary()}))
}

func must(err error) {
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "db-openers:", err)
	os.Exit(1)
}
