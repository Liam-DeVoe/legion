// handlers_private_projects.go: GET /v1/private-projects — the broker's own list of every private
// project, for the shared session watch alone. Part of the routes a listed service calls with its
// own ServiceAccount token (accountAuth), and the one route the readsPrivateList power reaches,
// which the caller name session-watch alone holds: the watch must never judge a private project's
// session, and the broker already decides with this list, so the watch reads it here rather than
// keep a copy of its own.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/redact"
)

// privateListHold is how long the route goes on answering the last list Dispatch gave while
// Dispatch cannot answer, counted from the earliest moment Dispatch can have given it. Past it the
// route answers 503 PRIVATE_LIST_UNKNOWN. So a list the watch reads lacks a project made private
// only while Dispatch could not answer, and only for this long.
const privateListHold = 5 * time.Minute

// CachedPrivateProjects is Dispatch's part of GET /v1/private-projects: every project Dispatch
// holds private, from the client's cache while the list it kept is younger than
// projectaccess.CacheTTL. *projectaccess.Client answers it.
type CachedPrivateProjects interface {
	PrivateProjects(ctx context.Context) ([]projectaccess.PrivateProject, error)
}

// privateProjectsResponse is GET /v1/private-projects' answer.
type privateProjectsResponse struct {
	// Every project the broker counts private, sorted, each key once: the ones Dispatch answers
	// private, with any the deployment itself declares private. An empty array when none is.
	PrivateProjects []string `json:"private_projects"`
}

// heldPrivateList is the last list of private projects Dispatch gave the route, which the route
// answers with while Dispatch cannot answer, for privateListHold from since.
type heldPrivateList struct {
	// started numbers the reads of Dispatch's list in the order they begin, so keep can tell the
	// later of two reads whose clock samples are equal.
	started atomic.Uint64
	mu      sync.Mutex
	keys    []string
	// since is the earliest moment Dispatch can have given keys, zero while the route holds no
	// list at all.
	since time.Time
	// kept is the read number that supplied keys. Read numbers order simultaneous clock samples.
	kept uint64
}

// keep holds keys from read, which Dispatch gave no earlier than since, unless the route already
// holds the answer to a later read: an answer that was slower to arrive never replaces it.
func (h *heldPrivateList) keep(keys []string, since time.Time, read uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if read > h.kept {
		h.keys, h.since, h.kept = slices.Clone(keys), since, read
	}
}

// held is the list the route holds and since, or a zero since when it holds none.
func (h *heldPrivateList) held() ([]string, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.keys), h.since
}

// privateProjects answers the shared session watch every project the broker counts private. A list
// the broker cannot read in full is 503 PRIVATE_LIST_UNKNOWN naming the kind of failure, never a
// shorter list: a short list is the one answer the watch cannot tell from the truth, and it reads
// a 503 as no list and fails closed.
func (s *server) privateProjects(w http.ResponseWriter, r *http.Request, _ string) {
	keys, err := s.privateProjectKeys(r.Context())
	if err != nil {
		slog.Error("broker: the list of private projects is unknown", "error", err)
		writeError(w, http.StatusServiceUnavailable, "PRIVATE_LIST_UNKNOWN", "the broker cannot say which projects are private: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, privateProjectsResponse{PrivateProjects: keys})
}

// privateProjectKeys is every project the broker counts private, sorted, each key once: Dispatch's
// (dispatchPrivateKeys) and the deployment's own (Deps.PrivateDeclared). It is an error, and never
// the declared keys alone, whenever Dispatch's part is unknown.
func (s *server) privateProjectKeys(ctx context.Context) ([]string, error) {
	keys, err := s.dispatchPrivateKeys(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.PrivateDeclared != nil {
		keys = append(keys, s.deps.PrivateDeclared()...)
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}

// dispatchPrivateKeys is the key of every project Dispatch answers private, asked of
// Deps.PrivateProjects on every call, whose cache keeps a list projectaccess.CacheTTL. While
// Dispatch cannot answer it is the list Dispatch gave last, for privateListHold from the earliest
// moment Dispatch can have given it; past that, with no list held, and on a broker with no
// BROKER_PROJECT_ACCESS_URL, it is an error naming the kind of failure. Every failure of the read
// counts: no answer in time, a refusal of the broker's token, any status but 200 and an answer
// with no projects list (projectaccess.Client.PrivateProjects refuses each of them). It answers a
// slice of its own.
func (s *server) dispatchPrivateKeys(ctx context.Context) ([]string, error) {
	if s.deps.PrivateProjects == nil {
		return nil, errors.New("this broker has no BROKER_PROJECT_ACCESS_URL, so it cannot ask Dispatch which projects are private")
	}
	// The client may answer from its cache, so the list it answers now can be as old as
	// projectaccess.CacheTTL: it is held from then, so that the hold bounds how old the list is
	// and not only how long ago this route read it.
	read := s.privateList.started.Add(1)
	since := s.privateListClock().Add(-projectaccess.CacheTTL)
	projects, err := s.deps.PrivateProjects.PrivateProjects(ctx)
	if err == nil {
		keys := make([]string, 0, len(projects))
		for _, project := range projects {
			keys = append(keys, project.Key)
		}
		s.privateList.keep(keys, since, read)
		return keys, nil
	}
	// A dependency error can repeat the broker's bearer in a refusal code or a malformed
	// answer. Only a fixed failure class may reach this route's response or its logs.
	if errors.Is(err, projectaccess.ErrBrokerRefused) {
		err = redact.Masked(err, projectaccess.ErrBrokerRefused.Error())
	} else {
		err = redact.Class(err)
	}
	keys, heldSince := s.privateList.held()
	if heldSince.IsZero() {
		return nil, fmt.Errorf("dispatch did not answer and no earlier answer is held: %w", err)
	}
	age := s.privateListClock().Sub(heldSince)
	if age >= privateListHold {
		return nil, fmt.Errorf("dispatch did not answer, and the last list it gave may be %s old, past the %s it is held: %w", age.Round(time.Second), privateListHold, err)
	}
	slog.Warn("broker: dispatch did not answer the list of private projects; answering the last list it gave", "age", age.Round(time.Second), "error", err)
	return keys, nil
}

// privateListClock is the time the held list is aged by: Deps.now where a test set one, and the
// wall clock otherwise.
func (s *server) privateListClock() time.Time {
	if s.deps.now != nil {
		return s.deps.now()
	}
	return time.Now()
}
