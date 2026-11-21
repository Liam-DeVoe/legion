// Package projectaccesstest is a stand-in for Dispatch's three project-access routes, the ones
// the broker's projectaccess client asks: whether a person may see a project, what kind of member
// a person is, and every private project with its Slack channels. It answers from rows a test
// gives it, holds every caller to the one bearer it mints, counts the calls it answers, and can be
// made to fail, so a test can show what the broker does when Dispatch cannot be reached. It is an
// ordinary package rather than a _test.go file so every broker package's tests, and the real
// binary's, can serve it.
//
// It spells the wire on its own, field by field, rather than encoding the client's own type: a
// client that read a field under a name Dispatch does not send would then disagree with this
// stand-in, where one declaration shared by both would agree with itself and prove nothing.
package projectaccesstest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The paths of Dispatch's three project-access routes, as projectaccess asks them.
const (
	projectPath         = "/api/v1/access/projects/"
	personPath          = "/api/v1/access/person"
	privateProjectsPath = "/api/v1/access/private-projects"
)

// Row is one project as Dispatch holds it: whether it is public or private, its owner, the
// people its access list names, and the Slack channels its owner named.
type Row struct {
	// Visibility is "public" or "private".
	Visibility string
	// Owner is the owner's lowercase login, who sees the project whatever else is true.
	Owner string
	// Listed are the lowercase logins the project's access list names.
	Listed []string
	// SlackChannels are the Slack channel ids the project's owner named. A real Dispatch always
	// carries the key, empty when the project names none, so this stand-in does too — unless
	// OmitSlackChannels, which is a Dispatch from before dispatch://INFRA-3862's #t-a-31 and
	// carries no such key at all.
	SlackChannels []string
	// OmitSlackChannels makes this project's answer carry no slack_channels key, the shape a
	// Dispatch from before #t-a-31 answers.
	OmitSlackChannels bool
	// SlackChannelsChangedAt is when the owner last changed SlackChannels, or the project turned
	// private, as Dispatch answers it in slack_channels_changed_at; zero answers null, a list
	// never changed.
	SlackChannelsChangedAt time.Time
}

// Server is the stand-in: URL is its base, Token the one bearer it takes, and Account that bearer
// as the reader the broker's client presents.
type Server struct {
	// URL is the stand-in's base URL, what BROKER_PROJECT_ACCESS_URL names.
	URL string
	// Token is the one bearer the stand-in takes; every other bearer is refused 403.
	Token string
	// Account reads Token, as ciphertoken.AccountToken reads a projected token from its file.
	Account StaticAccount

	rows   map[string]Row
	people map[string]string

	mu    sync.Mutex
	calls int
	down  bool
	// privateCalls counts the private-projects route's requests alone, and privateDown fails
	// that route alone (SetPrivateProjectsDown).
	privateCalls int
	privateDown  bool
}

// StaticAccount is a bearer that never rotates: a ciphertoken.Account for a test.
type StaticAccount string

// Read answers the bearer.
func (a StaticAccount) Read() (string, error) { return string(a), nil }

// NewServer serves rows (by project key) and people (lowercase login to member kind: "full" or
// "restricted"; a login it does not hold is "none") until t ends. The maps are copied, so a test
// that goes on changing its own changes nothing here.
func NewServer(t testing.TB, rows map[string]Row, people map[string]string) *Server {
	t.Helper()
	var bearer [16]byte
	if _, err := rand.Read(bearer[:]); err != nil {
		t.Fatalf("mint the stand-in's bearer: %v", err)
	}
	s := &Server{Token: "dispatch-access-" + hex.EncodeToString(bearer[:]), rows: map[string]Row{}, people: map[string]string{}}
	for key, row := range rows {
		row.Listed = slices.Clone(row.Listed)
		row.SlackChannels = slices.Clone(row.SlackChannels)
		s.rows[key] = row
	}
	for login, kind := range people {
		s.people[login] = kind
	}
	s.Account = StaticAccount(s.Token)
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	s.URL = server.URL
	return s
}

