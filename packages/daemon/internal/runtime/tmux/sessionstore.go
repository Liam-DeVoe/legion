package tmux

import (
	"context"
	"fmt"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// sessionLookupTimeout bounds a resume's look-up of its session in the session table: connecting
// to the database and reading the row.
const sessionLookupTimeout = 30 * time.Second

// sessionInDatabase is whether the session a resume of backend names is a row of the session table
// rather than a file: an Oh My Pi session under a session database (Options.SessionDSNFile), keyed
// by the path its file would have had, as a pod's launcher holds a resume (internal/launcher).
// Every other session, a Claude Code transcript under either store, is a file.
func (r *Runtime) sessionInDatabase(backend runtime.Backend) bool {
	return r.sessionDSNFile != "" && backend == runtime.BackendOmp
}

// sessionWritten is whether the session table holds the session file names (ompsessions.Written).
// An error is a look-up that could not say: a database it could not reach or read.
func (r *Runtime) sessionWritten(ctx context.Context, file string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionLookupTimeout)
	defer cancel()
	conn, err := ompsessions.Connect(ctx, r.sessionDSNFile)
	if err != nil {
		return false, fmt.Errorf("resume session %s: %w", file, err)
	}
	defer conn.Close(context.Background())
	_, ok, err := ompsessions.Written(ctx, conn, file)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// recordedSession is where the session a resume names is, "" when it is nowhere, with the folders
// searched for it. Under a session database an Oh My Pi session is a row of the session table
// rather than a file (sessionInDatabase), so the row is the whole search: the session is at its
// recorded path or nowhere, and no folder is searched. Every other session is searched for as a
// file (sessionFileOf).
func (r *Runtime) recordedSession(ctx context.Context, file string, backend runtime.Backend, sessionsDir func() (string, error)) (string, []string, error) {
	if !r.sessionInDatabase(backend) {
		return sessionFileOf(file, sessionsDir)
	}
	written, err := r.sessionWritten(ctx, file)
	if err != nil || !written {
		return "", nil, err
	}
	return file, nil, nil
}
