package launcher

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
)

// openFailed is what the pinned Oh My Pi's report of a failed first open of its session database
// says, between the URL file it names and the failure: one line on its standard error, written just
// before it exits 1 (resolveSessionStorage in its src/session/session-storage-config.ts, and its
// src/main.ts):
//
//	Error: OMP_SESSION_SQL_DSN_FILE names <file>, but the session database could not be opened: <message> (<code>)
//
// <code> is its database driver's (Bun's SQL client) own name for the failure.
const openFailed = "but the session database could not be opened: "

// connectionFailed begins every code Bun's SQL client gives a connection that failed before the
// server answered it: refused (ERR_POSTGRES_CONNECTION_REFUSED), ended while it was being opened
// (ERR_POSTGRES_CONNECTION_FAILED, ERR_POSTGRES_CONNECTION_CLOSED) or not answered in time
// (ERR_POSTGRES_CONNECTION_TIMEOUT).
const connectionFailed = "ERR_POSTGRES_CONNECTION_"

// reportLine bounds the line a childReport holds while it waits for the line's end; a longer line
// is no report and is dropped up to its end.
const reportLine = 4096

// ansiColor is the colour a report can carry when the child's output is a terminal's.
var ansiColor = regexp.MustCompile("\x1b\\[[0-9;]*m")

// childReport reads a child's standard error, as it passes to the launcher's own, for the child's
// own report that its first open of its session database failed (openFailed). A failure the
// driver names as the connection's own (connectionFailed) is the store's, told by where it came
// from, as the launcher's own connection's is (ompsessions.ConnectURL). A server's answer
// (ERR_POSTGRES_SERVER_ERROR) is not decided here: the report carries the server's message and no
// SQLSTATE, so a store starting up (57P03) and a password the server refuses (28P01) read the
// same, and the store witness decides as it does for a child that reports nothing. Writes never
// fail, so the child's output goes on whatever its lines hold.
type childReport struct {
	mu    sync.Mutex
	line  []byte
	long  bool
	fault error
}

func (r *childReport) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for rest := b; len(rest) > 0; {
		end := bytes.IndexByte(rest, '\n')
		part := rest
		if end >= 0 {
			part = rest[:end]
		}
		if !r.long && len(r.line)+len(part) <= reportLine {
			r.line = append(r.line, part...)
		} else {
			r.long, r.line = true, r.line[:0]
		}
		if end < 0 {
			break
		}
		if !r.long {
			r.read(string(r.line))
		}
		r.line, r.long, rest = r.line[:0], false, rest[end+1:]
	}
	return len(b), nil
}

// read takes one whole line of the child's standard error.
func (r *childReport) read(line string) {
	_, detail, ok := strings.Cut(ansiColor.ReplaceAllString(line, ""), openFailed)
	if !ok {
		return
	}
	detail = strings.TrimSpace(detail)
	open := strings.LastIndexByte(detail, '(')
	if open < 0 || !strings.HasSuffix(detail, ")") {
		return
	}
	if code := detail[open+1 : len(detail)-1]; strings.HasPrefix(code, connectionFailed) {
		r.fault = fmt.Errorf("the child reported its session database could not be opened: %s", detail)
	}
}

// storeFault is the store's fault the child reported, nil for none or for a nil report.
func (r *childReport) storeFault() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fault
}

// alongside is w with the report reading what is written to it too; the report alone when w is
// nil, which exec.Cmd would have made the null device.
func (r *childReport) alongside(w io.Writer) io.Writer {
	if w == nil {
		return r
	}
	return io.MultiWriter(w, r)
}