// Calls is how many requests the stand-in has answered, refusals and the ones it failed while
// down included.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// SetDown makes every later request fail 503 (true) or be answered again (false), which is what a
// Dispatch the broker cannot reach looks like to it.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// SetPrivateProjectsDown makes every later request of the private-projects route fail 503 (true)
// or be answered again (false), while the project and person routes go on answering: a Dispatch
// that answers one project but not the list of all of them.
func (s *Server) SetPrivateProjectsDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.privateDown = down
}

// PrivateProjectsCalls is how many requests of the private-projects route the stand-in has
// answered, the ones it failed included.
func (s *Server) PrivateProjectsCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privateCalls
}

// SetRow makes Dispatch hold row for key from the next request on, as an owner's change of a
// project's visibility or access list does, so a test can change Dispatch under a client whose
// cache already holds the earlier answer.
func (s *Server) SetRow(key string, row Row) {
	row.Listed = slices.Clone(row.Listed)
	row.SlackChannels = slices.Clone(row.SlackChannels)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[key] = row
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls++
	down := s.down
	s.mu.Unlock()
	if down {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "UNAVAILABLE", "error": "the stand-in is down"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": "METHOD_NOT_ALLOWED", "error": r.Method})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "ACCESS_ACCOUNT_REQUIRED", "error": "the stand-in takes its own bearer alone"})
		return
	}
	person := r.URL.Query().Get("person")
	switch {
	case r.URL.Path == personPath:
		if person == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_QUERY", "error": "person is required"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"person": person, "member_kind": s.kind(person)})
	case r.URL.Path == privateProjectsPath:
		s.mu.Lock()
		s.privateCalls++
		privateDown := s.privateDown
		s.mu.Unlock()
		if privateDown {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "UNAVAILABLE", "error": "the stand-in's private-projects route is down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": s.privateProjects()})
	case strings.HasPrefix(r.URL.Path, projectPath):
		key := strings.TrimPrefix(r.URL.Path, projectPath)
		s.mu.Lock()
		row, held := s.rows[key]
		s.mu.Unlock()
		if !held || strings.Contains(key, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "PROJECT_NOT_FOUND", "error": "no such project"})
			return
		}
		answer := map[string]any{
			"project":     key,
			"visibility":  row.Visibility,
			"owner":       row.Owner,
			"person":      person,
			"member_kind": s.kind(person),
			"visible":     s.visible(row, person),
		}
		if !row.OmitSlackChannels {
			answer["slack_channels"] = channelsOf(row)
		}
		answer["slack_channels_changed_at"] = changedAtOf(row)
		writeJSON(w, http.StatusOK, answer)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "error": r.URL.Path})
	}
}

// privateProjects is the private-projects route's list: every private row, in key order, each
// with its channels and when they changed, and the channels left out for a row that omits them.
func (s *Server) privateProjects() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.rows))
	for key, row := range s.rows {
		if row.Visibility == "private" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	projects := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		row := s.rows[key]
		project := map[string]any{"project": key, "slack_channels_changed_at": changedAtOf(row)}
		if !row.OmitSlackChannels {
			project["slack_channels"] = channelsOf(row)
		}
		projects = append(projects, project)
	}
	return projects
}

// channelsOf is row's slack_channels as Dispatch spells it: never null, [] for none.
func channelsOf(row Row) []string {
	if row.SlackChannels == nil {
		return []string{}
	}
	return row.SlackChannels
}

// changedAtOf is row's slack_channels_changed_at as Dispatch spells it: null for a zero time.
func changedAtOf(row Row) any {
	if row.SlackChannelsChangedAt.IsZero() {
		return nil
	}
	return row.SlackChannelsChangedAt.UTC()
}

// kind is a login's member kind, "none" for a login the stand-in does not hold or no login at all.
func (s *Server) kind(person string) string {
	if kind, held := s.people[person]; held && person != "" {
		return kind
	}
	return "none"
}

// visible is Dispatch's rule as the spec states it: a person sees a project its access list names
// them on (its owner too), or a public project when they are not a restricted member; a query
// naming no person sees the public projects alone.
func (s *Server) visible(row Row, person string) bool {
	if person == "" {
		return row.Visibility == "public"
	}
	if person == row.Owner || slices.Contains(row.Listed, person) {
		return true
	}
	return row.Visibility == "public" && s.kind(person) != "restricted"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
